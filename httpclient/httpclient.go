// Package httpclient wraps [net/http.Client] with a small, context-first API
// and composable middleware. It is intentionally thin: the goal is to reduce
// per-call boilerplate (base URL joining, auth headers, JSON body handling)
// without hiding net/http semantics.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Doer is the minimal interface needed to dispatch an HTTP request. Both
// [*http.Client] and middleware wrappers satisfy it.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Middleware wraps a Doer to inject behavior (auth, logging, retries, ...) on
// every request passing through it. Middlewares compose by wrapping: the first
// registered runs closest to the outer caller.
type Middleware func(Doer) Doer

// Client is an HTTP client with a base URL, a middleware chain, and shortcut
// methods for common verbs. It is safe for concurrent use once configured.
type Client struct {
	base string
	doer Doer
}

// New constructs a Client dispatching via the given http.Client (nil means
// [http.DefaultClient]) with baseURL prepended to relative request paths.
// Middlewares are applied in the order given; the first runs closest to the
// caller (outermost wrapper).
func New(httpClient *http.Client, baseURL string, mws ...Middleware) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		doer: wrap(httpClient, mws),
	}
}

// Do sends req through the client's middleware chain, wrapped on the outside
// by any per-call middleware. Per-call middleware registered first runs
// closest to the caller, and all per-call middleware run before the chain
// configured on the client.
func (c *Client) Do(req *http.Request, mws ...Middleware) (*http.Response, error) {
	return wrap(c.doer, mws).Do(req)
}

// Get issues a GET request to path (joined with the base URL if relative).
// Per-call middleware follows the same ordering rules as [Client.Do].
func (c *Client) Get(ctx context.Context, path string, mws ...Middleware) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, path, nil, mws)
}

// Post issues a POST request with body.
func (c *Client) Post(ctx context.Context, path string, body io.Reader, mws ...Middleware) (*http.Response, error) {
	return c.do(ctx, http.MethodPost, path, body, mws)
}

// Put issues a PUT request with body.
func (c *Client) Put(ctx context.Context, path string, body io.Reader, mws ...Middleware) (*http.Response, error) {
	return c.do(ctx, http.MethodPut, path, body, mws)
}

// Patch issues a PATCH request with body.
func (c *Client) Patch(ctx context.Context, path string, body io.Reader, mws ...Middleware) (*http.Response, error) {
	return c.do(ctx, http.MethodPatch, path, body, mws)
}

// Delete issues a DELETE request.
func (c *Client) Delete(ctx context.Context, path string, mws ...Middleware) (*http.Response, error) {
	return c.do(ctx, http.MethodDelete, path, nil, mws)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, mws []Middleware) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, err
	}
	return wrap(c.doer, mws).Do(req)
}

// wrap applies mws to base in registration order: the first middleware is
// the outermost wrapper and runs closest to the caller. Returns base
// unchanged when mws is empty so the common zero-middleware case allocates
// nothing.
func wrap(base Doer, mws []Middleware) Doer {
	for i := len(mws) - 1; i >= 0; i-- {
		base = mws[i](base)
	}
	return base
}

// url joins the client's base with a request path. Absolute URLs (http://...
// or https://..., case-insensitive per RFC 3986) bypass the base so callers
// can occasionally reach other hosts without constructing a separate client.
func (c *Client) url(path string) string {
	if hasSchemePrefix(path, "http://") || hasSchemePrefix(path, "https://") {
		return path
	}
	if path == "" {
		return c.base
	}
	return c.base + "/" + strings.TrimLeft(path, "/")
}

// hasSchemePrefix reports whether s starts with scheme using ASCII
// case-insensitive matching, since URL schemes are case-insensitive.
func hasSchemePrefix(s, scheme string) bool {
	return len(s) >= len(scheme) && strings.EqualFold(s[:len(scheme)], scheme)
}

// DecodeJSON decodes r.Body into a fresh T and closes the body. Use as:
//
//	resp, err := client.Get(ctx, "/users/1")
//	if err != nil { return err }
//	u, err := httpclient.DecodeJSON[User](resp)
func DecodeJSON[T any](r *http.Response) (T, error) {
	var out T
	defer func() { _ = r.Body.Close() }()
	err := json.NewDecoder(r.Body).Decode(&out)
	return out, err
}

// JSONBody marshals v and returns a reader suitable for the body parameter of
// Post/Put/Patch. Panics if v cannot be marshalled; marshal failures here
// typically indicate a programming bug, not a runtime condition.
func JSONBody(v any) *bytes.Reader {
	data, err := json.Marshal(v)
	if err != nil {
		panic("httpclient: failed to marshal JSON body: " + err.Error())
	}
	return bytes.NewReader(data)
}
