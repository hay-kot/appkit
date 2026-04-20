// Package concurrency provides small helpers for running bounded-parallel work
// with first-error-wins semantics. All helpers propagate cancellation to
// in-flight workers via the ctx argument.
package concurrency

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ForEach runs fn for each index in [0, n) using at most concurrency
// workers. If concurrency <= 0, it runs fully in parallel (one goroutine per
// job).
//
// Semantics:
//   - On the first error returned by fn, the context passed to every worker is
//     cancelled (via context.WithCancelCause) and ForEach returns that error
//     once all workers observe cancellation.
//   - If the parent ctx is cancelled before completion, ForEach returns the
//     cause of that cancellation.
//   - Otherwise, returns nil when every job has completed without error.
func ForEach(parentCtx context.Context, n int, concurrency int, fn func(ctx context.Context, idx int) error) error {
	if n <= 0 {
		return nil
	}
	if concurrency <= 0 || concurrency > n {
		concurrency = n
	}

	ctx, cancel := context.WithCancelCause(parentCtx)
	defer cancel(nil)

	var (
		idx      atomic.Int64
		firstErr atomic.Pointer[error]
		wg       sync.WaitGroup
	)
	idx.Store(-1)

	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(idx.Add(1))
				if i >= n {
					return
				}
				if err := fn(ctx, i); err != nil {
					// Workers that return ctx.Err() after cancellation are
					// observing shutdown, not producing a real failure.
					// Surface only genuine errors in firstErr.
					if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
						return
					}
					if firstErr.CompareAndSwap(nil, &err) {
						cancel(err)
					}
					return
				}
			}
		}()
	}
	wg.Wait()

	if p := firstErr.Load(); p != nil {
		return *p
	}
	// Parent cancellation surfaces with its cause preserved; the child ctx
	// created above does not propagate cause back upward via context.Cause,
	// so we read from the parent directly.
	if parentCtx.Err() != nil {
		return context.Cause(parentCtx)
	}
	return nil
}

// ForEachMergeResults is like [ForEach] but each fn returns a slice of
// results, all of which are concatenated into the returned slice. On error,
// the partial results are discarded and the error is returned.
func ForEachMergeResults[J any, R any](
	ctx context.Context,
	jobs []J,
	concurrency int,
	fn func(ctx context.Context, job J) ([]R, error),
) ([]R, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	var (
		mu      sync.Mutex
		results = make([]R, 0, len(jobs))
	)

	err := ForEach(ctx, len(jobs), concurrency, func(ctx context.Context, idx int) error {
		r, err := fn(ctx, jobs[idx])
		if err != nil {
			return err
		}
		mu.Lock()
		results = append(results, r...)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
