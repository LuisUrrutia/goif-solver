package coordination

import (
	"context"
	"errors"
	"time"
)

type Leases interface {
	Acquire(context.Context, string, time.Duration) (Lease, error)
	Renew(context.Context, Lease, time.Duration) error
	Release(context.Context, Lease) error
}

// RunOwned retains ownership across reconnects and idle periods. Lease loss
// cancels the operation before cleanup; a replacement owner's lease is untouched.
func RunOwned(ctx context.Context, store Leases, resource string, ttl time.Duration, run func(context.Context, Lease) error) error {
	lease, err := store.Acquire(ctx, resource, ttl)
	if err != nil {
		return err
	}
	owned, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-owned.Done():
				return
			case <-ticker.C:
				attempt, stop := context.WithTimeout(owned, min(ttl/3, 5*time.Second))
				err := store.Renew(attempt, lease, ttl)
				stop()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	err = run(owned, lease)
	cause := context.Cause(owned)
	cancel(context.Canceled)
	<-done
	release, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_ = store.Release(release, lease)
	return errors.Join(err, cause)
}
