// Package dockertest is a zero-dependency harness for running Docker
// containers from Go tests. It shells out to the docker CLI via os/exec
// rather than binding the Docker API, keeping the import graph empty.
//
// The primary entry point for tests is [Start], which ties the container's
// lifetime to the test via t.Cleanup:
//
//	c := dockertest.Start(t, dockertest.Options{
//	    Image: "redis:7-alpine",
//	    Ports: []string{"6379/tcp"},
//	    Wait:  dockertest.WaitForListeningPort("6379/tcp"),
//	})
//	addr, _ := c.Endpoint(ctx, "6379/tcp")
//
// Use [Run] from TestMain or other non-*testing.T contexts; the caller is
// responsible for calling [Container.Terminate].
//
// Host ports are always assigned by Docker on 127.0.0.1. Resolve them with
// [Container.MappedPort] or [Container.Endpoint].
package dockertest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// LabelKey is applied to every container started by this package. Callers can
// use it to sweep orphans from crashed test runs, e.g.
//
//	docker ps -aq --filter label=appkit.dockertest=1 | xargs -r docker rm -f
const LabelKey = "appkit.dockertest"

// Options configures a container launch.
type Options struct {
	// Image is the Docker image reference. Required.
	Image string

	// Name is the container name. If empty, a unique name is generated.
	Name string

	// Env sets environment variables inside the container.
	Env map[string]string

	// Cmd overrides the image's default command.
	Cmd []string

	// Ports lists container ports to publish, e.g. "5432/tcp" or "6379".
	// Each port is published to 127.0.0.1 on an ephemeral host port chosen
	// by Docker. Use [Container.MappedPort] to resolve the assignment.
	Ports []string

	// Labels are added to the container alongside [LabelKey].
	Labels map[string]string

	// Wait is an optional readiness check. [Run] and [Start] return only
	// after Wait.WaitReady succeeds or ctx is done.
	Wait Wait

	// Platform sets --platform, e.g. "linux/amd64" on arm64 hosts running
	// images without a native arm64 build.
	Platform string
}

// Container is a handle to a running Docker container.
type Container struct {
	// ID is the full container ID returned by docker run.
	ID string
	// Name is the container name (either Options.Name or a generated value).
	Name string

	portMu sync.RWMutex
	ports  map[string]string // normalized container port → host port
}

// Run starts a container according to opts, waits for readiness if opts.Wait
// is set, and returns a handle. The caller is responsible for calling
// [Container.Terminate].
func Run(ctx context.Context, opts Options) (*Container, error) {
	if opts.Image == "" {
		return nil, errors.New("dockertest: Image is required")
	}

	name := opts.Name
	if name == "" {
		suffix, err := randomSuffix()
		if err != nil {
			return nil, fmt.Errorf("dockertest: generate name: %w", err)
		}
		name = "appkit-" + suffix
	}

	args := []string{"run", "-d", "--name", name, "--label", LabelKey + "=1"}
	for k, v := range opts.Labels {
		args = append(args, "--label", k+"="+v)
	}
	for k, v := range opts.Env {
		args = append(args, "-e", k+"="+v)
	}
	for _, p := range opts.Ports {
		args = append(args, "-p", "127.0.0.1::"+normalizePort(p))
	}
	if opts.Platform != "" {
		args = append(args, "--platform", opts.Platform)
	}
	args = append(args, opts.Image)
	args = append(args, opts.Cmd...)

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		// docker run -d may have created the container before the CLI was
		// killed (e.g. ctx deadline during image pull). Best-effort cleanup
		// by name so callers never see a half-created orphan.
		_, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput()
		return nil, fmt.Errorf("dockertest: docker run: %w: %s", err, bytes.TrimSpace(out))
	}

	c := &Container{
		ID:    strings.TrimSpace(string(out)),
		Name:  name,
		ports: make(map[string]string),
	}

	if opts.Wait != nil {
		if werr := opts.Wait.WaitReady(ctx, c); werr != nil {
			_ = c.Terminate(context.Background())
			return nil, fmt.Errorf("dockertest: wait: %w", werr)
		}
	}
	return c, nil
}

// Start calls [Run] and registers [Container.Terminate] with tb.Cleanup. It
// fails the test on error via tb.Fatal. If tb has a test deadline, Run is
// bounded by it so a stalled docker daemon cannot hang the test indefinitely.
func Start(tb testing.TB, opts Options) *Container {
	tb.Helper()
	ctx := context.Background()
	if dt, ok := tb.(interface{ Deadline() (time.Time, bool) }); ok {
		if d, has := dt.Deadline(); has {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, d)
			tb.Cleanup(cancel)
		}
	}
	c, err := Run(ctx, opts)
	if err != nil {
		tb.Fatalf("dockertest.Start: %v", err)
	}
	tb.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			tb.Logf("dockertest: terminate %s: %v", c.Name, err)
		}
	})
	return c
}

// Host returns the host interface containers are published on. It is always
// "127.0.0.1".
func (c *Container) Host() string { return "127.0.0.1" }

// MappedPort returns the host port assigned to containerPort. containerPort
// may be "5432" or "5432/tcp"; the "/tcp" suffix is added if missing.
func (c *Container) MappedPort(ctx context.Context, containerPort string) (string, error) {
	key := normalizePort(containerPort)

	c.portMu.RLock()
	if p, ok := c.ports[key]; ok {
		c.portMu.RUnlock()
		return p, nil
	}
	c.portMu.RUnlock()

	out, err := exec.CommandContext(ctx, "docker", "port", c.Name, key).Output()
	if err != nil {
		return "", fmt.Errorf("dockertest: docker port %s %s: %w", c.Name, key, err)
	}

	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("dockertest: no host mapping for %s in %s", key, c.Name)
	}
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		return "", fmt.Errorf("dockertest: unexpected docker port output: %q", line)
	}
	port := line[idx+1:]

	c.portMu.Lock()
	c.ports[key] = port
	c.portMu.Unlock()

	return port, nil
}

// Endpoint returns host:port for containerPort, suitable for TCP clients.
func (c *Container) Endpoint(ctx context.Context, containerPort string) (string, error) {
	p, err := c.MappedPort(ctx, containerPort)
	if err != nil {
		return "", err
	}
	return c.Host() + ":" + p, nil
}

// ExecResult is the outcome of a [Container.Exec] call.
type ExecResult struct {
	// Stdout is the command's standard output.
	Stdout []byte
	// Stderr is the command's standard error.
	Stderr []byte
	// ExitCode is the command's exit status.
	ExitCode int
}

// Exec runs cmd inside the container via docker exec. A non-zero exit code
// is reported through the returned [ExecResult]; err is non-nil only when
// docker itself failed to invoke the command.
func (c *Container) Exec(ctx context.Context, cmd ...string) (ExecResult, error) {
	args := append([]string{"exec", c.Name}, cmd...)
	ecmd := exec.CommandContext(ctx, "docker", args...)

	var out, stderr bytes.Buffer
	ecmd.Stdout = &out
	ecmd.Stderr = &stderr

	err := ecmd.Run()
	res := ExecResult{Stdout: out.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("dockertest: docker exec: %w", err)
	}
	return res, nil
}

// Logs returns a reader that streams the container's combined stdout and
// stderr. The reader follows new output until Close is called or ctx is
// cancelled.
func (c *Container) Logs(ctx context.Context) (io.ReadCloser, error) {
	lctx, cancel := context.WithCancel(ctx)
	ecmd := exec.CommandContext(lctx, "docker", "logs", "-f", c.Name)

	pr, pw := io.Pipe()
	ecmd.Stdout = pw
	ecmd.Stderr = pw

	if err := ecmd.Start(); err != nil {
		cancel()
		_ = pw.Close()
		return nil, fmt.Errorf("dockertest: docker logs: %w", err)
	}
	go func() {
		_ = pw.CloseWithError(ecmd.Wait())
	}()
	return &logsReader{ReadCloser: pr, cancel: cancel}, nil
}

type logsReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (l *logsReader) Close() error {
	l.cancel()
	return l.ReadCloser.Close()
}

// Terminate stops and removes the container. It is safe to call more than
// once; subsequent calls return an error from docker rm.
func (c *Container) Terminate(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", c.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dockertest: docker rm: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func normalizePort(p string) string {
	if strings.Contains(p, "/") {
		return p
	}
	return p + "/tcp"
}

func randomSuffix() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
