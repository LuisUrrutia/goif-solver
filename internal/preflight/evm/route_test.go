package evmpreflight_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	evmpreflight "github.com/LuisUrrutia/goif-solver/internal/preflight/evm"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type verificationBackend struct {
	settlement.Backend
	result  error
	started chan context.Context
	release chan struct{}
	calls   atomic.Int32
}

func (b *verificationBackend) Verify(ctx context.Context) error {
	b.calls.Add(1)
	b.started <- ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		return b.result
	}
}

func newRouteVerifier(t *testing.T, backend settlement.Backend) (*evmpreflight.RouteVerifier, protocol.Route) {
	t.Helper()
	root, err := config.Load("../../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := config.Decode[protocol.Deployment](root.Executions[protocol.IntentKind])
	if err != nil {
		t.Fatal(err)
	}
	route := deployment.Routes[0]
	raw, err := os.ReadFile("../../../internal/app/testdata/escrow-runtimes.json")
	if err != nil {
		t.Fatal(err)
	}
	var code map[string]string
	if err := json.Unmarshal(raw, &code); err != nil {
		t.Fatal(err)
	}

	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		defer func() { _ = r.Body.Close() }()
		var request struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		var result string
		switch request.Method {
		case "eth_getCode":
			var address string
			if err := json.Unmarshal(request.Params[0], &address); err != nil {
				return nil, err
			}
			switch {
			case strings.EqualFold(address, route.InputSettler.Hex()):
				result = code["input"]
			case strings.EqualFold(address, route.OutputSettler.Hex()):
				result = code["output"]
			default:
				result = "0x6000"
			}
		case "eth_call":
			result = fmt.Sprintf("0x%064x", 6)
		default:
			return nil, fmt.Errorf("unexpected method %q", request.Method)
		}
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})
	connection, err := rpc.DialOptions(t.Context(), "http://127.0.0.1", rpc.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	client := ethclient.NewClient(connection)
	t.Cleanup(client.Close)
	return &evmpreflight.RouteVerifier{
		Clients:     map[uint64]*ethclient.Client{route.OriginChain: client, route.DestinationChain: client},
		Settlements: map[string]settlement.Backend{route.Name: backend},
	}, route
}

func TestVerifierKeepsIndependentCallersAfterLeaderCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline-%t", deadline), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &verificationBackend{started: make(chan context.Context, 4), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(backend.release) })
				defer release()
				verifier, route := newRouteVerifier(t, backend)
				first, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				firstResult := make(chan error, 1)
				results := make(chan error, 3)
				go func() { firstResult <- verifier.Verify(first, route) }()
				<-backend.started
				for range cap(results) {
					go func() { results <- verifier.Verify(t.Context(), route) }()
				}
				synctest.Wait()

				want := context.Canceled
				if deadline {
					want = context.DeadlineExceeded
					time.Sleep(time.Second)
				} else {
					cancel()
				}
				synctest.Wait()

				if err := <-firstResult; !errors.Is(err, want) {
					t.Fatalf("leader: %v, want %v", err, want)
				}
				if backend.calls.Load() != 2 {
					t.Fatalf("live callers did not share a retry: %d attempts", backend.calls.Load())
				}
				if ctx := <-backend.started; ctx.Err() != nil {
					t.Fatalf("retry inherited canceled context: %v", ctx.Err())
				}
				release()
				for range cap(results) {
					if err := <-results; err != nil {
						t.Fatalf("independent caller failed: %v", err)
					}
				}
				if err := verifier.Verify(t.Context(), route); err != nil || backend.calls.Load() != 2 {
					t.Fatalf("successful verification not cached: %v", err)
				}
				if err := verifier.Verify(first, route); !errors.Is(err, want) {
					t.Fatalf("cached verification ignored caller cancellation: %v", err)
				}
				time.Sleep(time.Minute)
				if err := verifier.Verify(t.Context(), route); err != nil || backend.calls.Load() != 3 {
					t.Fatalf("expired verification not refreshed: %v", err)
				}
			})
		})
	}
}

func TestVerifierWaiterCancellationDoesNotStopLeader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &verificationBackend{started: make(chan context.Context, 1), release: make(chan struct{})}
		release := sync.OnceFunc(func() { close(backend.release) })
		defer release()
		verifier, route := newRouteVerifier(t, backend)
		leader := make(chan error, 1)
		go func() { leader <- verifier.Verify(t.Context(), route) }()
		verification := <-backend.started
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		waiter := make(chan error, 1)
		go func() { waiter <- verifier.Verify(ctx, route) }()
		synctest.Wait()

		cancel()
		synctest.Wait()

		if err := <-waiter; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter ignored cancellation: %v", err)
		}
		if verification.Err() != nil {
			t.Fatalf("waiter canceled shared work: %v", verification.Err())
		}
		release()
		if err := <-leader; err != nil || backend.calls.Load() != 1 {
			t.Fatalf("leader failed or repeated verification: %v", err)
		}
	})
}

func TestVerifierSharesBackendFailuresWithoutCachingOrRetrying(t *testing.T) {
	for _, failure := range []error{errors.New("invalid runtime"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &verificationBackend{result: failure, started: make(chan context.Context, 2), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(backend.release) })
				defer release()
				verifier, route := newRouteVerifier(t, backend)
				results := make(chan error, 3)
				for range cap(results) {
					go func() { results <- verifier.Verify(t.Context(), route) }()
				}
				synctest.Wait()

				release()

				for range cap(results) {
					if err := <-results; !errors.Is(err, failure) {
						t.Fatalf("backend failure changed: %v", err)
					}
				}
				if backend.calls.Load() != 1 {
					t.Fatalf("failure repeated within a live call: %d attempts", backend.calls.Load())
				}
				if err := verifier.Verify(t.Context(), route); !errors.Is(err, failure) || backend.calls.Load() != 2 {
					t.Fatalf("failure was cached: %v", err)
				}
			})
		})
	}
}
