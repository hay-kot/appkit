package plugs

import (
	"context"
	"errors"
	"os/signal"
	"sync"
	"time"
)

// Manager runs a group of [Plugin]s concurrently and coordinates shutdown.
//
// A single Manager may be reused across sequential [Manager.Start] calls; the
// shutdown signal is re-armed on each run. Overlapping Start calls return
// [ErrManagerAlreadyStarted].
type Manager struct {
	mu       sync.Mutex
	started  bool
	plugins  []Plugin
	shutdown chan struct{}
	opts     *managerOpts
}

// New constructs a Manager with the given options.
func New(opts ...Option) *Manager {
	o := defaultOpts()
	for _, fn := range opts {
		fn(o)
	}
	return &Manager{opts: o}
}

// Add registers one or more plugins. Must be called before [Manager.Start].
func (m *Manager) Add(p ...Plugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins = append(m.plugins, p...)
}

// AddFunc is a shorthand for Add(PluginFunc(name, fn)).
func (m *Manager) AddFunc(name string, start func(ctx context.Context) error) {
	m.Add(PluginFunc(name, start))
}

// Shutdown initiates a graceful shutdown of a running Manager. Idempotent:
// calls before Start or after Start has returned are no-ops, and repeated
// calls during a single run are safe.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		select {
		case <-m.shutdown:
			// already closed
		default:
			close(m.shutdown)
		}
	}
}

// Start blocks until all plugins exit. Exit is triggered by any of: the parent
// context cancelling, one of the configured signals firing, [Manager.Shutdown]
// being called, or a plugin returning an error.
//
// Returns nil on clean shutdown, [ErrManagerAlreadyStarted] if an earlier
// Start has not yet returned, or a *[PluginErrors] aggregating any plugin
// failures (including a shutdown timeout if plugins fail to exit within
// WithTimeout).
func (m *Manager) Start(ctx context.Context) error {
	shutdown, ok := m.claimStarted()
	if !ok {
		return ErrManagerAlreadyStarted
	}
	defer m.releaseStarted()

	ctx, cancel := signal.NotifyContext(ctx, m.opts.signals...)
	defer cancel()

	var (
		errsMu sync.Mutex
		errs   []error
	)
	addErr := func(err error) {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		errsMu.Lock()
		errs = append(errs, err)
		errsMu.Unlock()
	}

	plugins := m.snapshotPlugins()
	done := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(len(plugins))
	for _, p := range plugins {
		go func(p Plugin) {
			defer wg.Done()
			if err := runWithRetry(ctx, p, m.opts); err != nil {
				addErr(err)
				cancel()
			}
		}(p)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	go func() {
		select {
		case <-shutdown:
			cancel()
		case <-ctx.Done():
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		m.opts.log.Info("plugs: shutting down", "cause", context.Cause(ctx))
		timer := time.NewTimer(m.opts.timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			m.opts.log.Warn("plugs: timeout waiting for plugins to stop",
				"timeout", m.opts.timeout,
			)
			addErr(context.DeadlineExceeded)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return &PluginErrors{Errors: errs}
}

// claimStarted atomically marks the manager as running and arms a fresh
// shutdown channel for this run. The returned channel is captured by Start
// so that a subsequent run cannot swap it out from under the watcher
// goroutine.
func (m *Manager) claimStarted() (chan struct{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil, false
	}
	m.started = true
	m.shutdown = make(chan struct{})
	return m.shutdown, true
}

func (m *Manager) releaseStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = false
}

func (m *Manager) snapshotPlugins() []Plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Plugin, len(m.plugins))
	copy(out, m.plugins)
	return out
}
