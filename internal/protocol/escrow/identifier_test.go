package escrow

import "testing"

func TestIdentifierMatchesHistoricalOriginAndFill(t *testing.T) {
	envelope, route, _ := pilot(t)
	order, err := Parse(envelope.Order)
	if err != nil {
		t.Fatal(err)
	}

	id, err := Identifier(order, route.InputSettler)

	if err != nil || id.Hex() != envelope.ID {
		t.Fatalf("identifier = %s, want %s: %v", id.Hex(), envelope.ID, err)
	}
}
