package solver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestMultipleSourcesCanonicalizeDuplicateOrders(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	store, err := coordination.New(client, fmt.Sprintf("sources-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Order.FillDeadline = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	envelope.Order.Expires = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	envelope.Order.Outputs[0].Context = "0x"
	service := Service{Engine: &Engine{Config: c, Store: store}, Log: zap.NewNop()}
	for i := 0; i < 2; i++ {
		copy := envelope
		if i == 1 {
			copy.Order.User = "0x" + strings.ToUpper(copy.Order.User[2:])
			copy.Meta.Status = "Open"
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(struct {
				Data []lifi.Envelope `json:"data"`
				Meta map[string]int  `json:"meta"`
			}{[]lifi.Envelope{copy}, map[string]int{"total": 1, "limit": 50, "offset": 0}})
		}))
		defer server.Close()
		source, err := lifi.New(server.URL, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		service.Sources = append(service.Sources, source)
	}
	if err = service.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if service.Discovered.Load() != 1 {
		t.Fatalf("discovered %d times", service.Discovered.Load())
	}
	ids, err := store.Ready(t.Context(), 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("queue %v %v", ids, err)
	}
}
