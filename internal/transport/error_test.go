package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFailurePreservesOnlySafeCauses(t *testing.T) {
	for _, test := range []struct{ cause, want error }{
		{context.Canceled, context.Canceled},
		{context.DeadlineExceeded, context.DeadlineExceeded},
		{errors.New("provider failure"), ErrUnavailable},
	} {
		t.Run(test.cause.Error(), func(t *testing.T) {
			upstream := fmt.Errorf("https://rpc.invalid/private-api-key: %w", test.cause)

			err := Failure(t.Context(), "query balance", upstream)

			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private-api-key") || errors.Is(err, upstream) {
				t.Fatalf("unsafe or unclassified error: %v", err)
			}
		})
	}
}

func TestFailureRetainsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := Failure(ctx, "read response", errors.New("connection closed"))

	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
