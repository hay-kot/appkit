package plugs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// runWithRetry executes p.Start up to opts.maxAttempts total times. Returns
// nil if ctx is cancelled (clean shutdown) or if any attempt succeeds.
// Returns a wrapped error after the final failed attempt.
func runWithRetry(ctx context.Context, p Plugin, opts *managerOpts) error {
	runCount := max(1, opts.maxAttempts)
	var lastErr error

	for attempt := range runCount {
		err := safeStart(ctx, p)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) {
			return nil
		}

		lastErr = err
		if attempt == runCount-1 {
			break
		}

		opts.log.Warn("plugs: plugin failed, retrying",
			"plugin", p.Name(),
			"attempt", attempt+1,
			"remaining", runCount-attempt-1,
			"err", err,
		)

		if !sleep(ctx, opts.backoff(attempt)) {
			return nil
		}
	}

	var panicErr *PanicError
	if errors.As(lastErr, &panicErr) {
		return lastErr
	}
	return fmt.Errorf("plugin %q: %w", p.Name(), lastErr)
}

// safeStart invokes p.Start with a recover so a panic becomes a PanicError.
func safeStart(ctx context.Context, p Plugin) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Name: p.Name(), Value: r}
		}
	}()
	return p.Start(ctx)
}

// sleep waits for d or ctx cancellation. Returns false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
