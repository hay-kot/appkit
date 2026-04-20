package concurrency_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hay-kot/appkit/concurrency"
)

func TestPool_SubmitRunsJobs(t *testing.T) {
	p := concurrency.NewPool(4, 16)
	defer p.Close()

	var count atomic.Int32
	var done sync.WaitGroup
	const n = 100

	done.Add(n)
	for range n {
		err := p.Submit(context.Background(), func(_ context.Context) {
			count.Add(1)
			done.Done()
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	done.Wait()
	if got := count.Load(); got != n {
		t.Errorf("want %d, got %d", n, got)
	}
}

func TestPool_SubmitBlocksWhenQueueFull(t *testing.T) {
	p := concurrency.NewPool(1, 1)
	defer p.Close()

	// Block the single worker indefinitely.
	release := make(chan struct{})
	if err := p.Submit(context.Background(), func(_ context.Context) { <-release }); err != nil {
		t.Fatal(err)
	}
	// Fill the queue (capacity 1).
	if err := p.Submit(context.Background(), func(_ context.Context) {}); err != nil {
		t.Fatal(err)
	}

	// Third Submit should block until we unblock the worker or cancel.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := p.Submit(ctx, func(_ context.Context) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want DeadlineExceeded, got %v", err)
	}

	close(release)
}

func TestPool_TrySubmitReturnsFalseWhenFull(t *testing.T) {
	p := concurrency.NewPool(1, 1)
	defer p.Close()

	release := make(chan struct{})
	if err := p.Submit(context.Background(), func(_ context.Context) { <-release }); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(context.Background(), func(_ context.Context) {}); err != nil {
		t.Fatal(err)
	}

	if p.TrySubmit(func(_ context.Context) {}) {
		t.Error("TrySubmit should return false when queue is full")
	}
	close(release)
}

func TestPool_TrySubmitSucceedsWhenSpace(t *testing.T) {
	p := concurrency.NewPool(2, 4)
	defer p.Close()

	if !p.TrySubmit(func(_ context.Context) {}) {
		t.Error("TrySubmit should succeed when queue has space")
	}
}

func TestPool_CloseWaitsForQueuedJobs(t *testing.T) {
	p := concurrency.NewPool(2, 8)

	var completed atomic.Int32
	const n = 10
	for range n {
		err := p.Submit(context.Background(), func(_ context.Context) {
			time.Sleep(5 * time.Millisecond)
			completed.Add(1)
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	p.Close()

	if got := completed.Load(); got != n {
		t.Errorf("want %d completed jobs after Close, got %d", n, got)
	}
}

func TestPool_SubmitAfterCloseReturnsErr(t *testing.T) {
	p := concurrency.NewPool(2, 4)
	p.Close()

	err := p.Submit(context.Background(), func(_ context.Context) {})
	if !errors.Is(err, concurrency.ErrPoolClosed) {
		t.Errorf("want ErrPoolClosed, got %v", err)
	}
}

func TestPool_TrySubmitAfterCloseReturnsFalse(t *testing.T) {
	p := concurrency.NewPool(2, 4)
	p.Close()

	if p.TrySubmit(func(_ context.Context) {}) {
		t.Error("TrySubmit should return false after Close")
	}
}

func TestPool_CloseIsIdempotent(t *testing.T) {
	p := concurrency.NewPool(2, 4)
	p.Close()
	p.Close() // must not panic or deadlock
	p.Close()
}

// A panicking job must not kill its worker; subsequent jobs should still run.
func TestPool_PanicRecoveryKeepsWorkerAlive(t *testing.T) {
	p := concurrency.NewPool(1, 4)
	defer p.Close()

	bad := make(chan struct{})
	good := make(chan struct{})

	if err := p.Submit(context.Background(), func(_ context.Context) {
		close(bad)
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	<-bad

	if err := p.Submit(context.Background(), func(_ context.Context) {
		close(good)
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-good:
	case <-time.After(time.Second):
		t.Fatal("subsequent job did not run; worker likely died")
	}
}

// Jobs receive the pool's ctx so they can observe shutdown and exit early.
func TestPool_JobContextCancelsOnClose(t *testing.T) {
	p := concurrency.NewPool(1, 1)

	observed := make(chan struct{})
	if err := p.Submit(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		close(observed)
	}); err != nil {
		t.Fatal(err)
	}

	// Give the worker a moment to pick up the job.
	time.Sleep(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()

	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("job did not observe ctx cancellation")
	}
	<-done
}

// Close must unblock Submit callers that are parked on a full queue so
// shutdown completes even when workers are stuck on long-running jobs.
func TestPool_CloseUnblocksPendingSubmit(t *testing.T) {
	p := concurrency.NewPool(1, 1)

	release := make(chan struct{})
	if err := p.Submit(context.Background(), func(_ context.Context) { <-release }); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(context.Background(), func(_ context.Context) {}); err != nil {
		t.Fatal(err)
	}

	submitDone := make(chan error, 1)
	go func() {
		submitDone <- p.Submit(context.Background(), func(_ context.Context) {})
	}()

	// Let the third Submit reach its select before we close.
	time.Sleep(20 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		p.Close()
		close(closeDone)
	}()

	select {
	case err := <-submitDone:
		if !errors.Is(err, concurrency.ErrPoolClosed) {
			t.Errorf("want ErrPoolClosed, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit did not return after Close")
	}

	close(release)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return")
	}
}

// A registered panic handler must receive the recovered value so operators
// can observe crashes in long-running pools.
func TestPool_PanicHandlerReceivesValue(t *testing.T) {
	got := make(chan any, 1)
	p := concurrency.NewPool(1, 1, concurrency.WithPanicHandler(func(r any) {
		got <- r
	}))
	defer p.Close()

	if err := p.Submit(context.Background(), func(_ context.Context) {
		panic("bang")
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case v := <-got:
		if fmt.Sprint(v) != "bang" {
			t.Errorf("want bang, got %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("panic handler not invoked")
	}
}

func TestNewPool_PanicsOnInvalidArgs(t *testing.T) {
	cases := []struct {
		name             string
		workers, queueSz int
	}{
		{"zero workers", 0, 1},
		{"negative workers", -1, 1},
		{"zero queue", 1, 0},
		{"negative queue", 1, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for %s", c.name)
				}
			}()
			concurrency.NewPool(c.workers, c.queueSz)
		})
	}
}
