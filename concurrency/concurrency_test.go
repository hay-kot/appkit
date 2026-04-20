package concurrency_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hay-kot/appkit/concurrency"
)

func TestForEach_RunsAllJobs(t *testing.T) {
	var seen sync.Map
	err := concurrency.ForEach(context.Background(), 50, 5, func(_ context.Context, idx int) error {
		seen.Store(idx, true)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, ok := seen.Load(i); !ok {
			t.Errorf("job %d did not run", i)
		}
	}
}

func TestForEach_ZeroJobsReturnsNil(t *testing.T) {
	called := false
	err := concurrency.ForEach(context.Background(), 0, 4, func(_ context.Context, _ int) error {
		called = true
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	if called {
		t.Error("fn should not be called for n=0")
	}
}

func TestForEach_ReturnsFirstError(t *testing.T) {
	sentinel := errors.New("fail")
	err := concurrency.ForEach(context.Background(), 100, 4, func(_ context.Context, idx int) error {
		if idx == 7 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel, got %v", err)
	}
}

// All workers must observe cancellation once one returns an error. Uses a
// barrier so every worker is definitely inside fn before the sentinel fires —
// otherwise a worker can exit the outer loop at `ctx.Err() == nil` before it
// ever calls fn, which is correct behavior but not what this test asserts.
func TestForEach_ErrorCancelsInflightWorkers(t *testing.T) {
	sentinel := errors.New("fail")
	const workers = 4

	var reached atomic.Int32
	barrier := make(chan struct{})
	var cancelled atomic.Int32

	err := concurrency.ForEach(context.Background(), workers, workers, func(ctx context.Context, idx int) error {
		if reached.Add(1) == workers {
			close(barrier)
		} else {
			<-barrier
		}

		if idx == 0 {
			return sentinel
		}
		select {
		case <-ctx.Done():
			cancelled.Add(1)
			return ctx.Err()
		case <-time.After(time.Second):
			return nil
		}
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel, got %v", err)
	}
	if got := cancelled.Load(); got != workers-1 {
		t.Errorf("expected %d workers to observe cancellation, got %d", workers-1, got)
	}
}

func TestForEach_ParentContextCancellationReturnsCause(t *testing.T) {
	cause := errors.New("parent gave up")
	ctx, cancel := context.WithCancelCause(context.Background())

	var once sync.Once
	start := make(chan struct{})
	err := make(chan error, 1)
	go func() {
		err <- concurrency.ForEach(ctx, 10, 2, func(ctx context.Context, _ int) error {
			once.Do(func() { close(start) })
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	<-start
	cancel(cause)

	select {
	case got := <-err:
		if !errors.Is(got, cause) {
			t.Errorf("want %v, got %v", cause, got)
		}
	case <-time.After(time.Second):
		t.Fatal("ForEach did not return after parent cancellation")
	}
}

func TestForEach_ConcurrencyCapIsObserved(t *testing.T) {
	var (
		inflight atomic.Int32
		peak     atomic.Int32
	)
	const cap_ = 3
	err := concurrency.ForEach(context.Background(), 30, cap_, func(_ context.Context, _ int) error {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > int32(cap_) {
		t.Errorf("exceeded concurrency cap: peak=%d cap=%d", got, cap_)
	}
}

func TestForEach_ConcurrencyGreaterThanJobs(t *testing.T) {
	var count atomic.Int32
	err := concurrency.ForEach(context.Background(), 3, 100, func(_ context.Context, _ int) error {
		count.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 3 {
		t.Errorf("want 3, got %d", count.Load())
	}
}

func TestForEachMergeResults_ConcatenatesResults(t *testing.T) {
	jobs := []int{1, 2, 3}
	got, err := concurrency.ForEachMergeResults(
		context.Background(), jobs, 2,
		func(_ context.Context, j int) ([]int, error) {
			return []int{j, j * 10}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(got)
	want := []int{1, 2, 3, 10, 20, 30}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestForEachMergeResults_ErrorDiscardsPartials(t *testing.T) {
	sentinel := errors.New("boom")
	got, err := concurrency.ForEachMergeResults(
		context.Background(), []int{1, 2, 3, 4}, 2,
		func(_ context.Context, j int) ([]int, error) {
			if j == 3 {
				return nil, sentinel
			}
			return []int{j}, nil
		},
	)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel, got %v", err)
	}
	if got != nil {
		t.Errorf("want nil results on error, got %v", got)
	}
}

func TestForEachMergeResults_EmptyJobs(t *testing.T) {
	got, err := concurrency.ForEachMergeResults(
		context.Background(), []int(nil), 4,
		func(_ context.Context, _ int) ([]int, error) { return nil, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}
