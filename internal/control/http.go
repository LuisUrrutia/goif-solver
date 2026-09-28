// Package control exposes bounded health, metrics, and authenticated fleet controls.
package control

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
)

func Handler(s *solver.Service, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK); w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Engine.Store.Ping(r.Context()); err != nil {
			http.Error(w, "Redis unavailable", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "goif_orders_discovered_total %d\ngoif_order_steps_total %d\ngoif_cycle_failures_total %d\n", s.Discovered.Load(), s.Advanced.Load(), s.Failures.Load())
	})
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || token == "" || len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /control", auth(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Engine.Store.Control(r.Context())
		if err != nil {
			http.Error(w, "control unavailable", 503)
			return
		}
		write(w, c)
	}))
	mux.HandleFunc("PUT /control", auth(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Expected uint64               `json:"expected_version"`
			Control  coordination.Control `json:"control"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil {
			http.Error(w, "invalid control JSON", 400)
			return
		}
		var extra interface{}
		if d.Decode(&extra) != io.EOF {
			http.Error(w, "trailing control data", 400)
			return
		}
		if err := s.Engine.Store.SetControl(r.Context(), request.Expected, request.Control); err != nil {
			status := 400
			if errors.Is(err, coordination.ErrConflict) {
				status = 409
			}
			http.Error(w, "control update rejected", status)
			return
		}
		write(w, request.Control)
	}))
	mux.HandleFunc("GET /orders/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if len(id) != 66 {
			http.Error(w, "invalid order ID", 400)
			return
		}
		record, err := s.Engine.Store.Record(r.Context(), id)
		if err != nil {
			status := 503
			if errors.Is(err, coordination.ErrNotFound) {
				status = 404
			}
			http.Error(w, "order unavailable", status)
			return
		}
		write(w, record)
	}))
	return http.TimeoutHandler(mux, 10*time.Second, "request timed out")
}
func write(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
