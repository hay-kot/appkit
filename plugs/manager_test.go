package plugs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hay-kot/appkit/plugs"
)

// waitFor polls a condition until it is true or the deadline passes. Useful
// for lifecycle assertions that happen on another goroutine without requiring
// fixed sleeps.
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func newTestManager(t *testing.T, opts ...plugs.Option) *plugs.Manager {
	t.Helper()
	defaults := []plugs.Option{
		plugs.WithSignals(), // disable signals for tests
		plugs.WithTimeout(100 * time.Millisecond),
	}
	return plugs.New(append(defaults, opts...)...)
}

func TestManager_StartsAllPluginsAndReturnsNilOnContextCancel(t *testing.T) {
	mgr := newTestManager(t)

	var startCount atomic.Int32
	for i := range 3 {
		name := fmt.Sprintf("p%d", i)
		mgr.AddFunc(name, func(ctx context.Context) error {
			startCount.Add(1)
			<-ctx.Done()
			return nil
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()

	waitFor(t, time.Second, func() bool { return startCount.Load() == 3 }, "3 plugins to start")
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

func TestManager_ShutdownTriggersStop(t *testing.T) {
	mgr := newTestManager(t)

	ran := make(chan struct{})
	mgr.AddFunc("p", func(ctx context.Context) error {
		close(ran)
		<-ctx.Done()
		return nil
	})

	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(context.Background()) }()

	<-ran
	mgr.Shutdown()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Shutdown")
	}
}

func TestManager_SecondStartReturnsErrAlreadyStarted(t *testing.T) {
	mgr := newTestManager(t)
	ready := make(chan struct{})
	mgr.AddFunc("p", func(ctx context.Context) error {
		close(ready)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()
	<-ready

	err2 := mgr.Start(ctx)
	if !errors.Is(err2, plugs.ErrManagerAlreadyStarted) {
		t.Errorf("want ErrManagerAlreadyStarted, got %v", err2)
	}

	cancel()
	<-errCh
}

// When a plugin's ctx is cancelled and it returns ctx.Err(), the manager
// must NOT treat that as an error — it is a normal shutdown.
func TestManager_ContextCancelFromPluginNotTreatedAsError(t *testing.T) {
	mgr := newTestManager(t)
	mgr.AddFunc("p", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err() // plugins that return ctx.Err() should be fine
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()

	if err := <-errCh; err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestManager_TimeoutWhenPluginDoesNotExit(t *testing.T) {
	mgr := newTestManager(t, plugs.WithTimeout(30*time.Millisecond))

	block := make(chan struct{})
	mgr.AddFunc("stuck", func(ctx context.Context) error {
		<-block // never closed during test
		return nil
	})
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		var pe *plugs.PluginErrors
		if !errors.As(err, &pe) {
			t.Fatalf("want *PluginErrors, got %T: %v", err, err)
		}
		if !errors.Is(pe, context.DeadlineExceeded) {
			t.Errorf("want DeadlineExceeded inside PluginErrors, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return")
	}
}

func TestManager_PluginErrorCausesShutdownAndIsAggregated(t *testing.T) {
	mgr := newTestManager(t)

	mgr.AddFunc("bad", func(ctx context.Context) error {
		return errors.New("explicit failure")
	})

	siblingStopped := make(chan struct{})
	mgr.AddFunc("sibling", func(ctx context.Context) error {
		<-ctx.Done()
		close(siblingStopped)
		return nil
	})

	err := mgr.Start(context.Background())

	var pe *plugs.PluginErrors
	if !errors.As(err, &pe) {
		t.Fatalf("want *PluginErrors, got %T: %v", err, err)
	}
	if len(pe.Errors) != 1 {
		t.Fatalf("want 1 error, got %d: %v", len(pe.Errors), pe.Errors)
	}
	if !strings.Contains(pe.Errors[0].Error(), "explicit failure") {
		t.Errorf("error missing plugin err: %v", pe.Errors[0])
	}
	if !strings.Contains(pe.Errors[0].Error(), "bad") {
		t.Errorf("error missing plugin name: %v", pe.Errors[0])
	}

	select {
	case <-siblingStopped:
	case <-time.After(time.Second):
		t.Fatal("sibling not stopped after peer failure")
	}
}

func TestManager_PluginPanicIsRecoveredAsPanicError(t *testing.T) {
	mgr := newTestManager(t)
	mgr.AddFunc("crash", func(ctx context.Context) error {
		panic("kaboom")
	})

	err := mgr.Start(context.Background())

	var pe *plugs.PluginErrors
	if !errors.As(err, &pe) {
		t.Fatalf("want *PluginErrors, got %T: %v", err, err)
	}
	var panicErr *plugs.PanicError
	if !errors.As(pe, &panicErr) {
		t.Fatalf("want *PanicError within PluginErrors, got %v", err)
	}
	if panicErr.Name != "crash" {
		t.Errorf("want name=crash, got %q", panicErr.Name)
	}
	if fmt.Sprint(panicErr.Value) != "kaboom" {
		t.Errorf("want value=kaboom, got %v", panicErr.Value)
	}
}

func TestManager_RetriesOnFailureThenReturnsFinalError(t *testing.T) {
	mgr := newTestManager(t,
		plugs.WithMaxAttempts(3),
		plugs.WithBackoff(func(int) time.Duration { return 0 }),
	)

	var attempts atomic.Int32
	mgr.AddFunc("flaky", func(ctx context.Context) error {
		attempts.Add(1)
		return errors.New("always fails")
	})

	err := mgr.Start(context.Background())

	if got := attempts.Load(); got != 3 {
		t.Errorf("want 3 attempts, got %d", got)
	}
	var pe *plugs.PluginErrors
	if !errors.As(err, &pe) {
		t.Fatalf("want *PluginErrors, got %T: %v", err, err)
	}
}

func TestManager_RetrySucceedsReturnsNil(t *testing.T) {
	mgr := newTestManager(t,
		plugs.WithMaxAttempts(5),
		plugs.WithBackoff(func(int) time.Duration { return 0 }),
	)

	var attempts atomic.Int32
	mgr.AddFunc("flaky", func(ctx context.Context) error {
		n := attempts.Add(1)
		if n < 3 {
			return errors.New("transient")
		}
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()

	waitFor(t, time.Second, func() bool { return attempts.Load() >= 3 }, "3 attempts")
	cancel()

	if err := <-errCh; err != nil {
		t.Errorf("want nil after successful retry, got %v", err)
	}
}

func TestManager_MultiplePluginFailuresAggregate(t *testing.T) {
	mgr := newTestManager(t)
	mgr.AddFunc("a", func(ctx context.Context) error { return errors.New("a err") })
	mgr.AddFunc("b", func(ctx context.Context) error { return errors.New("b err") })

	err := mgr.Start(context.Background())

	var pe *plugs.PluginErrors
	if !errors.As(err, &pe) {
		t.Fatalf("want *PluginErrors, got %T: %v", err, err)
	}
	if len(pe.Errors) != 2 {
		t.Errorf("want 2 aggregated errors, got %d: %v", len(pe.Errors), pe.Errors)
	}
}

func TestManager_LoggerReceivesShutdownMessage(t *testing.T) {
	log := &recordingLogger{}
	mgr := newTestManager(t, plugs.WithLogger(log))

	mgr.AddFunc("p", func(ctx context.Context) error { <-ctx.Done(); return nil })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-errCh

	if !log.has("info", "shutting down") {
		t.Errorf("expected info shutdown log, got %v", log.entries)
	}
}

func TestManager_LoggerReceivesRetryWarning(t *testing.T) {
	log := &recordingLogger{}
	mgr := newTestManager(t,
		plugs.WithLogger(log),
		plugs.WithMaxAttempts(2),
		plugs.WithBackoff(func(int) time.Duration { return 0 }),
	)
	mgr.AddFunc("flaky", func(ctx context.Context) error { return errors.New("fail") })

	_ = mgr.Start(context.Background())

	if !log.has("warn", "retrying") {
		t.Errorf("expected warn retry log, got %v", log.entries)
	}
}

// A Manager must be reusable across sequential Start calls: the shutdown
// channel is re-armed for each run so the second run can still be shut down
// cleanly.
func TestManager_ReusableAcrossSequentialRuns(t *testing.T) {
	mgr := newTestManager(t)
	mgr.AddFunc("p", func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})

	runOnce := func() {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- mgr.Start(ctx) }()

		// Cancel once Start is actually running; using a short sleep here is
		// fine because the assertion is on the return value, not on timing.
		time.Sleep(10 * time.Millisecond)
		cancel()

		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("run returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("run did not return after cancel")
		}
	}

	runOnce()
	runOnce()
}

// Positive backoff must actually wait (exercises the timer.C path in sleep)
// and must exit promptly when the manager context is cancelled during the
// backoff window (exercises the ctx.Done path).
func TestManager_RetryBackoffHonorsContextCancel(t *testing.T) {
	mgr := newTestManager(t,
		plugs.WithMaxAttempts(5),
		plugs.WithBackoff(func(int) time.Duration { return 200 * time.Millisecond }),
	)

	var attempts atomic.Int32
	firstAttempt := make(chan struct{})
	mgr.AddFunc("flaky", func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			close(firstAttempt)
		}
		return errors.New("fail")
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()

	<-firstAttempt
	// Cancel while the retry loop is parked in sleep.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after cancel during backoff")
	}

	if got := attempts.Load(); got > 2 {
		t.Errorf("cancel during backoff did not stop retries: attempts=%d", got)
	}
}

// recordingLogger is a test double that captures every log call. Safe for
// concurrent use since the manager may log from multiple goroutines.
type recordingLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

type logEntry struct {
	level string
	msg   string
	kv    []any
}

func (l *recordingLogger) record(level, msg string, kv []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{level, msg, append([]any(nil), kv...)})
}

func (l *recordingLogger) has(level, msgSubstr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == level && strings.Contains(e.msg, msgSubstr) {
			return true
		}
	}
	return false
}

func (l *recordingLogger) Debug(msg string, kv ...any) { l.record("debug", msg, kv) }
func (l *recordingLogger) Info(msg string, kv ...any)  { l.record("info", msg, kv) }
func (l *recordingLogger) Warn(msg string, kv ...any)  { l.record("warn", msg, kv) }
func (l *recordingLogger) Error(msg string, kv ...any) { l.record("error", msg, kv) }
