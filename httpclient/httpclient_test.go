package httpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hay-kot/appkit/httpclient"
)

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func newEchoServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func TestClient_GetJoinsBaseURL(t *testing.T) {
	var gotPath string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	})
	c := httpclient.New(nil, s.URL)

	resp, err := c.Get(context.Background(), "/users/1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotPath != "/users/1" {
		t.Errorf("want /users/1, got %q", gotPath)
	}
}

func TestClient_GetStripsTrailingSlashOnBase(t *testing.T) {
	var gotPath string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	})
	c := httpclient.New(nil, s.URL+"/")

	resp, err := c.Get(context.Background(), "users")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotPath != "/users" {
		t.Errorf("want /users, got %q", gotPath)
	}
}

func TestClient_AbsoluteURLBypassesBase(t *testing.T) {
	var hit atomic.Bool
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	})
	c := httpclient.New(nil, "http://example.invalid")

	resp, err := c.Get(context.Background(), s.URL+"/path")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !hit.Load() {
		t.Error("request did not hit the absolute URL")
	}
}

// URL schemes are case-insensitive per RFC 3986, so uppercase http/https
// prefixes must also bypass the base URL.
func TestClient_AbsoluteURLBypassesBaseCaseInsensitive(t *testing.T) {
	var hit atomic.Bool
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	})
	c := httpclient.New(nil, "http://example.invalid")

	// Synthesize an upper-case scheme against the test server's address.
	upper := "HTTP://" + strings.TrimPrefix(s.URL, "http://") + "/path"
	resp, err := c.Get(context.Background(), upper)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !hit.Load() {
		t.Error("uppercase HTTP scheme did not bypass base URL")
	}
}

func TestClient_PostSendsBody(t *testing.T) {
	var gotBody string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	})
	c := httpclient.New(nil, s.URL)

	resp, err := c.Post(context.Background(), "/", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotBody != "hello" {
		t.Errorf("want hello, got %q", gotBody)
	}
}

// Put/Patch/Delete are thin wrappers but differ in method and body-handling,
// so a table-driven test confirms each dispatches correctly.
func TestClient_VerbWrappersDispatchCorrectMethod(t *testing.T) {
	cases := []struct {
		name, method, wantBody string
		call                   func(c *httpclient.Client, ctx context.Context, path string) (*http.Response, error)
	}{
		{"Put", http.MethodPut, "put-body", func(c *httpclient.Client, ctx context.Context, path string) (*http.Response, error) {
			return c.Put(ctx, path, strings.NewReader("put-body"))
		}},
		{"Patch", http.MethodPatch, "patch-body", func(c *httpclient.Client, ctx context.Context, path string) (*http.Response, error) {
			return c.Patch(ctx, path, strings.NewReader("patch-body"))
		}},
		{"Delete", http.MethodDelete, "", func(c *httpclient.Client, ctx context.Context, path string) (*http.Response, error) {
			return c.Delete(ctx, path)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotBody string
			s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
			})
			c := httpclient.New(nil, s.URL)

			resp, err := tc.call(c, context.Background(), "/")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if gotMethod != tc.method {
				t.Errorf("method: want %s, got %s", tc.method, gotMethod)
			}
			if gotBody != tc.wantBody {
				t.Errorf("body: want %q, got %q", tc.wantBody, gotBody)
			}
		})
	}
}

// Do must send a caller-constructed request through the middleware chain.
func TestClient_DoSendsThroughMiddleware(t *testing.T) {
	var gotAuth string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	})
	c := httpclient.New(nil, s.URL, httpclient.BearerAuth(func() string { return "raw-do" }))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotAuth != "Bearer raw-do" {
		t.Errorf("middleware not applied to Do: got %q", gotAuth)
	}
}

func TestClient_ContextCancellationPropagates(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	c := httpclient.New(nil, s.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate

	resp, err := c.Get(ctx, "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected error when context is already cancelled")
	}
}

func TestBearerAuth_SetsHeaderWhenTokenNonEmpty(t *testing.T) {
	var gotAuth string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	})

	token := "tok-123"
	c := httpclient.New(nil, s.URL, httpclient.BearerAuth(func() string { return token }))

	resp, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotAuth != "Bearer tok-123" {
		t.Errorf("want Bearer tok-123, got %q", gotAuth)
	}
}

func TestBearerAuth_EmptyTokenSkipsHeader(t *testing.T) {
	var gotAuth string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	})

	c := httpclient.New(nil, s.URL, httpclient.BearerAuth(func() string { return "" }))
	resp, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotAuth != "" {
		t.Errorf("expected no Authorization, got %q", gotAuth)
	}
}

// BearerAuth should read the token fresh on every request so rotation via
// an updated closure takes effect without reconstructing the client.
func TestBearerAuth_TokenIsResolvedPerRequest(t *testing.T) {
	var seen []string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
	})

	current := "first"
	c := httpclient.New(nil, s.URL, httpclient.BearerAuth(func() string { return current }))

	r1, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = r1.Body.Close()

	current = "second"
	r2, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = r2.Body.Close()

	if len(seen) != 2 || seen[0] != "Bearer first" || seen[1] != "Bearer second" {
		t.Errorf("tokens not rotated: %v", seen)
	}
}

func TestJSONContent_SetsDefaultContentType(t *testing.T) {
	var gotCT string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
	})
	c := httpclient.New(nil, s.URL, httpclient.JSONContent())

	resp, err := c.Post(context.Background(), "/", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotCT != "application/json" {
		t.Errorf("want application/json, got %q", gotCT)
	}
}

func TestJSONContent_DoesNotOverrideExisting(t *testing.T) {
	var gotCT string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
	})

	// Middleware that sets a custom content-type first.
	custom := func(next httpclient.Doer) httpclient.Doer {
		return httpclient.DoerFunc(func(req *http.Request) (*http.Response, error) {
			req.Header.Set("Content-Type", "application/x-custom")
			return next.Do(req)
		})
	}

	c := httpclient.New(nil, s.URL, custom, httpclient.JSONContent())
	resp, err := c.Post(context.Background(), "/", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotCT != "application/x-custom" {
		t.Errorf("custom content-type was overridden: got %q", gotCT)
	}
}

// Per-call middleware runs for a single request and wraps the client's
// existing chain from the outside.
func TestClient_PerCallMiddlewareAppliedForSingleCall(t *testing.T) {
	var gotHeader string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Per-Call")
	})
	c := httpclient.New(nil, s.URL)

	resp, err := c.Get(context.Background(), "/", httpclient.Header("X-Per-Call", "once"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotHeader != "once" {
		t.Errorf("per-call header not applied: got %q", gotHeader)
	}

	// A second call without the per-call middleware must not see it.
	gotHeader = ""
	resp2, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if gotHeader != "" {
		t.Errorf("per-call middleware leaked to later call: %q", gotHeader)
	}
}

// Per-call middleware sits outside the client's chain so it observes each
// request before the client's middlewares do.
func TestClient_PerCallMiddlewareWrapsClientChain(t *testing.T) {
	var gotOrder string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrder = r.Header.Get("X-Order")
	})
	appender := func(label string) httpclient.Middleware {
		return func(next httpclient.Doer) httpclient.Doer {
			return httpclient.DoerFunc(func(req *http.Request) (*http.Response, error) {
				existing := req.Header.Get("X-Order")
				if existing != "" {
					req.Header.Set("X-Order", existing+","+label)
				} else {
					req.Header.Set("X-Order", label)
				}
				return next.Do(req)
			})
		}
	}
	c := httpclient.New(nil, s.URL, appender("client"))

	resp, err := c.Get(context.Background(), "/", appender("call"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotOrder != "call,client" {
		t.Errorf("want call,client, got %q", gotOrder)
	}
}

// Multiple per-call middlewares follow the same ordering as the client's:
// the first registered runs closest to the caller.
func TestClient_PerCallMiddlewareOrderingIsOuterFirst(t *testing.T) {
	var gotOrder string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrder = r.Header.Get("X-Order")
	})
	appender := func(label string) httpclient.Middleware {
		return func(next httpclient.Doer) httpclient.Doer {
			return httpclient.DoerFunc(func(req *http.Request) (*http.Response, error) {
				existing := req.Header.Get("X-Order")
				if existing != "" {
					req.Header.Set("X-Order", existing+","+label)
				} else {
					req.Header.Set("X-Order", label)
				}
				return next.Do(req)
			})
		}
	}
	c := httpclient.New(nil, s.URL)

	resp, err := c.Get(context.Background(), "/", appender("A"), appender("B"), appender("C"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotOrder != "A,B,C" {
		t.Errorf("want A,B,C, got %q", gotOrder)
	}
}

// Do must honor per-call middleware too, since callers reach for it when
// they have already constructed a request.
func TestClient_DoAppliesPerCallMiddleware(t *testing.T) {
	var gotHeader string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Per-Call")
	})
	c := httpclient.New(nil, s.URL)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req, httpclient.Header("X-Per-Call", "via-do"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotHeader != "via-do" {
		t.Errorf("want via-do, got %q", gotHeader)
	}
}

// Middleware order: first registered runs closest to caller (outermost). We
// verify by having each middleware append to a header in order.
func TestMiddleware_OrderingIsOuterFirst(t *testing.T) {
	var gotOrder string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrder = r.Header.Get("X-Order")
	})
	mw := func(label string) httpclient.Middleware {
		return func(next httpclient.Doer) httpclient.Doer {
			return httpclient.DoerFunc(func(req *http.Request) (*http.Response, error) {
				existing := req.Header.Get("X-Order")
				if existing != "" {
					req.Header.Set("X-Order", existing+","+label)
				} else {
					req.Header.Set("X-Order", label)
				}
				return next.Do(req)
			})
		}
	}

	c := httpclient.New(nil, s.URL, mw("A"), mw("B"), mw("C"))
	resp, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotOrder != "A,B,C" {
		t.Errorf("want A,B,C, got %q", gotOrder)
	}
}

func TestDecodeJSON_ParsesBodyAndClosesIt(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(user{ID: 7, Name: "alice"}); err != nil {
			t.Errorf("encode: %v", err)
		}
	})

	c := httpclient.New(nil, s.URL)
	resp, err := c.Get(context.Background(), "/") //nolint:bodyclose // DecodeJSON closes
	if err != nil {
		t.Fatal(err)
	}

	u, err := httpclient.DecodeJSON[user](resp)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 7 || u.Name != "alice" {
		t.Errorf("unexpected: %+v", u)
	}
}

func TestJSONBody_MarshalsAndReturnsReader(t *testing.T) {
	r := httpclient.JSONBody(map[string]any{"k": 1})
	b, _ := io.ReadAll(r)
	if !strings.Contains(string(b), `"k":1`) {
		t.Errorf("unexpected body: %s", b)
	}
}

func TestJSONBody_PanicsOnUnmarshalable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for unmarshalable input")
		}
	}()
	// channels cannot be marshalled
	httpclient.JSONBody(make(chan int))
}

func TestClient_HeaderMiddlewareSetsStatic(t *testing.T) {
	var gotUA string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
	})

	c := httpclient.New(nil, s.URL, httpclient.Header("User-Agent", "appkit/1"))
	resp, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotUA != "appkit/1" {
		t.Errorf("want appkit/1, got %q", gotUA)
	}
}

// closeTracker records whether the response body was closed so the JSON
// methods, which close it out of sight of the caller, can be held to it.
type closeTracker struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (c closeTracker) Close() error {
	c.closed.Store(true)
	return c.ReadCloser.Close()
}

func trackBodyClose(closed *atomic.Bool) httpclient.Middleware {
	return func(next httpclient.Doer) httpclient.Doer {
		return httpclient.DoerFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := next.Do(req)
			if err != nil {
				return nil, err
			}
			resp.Body = closeTracker{ReadCloser: resp.Body, closed: closed}
			return resp, nil
		})
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func TestClient_GetJSONDecodesBody(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, user{ID: 7, Name: "alice"})
	})
	c := httpclient.New(nil, s.URL)

	u, err := c.GetJSON[user](context.Background(), "/users/7")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 7 || u.Name != "alice" {
		t.Errorf("unexpected: %+v", u)
	}
}

// The JSON verb methods are thin wrappers over the same dispatch path, so one
// table confirms each sends its own method and body and decodes the reply.
func TestClient_JSONVerbsDispatchAndDecode(t *testing.T) {
	cases := []struct {
		name, method, wantBody string
		call                   func(c *httpclient.Client, ctx context.Context, path string) (user, error)
	}{
		{"PostJSON", http.MethodPost, `{"id":1}`, func(c *httpclient.Client, ctx context.Context, path string) (user, error) {
			return c.PostJSON[user](ctx, path, strings.NewReader(`{"id":1}`))
		}},
		{"PutJSON", http.MethodPut, `{"id":2}`, func(c *httpclient.Client, ctx context.Context, path string) (user, error) {
			return c.PutJSON[user](ctx, path, strings.NewReader(`{"id":2}`))
		}},
		{"PatchJSON", http.MethodPatch, `{"id":3}`, func(c *httpclient.Client, ctx context.Context, path string) (user, error) {
			return c.PatchJSON[user](ctx, path, strings.NewReader(`{"id":3}`))
		}},
		{"DeleteJSON", http.MethodDelete, "", func(c *httpclient.Client, ctx context.Context, path string) (user, error) {
			return c.DeleteJSON[user](ctx, path)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotBody string
			s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				writeJSON(t, w, user{ID: 42, Name: "bob"})
			})
			c := httpclient.New(nil, s.URL)

			u, err := tc.call(c, context.Background(), "/users")
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.method {
				t.Errorf("method: want %s, got %s", tc.method, gotMethod)
			}
			if gotBody != tc.wantBody {
				t.Errorf("body: want %q, got %q", tc.wantBody, gotBody)
			}
			if u.ID != 42 || u.Name != "bob" {
				t.Errorf("decoded: %+v", u)
			}
		})
	}
}

func TestClient_GetJSONClosesBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"success", http.StatusOK},
		{"status error", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				writeJSON(t, w, user{ID: 1})
			})

			var closed atomic.Bool
			c := httpclient.New(nil, s.URL, trackBodyClose(&closed))
			if _, err := c.GetJSON[user](context.Background(), "/"); (err != nil) != (tc.status != http.StatusOK) {
				t.Fatalf("unexpected error state: %v", err)
			}
			if !closed.Load() {
				t.Error("response body was not closed")
			}
		})
	}
}

func TestClient_GetJSONReturnsStatusError(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no such user"}`))
	})
	c := httpclient.New(nil, s.URL)

	u, err := c.GetJSON[user](context.Background(), "/users/99")

	var statusErr *httpclient.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("want *StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("code: want 404, got %d", statusErr.StatusCode)
	}
	if statusErr.Status != "404 Not Found" {
		t.Errorf("status: got %q", statusErr.Status)
	}
	if statusErr.Method != http.MethodGet {
		t.Errorf("method: got %q", statusErr.Method)
	}
	if !strings.HasSuffix(statusErr.URL, "/users/99") {
		t.Errorf("url: got %q", statusErr.URL)
	}
	if statusErr.Body != `{"error":"no such user"}` {
		t.Errorf("body: got %q", statusErr.Body)
	}
	if u != (user{}) {
		t.Errorf("want zero value on error, got %+v", u)
	}
}

// A 204, or any 2xx with nothing in the body, is a successful call with no
// object to decode -- not an io.EOF failure the caller has to special-case.
func TestClient_JSONEmptyBodyYieldsZeroValue(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"204 no content", http.StatusNoContent},
		{"200 empty body", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
			c := httpclient.New(nil, s.URL)

			u, err := c.DeleteJSON[user](context.Background(), "/users/1")
			if err != nil {
				t.Fatalf("want nil error, got %v", err)
			}
			if u != (user{}) {
				t.Errorf("want zero value, got %+v", u)
			}
		})
	}
}

func TestClient_GetJSONMalformedBodyReturnsDecodeError(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	})
	c := httpclient.New(nil, s.URL)

	_, err := c.GetJSON[user](context.Background(), "/")
	if err == nil {
		t.Fatal("expected a decode error")
	}
	var statusErr *httpclient.StatusError
	if errors.As(err, &statusErr) {
		t.Errorf("2xx must not produce a StatusError: %v", err)
	}
}

func TestClient_JSONMethodsApplyMiddleware(t *testing.T) {
	var gotAuth, gotHeader string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Per-Call")
		writeJSON(t, w, user{ID: 1})
	})
	c := httpclient.New(nil, s.URL, httpclient.BearerAuth(func() string { return "tok" }))

	if _, err := c.GetJSON[user](context.Background(), "/", httpclient.Header("X-Per-Call", "once")); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("client middleware not applied: %q", gotAuth)
	}
	if gotHeader != "once" {
		t.Errorf("per-call middleware not applied: %q", gotHeader)
	}
}

func TestClient_DoJSONUsesCallerRequest(t *testing.T) {
	var gotMethod, gotHeader string
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Per-Call")
		writeJSON(t, w, user{ID: 5, Name: "carol"})
	})
	c := httpclient.New(nil, s.URL)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, s.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.DoJSON[user](req, httpclient.Header("X-Per-Call", "via-do"))
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodOptions {
		t.Errorf("method: got %s", gotMethod)
	}
	if gotHeader != "via-do" {
		t.Errorf("per-call middleware not applied: %q", gotHeader)
	}
	if u.ID != 5 || u.Name != "carol" {
		t.Errorf("decoded: %+v", u)
	}
}

// An error page can be arbitrarily large; only the leading kilobyte is worth
// carrying into a log line.
func TestStatusError_TruncatesBody(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	})
	c := httpclient.New(nil, s.URL)

	_, err := c.GetJSON[user](context.Background(), "/")
	var statusErr *httpclient.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("want *StatusError, got %v", err)
	}
	if len(statusErr.Body) != 1024 {
		t.Errorf("want 1024 bytes retained, got %d", len(statusErr.Body))
	}
}

// The error is destined for logs, so a password in the base URL must not ride
// along with it.
func TestStatusError_RedactsCredentialsInURL(t *testing.T) {
	s := newEchoServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	c := httpclient.New(nil, "http://user:hunter2@"+strings.TrimPrefix(s.URL, "http://"))

	_, err := c.GetJSON[user](context.Background(), "/secret")
	var statusErr *httpclient.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("want *StatusError, got %v", err)
	}
	if strings.Contains(statusErr.URL, "hunter2") || strings.Contains(statusErr.Error(), "hunter2") {
		t.Errorf("password leaked: %q", statusErr.Error())
	}
}

func TestStatusError_ErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  *httpclient.StatusError
		want string
	}{
		{
			name: "full",
			err: &httpclient.StatusError{
				Method: http.MethodGet, URL: "https://api.test/users/1",
				Status: "404 Not Found", StatusCode: 404, Body: `{"error":"nope"}`,
			},
			want: `httpclient: GET https://api.test/users/1: 404 Not Found: {"error":"nope"}`,
		},
		{
			name: "no request or body",
			err:  &httpclient.StatusError{Status: "500 Internal Server Error", StatusCode: 500},
			want: "httpclient: 500 Internal Server Error",
		},
		{
			name: "status line missing",
			err:  &httpclient.StatusError{StatusCode: 500},
			want: "httpclient: 500",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}
