package dockertest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"time"
)

// Wait describes a container readiness check. Implementations should honour
// ctx cancellation and return ctx.Err() promptly when it fires.
type Wait interface {
	WaitReady(ctx context.Context, c *Container) error
}

// WaitFunc adapts an ordinary function to the [Wait] interface. Use it for
// one-off protocol-level probes (SQL ping, Redis PING, etc.) that don't fit
// the built-in wait strategies.
type WaitFunc func(ctx context.Context, c *Container) error

// WaitReady implements [Wait].
func (f WaitFunc) WaitReady(ctx context.Context, c *Container) error {
	return f(ctx, c)
}

// ListeningPortWait dials the container's mapped host port until a TCP
// connection succeeds.
type ListeningPortWait struct {
	// ContainerPort is the port inside the container, e.g. "5432/tcp".
	ContainerPort string
	// PollInterval is the delay between dial attempts. Defaults to 200ms.
	PollInterval time.Duration
	// DialTimeout is the per-attempt timeout. Defaults to 1s.
	DialTimeout time.Duration
}

// WaitForListeningPort is a convenience constructor for [ListeningPortWait]
// using default intervals.
func WaitForListeningPort(containerPort string) *ListeningPortWait {
	return &ListeningPortWait{ContainerPort: containerPort}
}

// WaitReady implements [Wait].
func (w *ListeningPortWait) WaitReady(ctx context.Context, c *Container) error {
	poll := w.PollInterval
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	dialTimeout := w.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = time.Second
	}

	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return joinCtxErr(err, lastErr)
		}
		addr, err := c.Endpoint(ctx, w.ContainerPort)
		if err == nil {
			var d net.Dialer
			dctx, cancel := context.WithTimeout(ctx, dialTimeout)
			conn, derr := d.DialContext(dctx, "tcp", addr)
			cancel()
			if derr == nil {
				_ = conn.Close()
				return nil
			}
			lastErr = derr
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return joinCtxErr(ctx.Err(), lastErr)
		case <-time.After(poll):
		}
	}
}

// LogWait streams the container's logs and returns once Pattern matches
// Occurrences lines.
type LogWait struct {
	// Pattern is the regular expression to match. Required.
	Pattern *regexp.Regexp
	// Occurrences is the number of matching lines required before the
	// wait succeeds. Defaults to 1.
	Occurrences int
}

// WaitForLog is a convenience constructor: WaitReady returns once a log line
// matches pattern.
func WaitForLog(pattern string) *LogWait {
	return &LogWait{Pattern: regexp.MustCompile(pattern), Occurrences: 1}
}

// WaitReady implements [Wait].
func (w *LogWait) WaitReady(ctx context.Context, c *Container) error {
	if w.Pattern == nil {
		return errors.New("dockertest: LogWait.Pattern is nil")
	}
	want := w.Occurrences
	if want <= 0 {
		want = 1
	}

	rc, err := c.Logs(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	lines := make(chan string, 32)
	scanErr := make(chan error, 1)
	go func() {
		s := bufio.NewScanner(rc)
		s.Buffer(make([]byte, 64*1024), 1024*1024)
		for s.Scan() {
			select {
			case lines <- s.Text():
			case <-ctx.Done():
				scanErr <- nil
				close(lines)
				return
			}
		}
		scanErr <- s.Err()
		close(lines)
	}()

	seen := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				if err := <-scanErr; err != nil {
					return fmt.Errorf("dockertest: log stream: %w", err)
				}
				return errors.New("dockertest: log stream ended before pattern matched")
			}
			if w.Pattern.MatchString(line) {
				seen++
				if seen >= want {
					return nil
				}
			}
		}
	}
}

// HTTPWait issues HTTP GET requests until the response status matches Status.
type HTTPWait struct {
	// ContainerPort is the port inside the container, e.g. "8080/tcp".
	ContainerPort string
	// Path is the request path. Defaults to "/".
	Path string
	// Status is the desired status code. Defaults to 200.
	Status int
	// PollInterval is the delay between attempts. Defaults to 200ms.
	PollInterval time.Duration
	// Timeout is the per-request timeout. Defaults to 1s.
	Timeout time.Duration
}

// WaitForHTTP is a convenience constructor for [HTTPWait].
func WaitForHTTP(containerPort, path string, want int) *HTTPWait {
	return &HTTPWait{ContainerPort: containerPort, Path: path, Status: want}
}

// WaitReady implements [Wait].
func (w *HTTPWait) WaitReady(ctx context.Context, c *Container) error {
	poll := w.PollInterval
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}
	path := w.Path
	if path == "" {
		path = "/"
	}
	status := w.Status
	if status == 0 {
		status = http.StatusOK
	}

	client := &http.Client{Timeout: timeout}
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return joinCtxErr(err, lastErr)
		}
		addr, err := c.Endpoint(ctx, w.ContainerPort)
		if err != nil {
			lastErr = err
		} else {
			req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
			if rerr != nil {
				lastErr = rerr
			} else {
				resp, derr := client.Do(req)
				switch {
				case derr != nil:
					lastErr = derr
				case resp.StatusCode == status:
					_ = resp.Body.Close()
					return nil
				default:
					_ = resp.Body.Close()
					lastErr = fmt.Errorf("status %d", resp.StatusCode)
				}
			}
		}
		select {
		case <-ctx.Done():
			return joinCtxErr(ctx.Err(), lastErr)
		case <-time.After(poll):
		}
	}
}

// ExecWait runs cmd via docker exec and returns once it exits 0.
type ExecWait struct {
	// Cmd is the command and arguments to run inside the container.
	// Required.
	Cmd []string
	// PollInterval is the delay between attempts. Defaults to 200ms.
	PollInterval time.Duration
}

// WaitForExec is a convenience constructor: WaitReady returns once
// `docker exec <container> cmd...` exits with status 0.
func WaitForExec(cmd ...string) *ExecWait {
	return &ExecWait{Cmd: cmd}
}

// WaitReady implements [Wait].
func (w *ExecWait) WaitReady(ctx context.Context, c *Container) error {
	if len(w.Cmd) == 0 {
		return errors.New("dockertest: ExecWait.Cmd is empty")
	}
	poll := w.PollInterval
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}

	var lastStderr []byte
	for {
		if err := ctx.Err(); err != nil {
			if len(lastStderr) > 0 {
				return fmt.Errorf("%w (last stderr: %s)", err, lastStderr)
			}
			return err
		}
		res, err := c.Exec(ctx, w.Cmd...)
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		if err == nil {
			lastStderr = res.Stderr
		}
		select {
		case <-ctx.Done():
			if len(lastStderr) > 0 {
				return fmt.Errorf("%w (last stderr: %s)", ctx.Err(), lastStderr)
			}
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// AllWait runs each [Wait] sequentially and returns on the first failure.
type AllWait struct {
	// Waits is the ordered list of checks to run.
	Waits []Wait
}

// WaitAll composes multiple [Wait]s; each runs in sequence against the same
// container.
func WaitAll(waits ...Wait) *AllWait {
	return &AllWait{Waits: waits}
}

// WaitReady implements [Wait].
func (w *AllWait) WaitReady(ctx context.Context, c *Container) error {
	for _, inner := range w.Waits {
		if err := inner.WaitReady(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func joinCtxErr(ctxErr, lastErr error) error {
	if lastErr == nil {
		return ctxErr
	}
	return fmt.Errorf("%w (last error: %w)", ctxErr, lastErr)
}
