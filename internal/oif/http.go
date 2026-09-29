package oif

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

type Store interface {
	Enqueue(context.Context, string, string) (bool, error)
	Record(context.Context, string) (coordination.Record, error)
	Control(context.Context) (coordination.Control, error)
}
type Handler struct {
	next              time.Time
	Store             Store
	Accepted          func()
	Prepare           func(intent.Candidate) (intent.Candidate, error)
	Token             string
	Node              string
	Provider          string
	Routes            []Route
	QuoteKey          []byte
	RequestsPerSecond int
	mu                sync.Mutex
	Enabled           bool
}

func (h *Handler) HTTP() http.Handler {
	slots := make(chan struct{}, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/quotes", h.quotes)
	mux.HandleFunc("POST /v1/orders", h.submit)
	mux.HandleFunc("GET /v1/orders/{id...}", h.status)
	mux.HandleFunc("GET /v1/assets", h.assets)
	return http.TimeoutHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(h.Token) < 32 || subtle.ConstantTimeCompare([]byte(token), []byte(h.Token)) != 1 {
			fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			fail(w, http.StatusTooManyRequests, "request concurrency exhausted")
			return
		}
		h.mu.Lock()
		ready := !time.Now().Before(h.next)
		if ready {
			h.next = time.Now().Add(time.Second / time.Duration(h.RequestsPerSecond))
		}
		h.mu.Unlock()
		if !ready {
			w.Header().Set("Retry-After", "1")
			fail(w, http.StatusTooManyRequests, "request budget exhausted")
			return
		}
		mux.ServeHTTP(w, r)
	}), 10*time.Second, "request timed out")
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return ErrUnsupported
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrUnsupported
	}
	return nil
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, struct {
		Message string `json:"message"`
	}{Message: message})
}

func (h *Handler) available(ctx context.Context) bool {
	if !h.Enabled {
		return false
	}
	state, err := h.Store.Control(ctx)
	return err == nil && state.Allows(h.Node, 0, 1)
}

func (h *Handler) quotes(w http.ResponseWriter, r *http.Request) {
	if !h.available(r.Context()) {
		fail(w, http.StatusServiceUnavailable, "execution unavailable or paused")
		return
	}
	var request QuoteRequest
	if decode(w, r, &request) != nil {
		fail(w, http.StatusBadRequest, "invalid quote request")
		return
	}
	response := QuoteResponse{Quotes: []Quote{}}
	failures, quotes := h.collectQuotes(r.Context(), request)
	unavailable := false
	for i, err := range failures {
		if err != nil {
			unavailable = unavailable || !errors.Is(err, ErrUnsupported)
			continue
		}
		q := quotes[i]
		q.Provider = h.Provider
		token, err := h.sign(q)
		if err != nil {
			fail(w, http.StatusInternalServerError, "quote encoding failed")
			return
		}
		q.QuoteID = token
		response.Quotes = append(response.Quotes, q)
	}
	if len(response.Quotes) == 0 {
		if unavailable {
			fail(w, http.StatusServiceUnavailable, "matching route unavailable")
		} else {
			fail(w, http.StatusBadRequest, "unsupported quote request")
		}
		return
	}
	write(w, http.StatusOK, response)
}

func (h *Handler) collectQuotes(ctx context.Context, request QuoteRequest) ([]error, []Quote) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	type result struct {
		err   error
		quote Quote
		index int
	}
	results := make(chan result, len(h.Routes))
	for i, route := range h.Routes {
		go func() { q, err := route.Quote(ctx, request); results <- result{quote: q, err: err, index: i} }()
	}
	failures := make([]error, len(h.Routes))
	quotes := make([]Quote, len(h.Routes))
	for i := range failures {
		failures[i] = context.DeadlineExceeded
	}
	for range h.Routes {
		select {
		case value := <-results:
			failures[value.index] = value.err
			quotes[value.index] = value.quote
		case <-ctx.Done():
			return failures, quotes
		}
	}
	return failures, quotes
}

func (h *Handler) sign(q Quote) (string, error) {
	raw, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, h.QuoteKey)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (h *Handler) verify(submission Submission) error {
	parts := strings.Split(submission.QuoteID, ".")
	if len(parts) != 2 {
		return ErrUnsupported
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrUnsupported
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrUnsupported
	}
	mac := hmac.New(sha256.New, h.QuoteKey)
	_, _ = mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return ErrUnsupported
	}
	var q Quote
	if json.Unmarshal(raw, &q) != nil || q.ValidUntil <= time.Now().Unix() {
		return ErrUnsupported
	}
	expected, err := json.Marshal(q.Order)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(submission.Order)
	if err != nil || !bytes.Equal(actual, expected) {
		return ErrUnsupported
	}
	return nil
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	if !h.available(r.Context()) {
		fail(w, http.StatusServiceUnavailable, "execution unavailable or paused")
		return
	}
	var request Submission
	if decode(w, r, &request) != nil || request.Order.Type != UserOpen || len(request.Signature) != 0 || !UserSubmitted(request.OriginSubmission) {
		write(w, http.StatusBadRequest, SubmissionResponse{Status: Rejected, Message: "unsupported order or authorization"})
		return
	}
	if request.QuoteID != "" && h.verify(request) != nil {
		write(w, http.StatusBadRequest, SubmissionResponse{Status: Rejected, Message: "invalid, changed, or expired quote"})
		return
	}
	var candidate intent.Candidate
	for _, route := range h.Routes {
		value, err := route.Decode(request.Order)
		if err == nil {
			candidate = value
			break
		}
	}
	if candidate.Kind == "" {
		write(w, http.StatusBadRequest, SubmissionResponse{Status: Rejected, Message: "order does not match an enabled route"})
		return
	}
	prepared, err := h.Prepare(candidate)
	if err != nil {
		write(w, http.StatusBadRequest, SubmissionResponse{Status: Rejected, Message: "intent rejected by execution policy"})
		return
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		fail(w, http.StatusInternalServerError, "intent encoding failed")
		return
	}
	added, err := h.Store.Enqueue(r.Context(), prepared.Identity().Key(), string(raw))
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, coordination.ErrConflict) {
			status = http.StatusConflict
		}
		write(w, status, SubmissionResponse{Status: SubmissionError, Message: "durable intake failed"})
		return
	}
	if added && h.Accepted != nil {
		h.Accepted()
	}
	write(w, http.StatusOK, SubmissionResponse{Status: Received, OrderID: prepared.Identity().Key()})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := intent.ParseIdentity(id); err != nil {
		fail(w, http.StatusBadRequest, "invalid intent identifier")
		return
	}
	record, err := h.Store.Record(r.Context(), id)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, coordination.ErrNotFound) {
			status = http.StatusNotFound
		}
		fail(w, status, "intent unavailable")
		return
	}
	for _, route := range h.Routes {
		response, err := route.Status(record)
		if err == nil {
			response.ID = record.ID
			response.CreatedAt = record.CreatedAt
			response.UpdatedAt = record.UpdatedAt
			write(w, http.StatusOK, response)
			return
		}
	}
	fail(w, http.StatusNotFound, "intent has no enabled OIF adapter")
}

func (h *Handler) assets(w http.ResponseWriter, _ *http.Request) {
	response := AssetsResponse{Networks: map[string]Network{}}
	for _, route := range h.Routes {
		for key, network := range route.Assets() {
			current := response.Networks[key]
			current.ChainID = network.ChainID
			for _, asset := range network.Assets {
				exists := false
				for _, old := range current.Assets {
					exists = exists || old.Address == asset.Address
				}
				if !exists {
					current.Assets = append(current.Assets, asset)
				}
			}
			response.Networks[key] = current
		}
	}
	write(w, http.StatusOK, response)
}
