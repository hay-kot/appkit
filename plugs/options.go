package plugs

import (
	"os"
	"syscall"
	"time"
)

// BackoffFunc returns the wait duration before retry attempt n (0-indexed for
// the first delay, i.e. after the first failure). Return 0 or negative for no
// delay.
type BackoffFunc func(attempt int) time.Duration

// DefaultBackoff is exponential: 100ms, 200ms, 400ms, ... capped at 5s. It is
// deterministic; callers who want jitter should pass their own [BackoffFunc].
func DefaultBackoff(attempt int) time.Duration {
	if attempt < 0 {
		return 0
	}
	d := 100 * time.Millisecond * (1 << uint(attempt))
	if d <= 0 || d > 5*time.Second {
		return 5 * time.Second
	}
	return d
}

type managerOpts struct {
	signals     []os.Signal
	timeout     time.Duration
	log         Logger
	maxAttempts int
	backoff     BackoffFunc
}

// Option configures a [Manager].
type Option func(*managerOpts)

func defaultOpts() *managerOpts {
	return &managerOpts{
		signals:     []os.Signal{os.Interrupt, syscall.SIGTERM},
		timeout:     5 * time.Second,
		log:         Nop(),
		maxAttempts: 1,
		backoff:     DefaultBackoff,
	}
}

// WithSignals sets the OS signals that trigger manager shutdown. Defaults to
// SIGINT and SIGTERM. Pass no signals to disable signal handling.
func WithSignals(sigs ...os.Signal) Option {
	return func(o *managerOpts) { o.signals = sigs }
}

// WithTimeout sets how long [Manager.Start] waits for plugins to exit after
// shutdown is signalled. Defaults to 5s. Exceeding the timeout causes Start to
// return with context.DeadlineExceeded aggregated into its error.
func WithTimeout(d time.Duration) Option {
	return func(o *managerOpts) { o.timeout = d }
}

// WithLogger configures structured logging. Defaults to [applog.Nop].
func WithLogger(l Logger) Option {
	return func(o *managerOpts) {
		if l == nil {
			l = Nop()
		}
		o.log = l
	}
}

// WithMaxAttempts caps the total number of times a failing plugin will be
// run. A value of 1 (the default) means run once with no retries; 2 means
// run and retry once on failure; and so on. Non-positive values are treated
// as 1.
func WithMaxAttempts(n int) Option {
	return func(o *managerOpts) { o.maxAttempts = n }
}

// WithBackoff sets the retry backoff function. Defaults to [DefaultBackoff].
func WithBackoff(fn BackoffFunc) Option {
	return func(o *managerOpts) {
		if fn == nil {
			fn = DefaultBackoff
		}
		o.backoff = fn
	}
}
