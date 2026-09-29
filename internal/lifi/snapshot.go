package lifi

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"go.uber.org/zap"
)

// Subscription precedes catch-up. Only the Run loop resolves and durably accepts
// candidates, so slow REST requests cannot block live intake or race its consumer.
func (s *Stream) snapshot(ctx context.Context, history chan<- Envelope, accepted <-chan struct{}) {
	backoff := time.Second
	for ctx.Err() == nil {
		incomplete, retryAfter := s.snapshotPass(ctx, history, accepted)
		if ctx.Err() != nil || !incomplete {
			if ctx.Err() == nil && s.Log != nil {
				s.Log.Info("LI.FI snapshot complete")
			}
			return
		}
		delay := max(backoff, retryAfter) + time.Duration(rand.Int64N(int64(backoff/4))) // #nosec G404 -- retry jitter is not security-sensitive.
		s.warn("LI.FI snapshot incomplete; retrying with live intake active", zap.Duration("retry_after", delay))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Stream) snapshotPass(ctx context.Context, history chan<- Envelope, accepted <-chan struct{}) (bool, time.Duration) {
	incomplete := false
	for index, filter := range s.Filters {
		for offset := 0; offset <= maxOrderOffset; offset += orderPageSize {
			page, err := s.API.Orders(ctx, filter, offset)
			if ctx.Err() != nil {
				return true, 0
			}
			if err != nil {
				s.warn("LI.FI snapshot page unavailable", zap.Int("filter_index", index), zap.Int("offset", offset), zap.Error(err))
				incomplete = true
				if delay := intent.RetryDelay(err); delay > 0 {
					return true, delay
				}
				break
			}
			rejected := 0
			for _, raw := range page.Data {
				var envelope Envelope
				if json.Unmarshal(raw, &envelope) != nil {
					rejected++
					continue
				}
				select {
				case <-ctx.Done():
					return true, 0
				case history <- envelope:
				}
				select {
				case <-ctx.Done():
					return true, 0
				case <-accepted:
				}
			}
			if rejected > 0 {
				incomplete = true
				s.warn("LI.FI snapshot records rejected", zap.Int("filter_index", index), zap.Int("offset", offset), zap.Int("rejected", rejected))
			}
			if len(page.Data) < orderPageSize || offset+orderPageSize >= page.Meta.Total {
				break
			}
			if offset == maxOrderOffset {
				incomplete = true
				s.warn("LI.FI snapshot exceeds API window", zap.Int("filter_index", index), zap.Int("total", page.Meta.Total))
			}
		}
	}
	return incomplete, 0
}
