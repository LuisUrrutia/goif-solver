package escrow

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
)

func TestCanonicalIntentIgnoresLIFlMetadataAndAddressCase(t *testing.T) {
	c, err := loadTestPolicy("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Order.FillDeadline = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	envelope.Order.Expires = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	envelope.Order.Outputs[0].Context = "0x"
	engine := Engine{Config: c}
	prepare := func(e lifi.Envelope) intent.Candidate {
		payload, err := json.Marshal(e.Intent())
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := engine.Prepare(intent.Candidate{ID: e.Meta.ID, Kind: escrowprotocol.IntentKind, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}
	first := prepare(envelope)
	envelope.Meta.Status = "Open"
	envelope.Meta.FillTx = "enriched metadata"
	envelope.Order.User = "0x" + strings.ToUpper(envelope.Order.User[2:])
	second := prepare(envelope)
	if first.ID != second.ID || string(first.Payload) != string(second.Payload) {
		t.Fatal("equivalent sources conflict")
	}
}
