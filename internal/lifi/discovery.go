package lifi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type Stream struct {
	API     *Client
	Log     *zap.Logger
	Resolve func(context.Context, Envelope) (intent.Candidate, error)
	URL     string
	Key     string
	Filters []url.Values
}

func (s *Stream) Identity() intent.SourceID {
	filters := make([]string, 0, len(s.Filters))
	for _, filter := range s.Filters {
		values := make(url.Values, len(filter))
		for key, list := range filter {
			values[key] = slices.Clone(list)
			slices.Sort(values[key])
		}
		filters = append(filters, values.Encode())
	}
	slices.Sort(filters)
	digest := sha256.Sum256([]byte(s.URL + "\n" + s.Key + "\n" + strings.Join(filters, "\n")))
	return intent.SourceID("lifi-stream-v1/" + hex.EncodeToString(digest[:]))
}

type notification struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

const (
	submitEvent = "user:vm-order-submit"
	pingEvent   = "ping"
	pongEvent   = "pong"
)

func ValidateStreamURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "wss" && !(u.Scheme == "ws" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return errors.New("WebSocket requires wss or loopback ws without credentials")
	}
	return nil
}

func (s *Stream) Run(ctx context.Context, emit intent.Emit) error {
	if err := ValidateStreamURL(s.URL); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	headers := http.Header{}
	if s.Key != "" {
		headers.Set("X-API-Key", s.Key)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, response, err := dialer.DialContext(ctx, s.URL, headers)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil && response.StatusCode >= http.StatusBadRequest {
			return transport.NewStatusError(response.StatusCode, response.Header.Get("Retry-After"))
		}
		return transport.Failure(ctx, "connect LI.FI WebSocket", err)
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(1 << 20)
	queue := make(chan Envelope, 32)
	history := make(chan Envelope)
	accepted := make(chan struct{})
	failure := make(chan error, 1)
	readerDone := make(chan struct{})
	snapshotDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
			var message notification
			if err := conn.ReadJSON(&message); err != nil {
				if ctx.Err() == nil {
					failure <- errors.New("LI.FI WebSocket read failed")
					cancel()
				}
				return
			}
			switch message.Event {
			case pingEvent:
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteJSON(notification{Event: pongEvent}); err != nil {
					failure <- errors.New("LI.FI WebSocket pong failed")
					cancel()
					return
				}
			case submitEvent:
				var envelope Envelope
				if err := json.Unmarshal(message.Data, &envelope); err != nil {
					s.warn("LI.FI notification rejected", zap.Error(err))
					continue
				}
				select {
				case queue <- envelope:
				default:
					// Disconnect and reconcile rather than silently drop or starve heartbeat handling.
					failure <- errors.New("LI.FI ingress capacity exceeded")
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer close(snapshotDone)
		if s.API != nil {
			s.snapshot(ctx, history, accepted)
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		cancel()
		stop()
		_ = conn.Close()
		<-readerDone
		<-snapshotDone
	}()
	accept := func(envelope Envelope) error {
		candidate, err := s.Resolve(ctx, envelope)
		if errors.Is(err, intent.ErrRejected) {
			return nil
		}
		if err != nil {
			return err
		}
		return emit(ctx, candidate)
	}
	for {
		select {
		case <-ctx.Done():
			select {
			case err := <-failure:
				return err
			default:
				return ctx.Err()
			}
		case envelope := <-queue:
			if err := accept(envelope); err != nil {
				return err
			}
		case envelope := <-history:
			if err := accept(envelope); err != nil {
				return err
			}
			select {
			case accepted <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (s *Stream) warn(message string, fields ...zap.Field) {
	if s.Log != nil {
		s.Log.Warn(message, fields...)
	}
}
