package webhook

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Poller periodically fires registered Trigger keys, so incremental ingestion
// keeps running even if a provider's webhooks are unset up, misconfigured, or
// dropped. It reuses Trigger's debounce/coalescing, so a poll tick racing a
// webhook-triggered run just marks the run dirty instead of running twice.
type Poller struct {
	trigger *Trigger
	lg      *zap.Logger

	wg sync.WaitGroup
}

// NewPoller creates a Poller that fires keys on trigger.
func NewPoller(trigger *Trigger, lg *zap.Logger) *Poller {
	if lg == nil {
		lg = zap.NewNop()
	}
	return &Poller{trigger: trigger, lg: lg}
}

// Start launches a background ticker that fires key every interval, until ctx
// is done. It fires once immediately so ingestion runs at startup instead of
// waiting a full interval. Interval <= 0 disables polling for this key.
func (p *Poller) Start(ctx context.Context, key string, interval time.Duration) {
	if interval <= 0 {
		return
	}

	lg := p.lg.With(zap.String("poll", key), zap.Duration("interval", interval))
	p.wg.Go(func() {
		lg.Info("polling enabled")
		p.trigger.metrics.recordPollTick(ctx, key)
		p.trigger.Fire(key)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lg.Debug("poll tick")
				p.trigger.metrics.recordPollTick(ctx, key)
				p.trigger.Fire(key)
			}
		}
	})
}

// Wait blocks until all polling goroutines have returned. Call after ctx is
// canceled during shutdown.
func (p *Poller) Wait() {
	p.wg.Wait()
}
