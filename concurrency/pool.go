package concurrency

import (
	"context"
	"errors"
	"sync"
)

// ErrPoolClosed is returned by [Pool.Submit] once [Pool.Close] has been
// called or the pool's lifetime context is done.
var ErrPoolClosed = errors.New("concurrency: pool closed")

// Job is a unit of work submitted to a [Pool]. The passed ctx is the pool's
// lifetime context — it is cancelled during [Pool.Close], so long-running
// jobs should honor it.
type Job func(ctx context.Context)

// PoolOption configures a [Pool] at construction time.
type PoolOption func(*Pool)

// WithPanicHandler registers fn to receive the value recovered from a
// panicking job. When unset, panics are silently recovered so one bad job
// cannot take down a worker.
func WithPanicHandler(fn func(recovered any)) PoolOption {
	return func(p *Pool) { p.onPanic = fn }
}

// Pool is a fixed-size worker pool backed by a bounded job queue. Workers
// recover from job panics so one bad job cannot take down the pool.
//
// Pool is designed to start at application boot and live until shutdown. For
// one-shot parallel fan-out over a known slice, use [ForEach] instead.
type Pool struct {
	queue   chan Job
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	onPanic func(recovered any)

	mu     sync.RWMutex
	closed bool
}

// NewPool starts workers goroutines that pull from a queue of capacity
// queueSize. Both must be > 0. NewPool panics on invalid arguments so
// misconfiguration fails at startup.
func NewPool(workers, queueSize int, opts ...PoolOption) *Pool {
	if workers <= 0 {
		panic("concurrency: NewPool workers must be > 0")
	}
	if queueSize <= 0 {
		panic("concurrency: NewPool queueSize must be > 0")
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		queue:  make(chan Job, queueSize),
		ctx:    ctx,
		cancel: cancel,
	}
	for _, opt := range opts {
		opt(p)
	}
	p.wg.Add(workers)
	for range workers {
		go p.worker()
	}
	return p
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for job := range p.queue {
		p.runJob(job)
	}
}

func (p *Pool) runJob(job Job) {
	defer func() {
		if r := recover(); r != nil && p.onPanic != nil {
			p.onPanic(r)
		}
	}()
	job(p.ctx)
}

// Submit enqueues job. Blocks if the queue is full; returns when the job is
// accepted, ctx is cancelled, or the pool is closed.
func (p *Pool) Submit(ctx context.Context, job Job) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return ErrPoolClosed
	}
	select {
	case p.queue <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrPoolClosed
	}
}

// TrySubmit enqueues job without blocking. Returns false if the queue is full
// or the pool is closed.
func (p *Pool) TrySubmit(job Job) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return false
	}
	select {
	case p.queue <- job:
		return true
	default:
		return false
	}
}

// Close stops accepting new work, cancels the pool ctx to signal in-flight
// jobs and any pending [Pool.Submit] callers, and waits for all workers to
// drain the queue and exit. Safe to call multiple times; subsequent calls
// return immediately without re-waiting.
//
// Queued jobs are still executed; jobs should check ctx to exit early if they
// support cancellation.
func (p *Pool) Close() {
	// Cancel before acquiring the write lock so pending Submit callers
	// (holding RLock while blocked on a full queue) observe shutdown via
	// p.ctx.Done() and release the lock. Without this, Close would block
	// until every pending Submit unblocks on its own.
	p.cancel()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.queue)
	p.mu.Unlock()

	p.wg.Wait()
}
