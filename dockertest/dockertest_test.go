package dockertest_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hay-kot/appkit/dockertest"
)

// Test images are small and cheap to pull:
//
//	hashicorp/http-echo:0.2.3 — ~2MB scratch image, serves a fixed HTTP body
//	                           and logs "Server is listening on :5678" at startup.
//	alpine:3.20               — ~7MB, used for shell/exec-based tests.
const (
	httpEchoImage = "hashicorp/http-echo:0.2.3"
	alpineImage   = "alpine:3.20"
)

var dockerOK bool

func TestMain(m *testing.M) {
	dockerOK = exec.Command("docker", "version").Run() == nil
	code := m.Run()
	if dockerOK {
		sweepOrphans()
	}
	os.Exit(code)
}

// sweepOrphans removes any containers with this package's label that survived
// a crashed or panicking test.
func sweepOrphans() {
	out, err := exec.Command("docker", "ps", "-aq",
		"--filter", "label="+dockertest.LabelKey+"=1").Output()
	if err != nil {
		return
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return
	}
	_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
}

func requireDocker(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("docker tests disabled with -short")
	}
	if !dockerOK {
		t.Skip("docker not available")
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRun_RequiresImage(t *testing.T) {
	t.Parallel()
	_, err := dockertest.Run(context.Background(), dockertest.Options{})
	if err == nil {
		t.Fatal("expected error for empty Image")
	}
}

func TestRun_Lifecycle(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	c, err := dockertest.Run(ctx, dockertest.Options{
		Image: httpEchoImage,
		Cmd:   []string{"-text=hello"},
		Ports: []string{"5678/tcp"},
		Wait:  dockertest.WaitForListeningPort("5678/tcp"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	if c.ID == "" {
		t.Error("expected non-empty ID")
	}
	if !strings.HasPrefix(c.Name, "appkit-") {
		t.Errorf("expected generated name with appkit- prefix, got %q", c.Name)
	}
}

func TestRun_CustomName(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	name := "appkit-dockertest-custom-" + randHex(t)
	c, err := dockertest.Run(ctx, dockertest.Options{
		Image: alpineImage,
		Name:  name,
		Cmd:   []string{"sleep", "600"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	if c.Name != name {
		t.Errorf("Name: got %q, want %q", c.Name, name)
	}
}

func TestRun_UnknownImageReturnsError(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := dockertest.Run(ctx, dockertest.Options{
		Image: "appkit-dockertest-nonexistent:does-not-exist",
	})
	if err == nil {
		t.Fatal("expected error for unknown image")
	}
}

func TestEndpoint_ReturnsHostPort(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	c := dockertest.Start(t, dockertest.Options{
		Image: httpEchoImage,
		Cmd:   []string{"-text=hello"},
		Ports: []string{"5678/tcp"},
		Wait:  dockertest.WaitForListeningPort("5678/tcp"),
	})

	addr, err := c.Endpoint(ctx, "5678/tcp")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("expected 127.0.0.1: prefix, got %q", addr)
	}

	// containerPort normalization: "5678" and "5678/tcp" should resolve equally.
	p1, err := c.MappedPort(ctx, "5678/tcp")
	if err != nil {
		t.Fatalf("MappedPort(5678/tcp): %v", err)
	}
	p2, err := c.MappedPort(ctx, "5678")
	if err != nil {
		t.Fatalf("MappedPort(5678): %v", err)
	}
	if p1 != p2 {
		t.Errorf("port differs after normalization: %q vs %q", p1, p2)
	}
}

func TestWaitForHTTP(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	c := dockertest.Start(t, dockertest.Options{
		Image: httpEchoImage,
		Cmd:   []string{"-text=hello"},
		Ports: []string{"5678/tcp"},
		Wait:  dockertest.WaitForHTTP("5678/tcp", "/", 200),
	})

	addr, err := c.Endpoint(ctx, "5678/tcp")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("body: got %q, want to contain 'hello'", body)
	}
}

func TestWaitForLog(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	c, err := dockertest.Run(ctx, dockertest.Options{
		Image: httpEchoImage,
		Cmd:   []string{"-text=hello"},
		Ports: []string{"5678/tcp"},
		Wait:  dockertest.WaitForLog(`listening`),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
}

func TestWaitForExec(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	_ = dockertest.Start(t, dockertest.Options{
		Image: alpineImage,
		Cmd:   []string{"sleep", "600"},
		Wait:  dockertest.WaitForExec("/bin/true"),
	})
}

func TestWaitAll_RunsInOrder(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	_ = dockertest.Start(t, dockertest.Options{
		Image: httpEchoImage,
		Cmd:   []string{"-text=hello"},
		Ports: []string{"5678/tcp"},
		Wait: dockertest.WaitAll(
			dockertest.WaitForLog(`listening`),
			dockertest.WaitForListeningPort("5678/tcp"),
			dockertest.WaitForHTTP("5678/tcp", "/", 200),
		),
	})
}

func TestWaitReady_ReturnsOnCtxCancel(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	// Start with no published ports so MappedPort keeps erroring; the wait
	// will loop until ctx expires.
	c := dockertest.Start(t, dockertest.Options{
		Image: alpineImage,
		Cmd:   []string{"sleep", "600"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := dockertest.WaitForListeningPort("9999/tcp").WaitReady(ctx, c)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on ctx cancel")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error chain: got %v, want DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("wait did not return promptly on ctx cancel: %v", elapsed)
	}
}

func TestRun_WaitFailurePropagatesAndCleansUp(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	name := "appkit-dockertest-waitfail-" + randHex(t)
	_, err := dockertest.Run(ctx, dockertest.Options{
		Image: alpineImage,
		Name:  name,
		Cmd:   []string{"sleep", "600"},
		Wait:  dockertest.WaitForListeningPort("9999/tcp"),
	})
	if err == nil {
		t.Fatal("expected wait error")
	}

	// The container should have been terminated by Run.
	out, _ := exec.Command("docker", "ps", "-a",
		"--filter", "name="+name, "--format", "{{.Names}}").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("container %q still exists after failed wait", name)
	}
}

func TestExec_StdoutStderrExitCode(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	c := dockertest.Start(t, dockertest.Options{
		Image: alpineImage,
		Cmd:   []string{"sleep", "600"},
	})

	res, err := c.Exec(ctx, "sh", "-c", "echo out; echo err 1>&2; exit 3")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "out") {
		t.Errorf("Stdout: got %q", res.Stdout)
	}
	if !strings.Contains(string(res.Stderr), "err") {
		t.Errorf("Stderr: got %q", res.Stderr)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode: got %d, want 3", res.ExitCode)
	}
}

func TestLogs_Streams(t *testing.T) {
	requireDocker(t)
	t.Parallel()
	ctx := testCtx(t)

	c := dockertest.Start(t, dockertest.Options{
		Image: alpineImage,
		Cmd:   []string{"sh", "-c", "echo greetings; sleep 600"},
	})

	rc, err := c.Logs(ctx)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	found := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(rc)
		for s.Scan() {
			if strings.Contains(s.Text(), "greetings") {
				found <- s.Text()
				return
			}
		}
		found <- ""
	}()

	select {
	case line := <-found:
		if !strings.Contains(line, "greetings") {
			t.Errorf("did not see 'greetings' in logs: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for log line")
	}
}

func TestStart_RegistersCleanup(t *testing.T) {
	requireDocker(t)

	var name string
	t.Run("inner", func(t *testing.T) {
		c := dockertest.Start(t, dockertest.Options{
			Image: alpineImage,
			Cmd:   []string{"sleep", "600"},
		})
		name = c.Name
	})

	// The inner subtest's t.Cleanup has run by now.
	out, _ := exec.Command("docker", "ps", "-a",
		"--filter", "name="+name, "--format", "{{.Names}}").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("container %q still exists after subtest cleanup", name)
	}
}

func TestTerminate_IsIdempotentForMissingContainer(t *testing.T) {
	requireDocker(t)
	t.Parallel()

	c := dockertest.Start(t, dockertest.Options{
		Image: alpineImage,
		Cmd:   []string{"sleep", "600"},
	})
	if err := c.Terminate(context.Background()); err != nil {
		t.Fatalf("first Terminate: %v", err)
	}
	// Second call should fail (docker rm returns non-zero), but not panic or
	// otherwise corrupt state.
	if err := c.Terminate(context.Background()); err == nil {
		t.Log("second Terminate succeeded — acceptable if docker tolerates missing names")
	}
}

// randHex returns a short random hex string for unique container names.
func randHex(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b[:])
}
