package httpclient_test

import (
	"context"
	"encoding/json"
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
