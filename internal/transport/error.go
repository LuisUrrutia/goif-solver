package transport

import (
	"context"
	"errors"
	"fmt"
)

var ErrUnavailable = errors.New("remote operation failed")

// Failure preserves safe error classifications without retaining upstream details.
func Failure(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	cause := ErrUnavailable
	switch {
	case errors.Is(err, context.Canceled):
		cause = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		cause = context.DeadlineExceeded
	}
	return fmt.Errorf("%s: %w", operation, cause)
}
