package lifi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/gorilla/websocket"
)

type Stream struct {
	API     *Client
	Resolve func(context.Context, Envelope) (intent.Candidate, error)
	URL     string
	Key     string
	Filters []url.Values
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
		return errors.New("LI.FI WebSocket connection failed")
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(1 << 20)
	queue := make(chan Envelope, 32)
	failure := make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
			var message notification
			if err := conn.ReadJSON(&message); err != nil {
				failure <- errors.New("LI.FI WebSocket read failed")
				cancel()
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
				if json.Unmarshal(message.Data, &envelope) != nil {
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
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() { stop(); _ = conn.Close(); <-readerDone }()
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
	// Subscribe before reconciling the REST snapshot to close the connect-time gap.
	// The server has no durable replay cursor; its bounded REST window is not lossless.
	if s.API != nil {
		for _, filter := range s.Filters {
			for offset := 0; offset <= 1000; offset += 50 {
				page, err := s.API.Orders(ctx, filter, offset)
				if err != nil {
					return err
				}
				for _, envelope := range page.Data {
					if err := accept(envelope); err != nil {
						return err
					}
				}
				if len(page.Data) < 50 || offset+50 >= page.Meta.Total {
					break
				}
				if offset == 1000 {
					return errors.New("LI.FI reconnect snapshot exceeds API window")
				}
			}
		}
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
		}
	}
}
