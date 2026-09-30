package escrow

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
)

func BenchmarkPrepareRoutes(b *testing.B) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		b.Fatal(err)
	}
	deployment, err := config.Decode[protocol.Deployment](c.Executions[protocol.IntentKind])
	if err != nil {
		b.Fatal(err)
	}
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		b.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		b.Fatal(err)
	}
	envelope.Order.FillDeadline = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	envelope.Order.Expires = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	envelope.Order.Outputs[0].Context = "0x"
	payload, err := json.Marshal(envelope.Intent())
	if err != nil {
		b.Fatal(err)
	}
	candidate := intent.Candidate{Kind: protocol.IntentKind, ID: envelope.Meta.ID, Payload: payload}
	for _, routes := range []int{1, 8, 32, 64} {
		for _, position := range []string{"first", "last", "miss"} {
			b.Run(fmt.Sprintf("routes_%d/%s", routes, position), func(b *testing.B) {
				policy := deployment
				policy.Routes = make([]protocol.Route, routes)
				for i := range routes {
					policy.Routes[i] = deployment.Routes[0]
					policy.Routes[i].Name = fmt.Sprintf("route-%d", i)
					policy.Routes[i].OriginChain += uint64(i + 1)
				}
				if position == "first" {
					policy.Routes[0] = deployment.Routes[0]
				}
				if position == "last" {
					policy.Routes[routes-1] = deployment.Routes[0]
				}
				engine := Engine{Config: Policy{Deployment: policy, Version: c.Version}}
				b.ReportAllocs()
				for b.Loop() {
					_, err := engine.Prepare(candidate)
					if position != "miss" && err != nil {
						b.Fatal(err)
					}
					if position == "miss" && err == nil {
						b.Fatal("accepted unmatched route")
					}
				}
			})
		}
	}
}
