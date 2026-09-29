package lifi

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
)

func TestEnvelopeDecodesHistoricalNumericFields(t *testing.T) {
	// Captured Signed record from the public development API, offset 250.
	raw, err := os.ReadFile("testdata/legacy-numeric-envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope

	err = json.Unmarshal(raw, &envelope)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Order.Expires != "1755602241" || envelope.Order.FillDeadline != "1755602241" || envelope.Order.Nonce != "745802110" || envelope.Order.Outputs[0].ChainID != "84532" || envelope.Order.Inputs[0][0] != "36480414457181834686435859733604505384509859462345317876249814483837321507384" {
		t.Fatalf("historical values changed: %+v", envelope.Order)
	}
}

func TestEnvelopeIntegersRemainExact(t *testing.T) {
	maximum := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	for _, value := range []string{"0", "9007199254740993", maximum} {
		for _, encoded := range []string{value, `"` + value + `"`} {
			t.Run(encoded, func(t *testing.T) {
				var envelope Envelope
				err := json.Unmarshal([]byte(`{"order":{"nonce":`+encoded+`,"originChainId":84532,"expires":1755602241,"fillDeadline":"1755602241","inputs":[[123,`+encoded+`]],"outputs":[{"amount":`+encoded+`,"chainId":11155111}]}}`), &envelope)
				if err != nil {
					t.Fatal(err)
				}
				order := envelope.Order
				if order.Nonce != value || order.Inputs[0][1] != value || order.Outputs[0].Amount != value || order.OriginChainID != "84532" || order.Outputs[0].ChainID != "11155111" {
					t.Fatalf("integer precision lost: %+v", order)
				}
			})
		}
	}
}

func TestEnvelopeRejectsInvalidIntegers(t *testing.T) {
	overflow := new(big.Int).Lsh(big.NewInt(1), 256).String()
	for _, encoded := range []string{"null", "true", "-1", "1.5", "1e3", `"-1"`, `"1.5"`, `"1e3"`, `"01"`, `""`, `"+1"`, `" 1"`, overflow, `"` + overflow + `"`} {
		t.Run(encoded, func(t *testing.T) {
			var envelope Envelope

			err := json.Unmarshal([]byte(`{"order":{"nonce":`+encoded+`}}`), &envelope)

			if err == nil {
				t.Fatalf("accepted invalid integer %s", encoded)
			}
		})
	}
}
