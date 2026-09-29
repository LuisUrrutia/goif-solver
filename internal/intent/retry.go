package intent

import (
	"errors"
	"time"
)

// RetryHint is implemented by adapters with an upstream retry deadline.
type RetryHint interface {
	RetryDelay() time.Duration
}

func RetryDelay(err error) time.Duration {
	var hint RetryHint
	if errors.As(err, &hint) {
		return max(hint.RetryDelay(), 0)
	}
	return 0
}
