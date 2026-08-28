// Package httpclient wraps [net/http.Client] with a small, context-first API
// and composable middleware. It is intentionally thin: the goal is to reduce
// per-call boilerplate (base URL joining, auth headers, JSON body handling)
// without hiding net/http semantics.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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

// GetJSON issues a GET request and decodes the response into T, collapsing
// the [Client.Get] plus [DecodeJSON] pair into a single call:
//
//	u, err := client.GetJSON[User](ctx, "/users/1")
//
// The body is always closed. Unlike [DecodeJSON], which leaves status
// handling to the caller holding the response, the JSON methods close the
// response and so report a non-2xx status as a [*StatusError]. A 2xx with an
// empty body (204 and friends) yields the zero T and a nil error.
func (c *Client) GetJSON[T any](ctx context.Context, path string, mws ...Middleware) (T, error) {
	return c.doJSON[T](ctx, http.MethodGet, path, nil, mws)
}

// PostJSON issues a POST request with body and decodes the response into T.
// Semantics match [Client.GetJSON]. Use [JSONBody] to send a value as the
// request body.
func (c *Client) PostJSON[T any](ctx context.Context, path string, body io.Reader, mws ...Middleware) (T, error) {
	return c.doJSON[T](ctx, http.MethodPost, path, body, mws)
}

// PutJSON issues a PUT request with body and decodes the response into T.
// Semantics match [Client.GetJSON].
func (c *Client) PutJSON[T any](ctx context.Context, path string, body io.Reader, mws ...Middleware) (T, error) {
	return c.doJSON[T](ctx, http.MethodPut, path, body, mws)
}

// PatchJSON issues a PATCH request with body and decodes the response into T.
// Semantics match [Client.GetJSON].
func (c *Client) PatchJSON[T any](ctx context.Context, path string, body io.Reader, mws ...Middleware) (T, error) {
	return c.doJSON[T](ctx, http.MethodPatch, path, body, mws)
}

// DeleteJSON issues a DELETE request and decodes the response into T.
// Semantics match [Client.GetJSON]; endpoints that answer 204 give the zero T
// and a nil error, so struct{} is a reasonable T when nothing is returned.
func (c *Client) DeleteJSON[T any](ctx context.Context, path string, mws ...Middleware) (T, error) {
	return c.doJSON[T](ctx, http.MethodDelete, path, nil, mws)
}

// DoJSON sends a caller-constructed request through the middleware chain and
// decodes the response into T. Semantics match [Client.GetJSON]; reach for it
// when the verb shortcuts cannot express the request.
func (c *Client) DoJSON[T any](req *http.Request, mws ...Middleware) (T, error) {
	resp, err := wrap(c.doer, mws).Do(req)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeResponse[T](resp)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, mws []Middleware) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, err
	}
	return wrap(c.doer, mws).Do(req)
}

// doJSON dispatches a request and hands the response to decodeResponse. It
// mirrors do, which the non-JSON verb shortcuts share.
func (c *Client) doJSON[T any](ctx context.Context, method, path string, body io.Reader, mws []Middleware) (T, error) {
	resp, err := c.do(ctx, method, path, body, mws)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeResponse[T](resp)
}

// decodeResponse decodes a 2xx JSON body into T. Closing the body stays with
// the caller that obtained the response.
func decodeResponse[T any](r *http.Response) (T, error) {
	var zero T

	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return zero, newStatusError(r)
	}

	var out T
	err := json.NewDecoder(r.Body).Decode(&out)
	if errors.Is(err, io.EOF) {
		// Nothing at all in the body. The zero value is the honest answer
		// for a 204, and reporting io.EOF would push that check onto every
		// caller of an endpoint that sometimes returns no content.
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	return out, nil
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
//
// The status code is left to the caller, who still holds the response.
// [Client.GetJSON] and the other JSON methods are the one-call form and check
// it for you.
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

// maxStatusErrorBody caps how much of a failed response body a [StatusError]
// keeps. The snippet exists to make a log line diagnosable; a full HTML error
// page held in memory (and pasted into every log line) is not worth it.
const maxStatusErrorBody = 1 << 10

// StatusError reports a response outside the 2xx range from one of the JSON
// methods on [Client]. Those methods close the body before returning, so the
// error carries the details a caller would otherwise read off the response:
//
//	u, err := client.GetJSON[User](ctx, "/users/1")
//	var statusErr *httpclient.StatusError
//	if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
//		return ErrNoUser
//	}
type StatusError struct {
	// Method and URL identify the request that was answered. Both are empty
	// when a middleware synthesizes a response without a request attached,
	// and the URL has any password redacted.
	Method string
	URL    string

	// Status is the full status line ("404 Not Found") and StatusCode the
	// numeric part.
	Status     string
	StatusCode int

	// Body is the leading 1 KiB of the response body with surrounding space
	// trimmed, or empty if the body was empty or could not be read.
	Body string
}

// Error returns the request, status, and body snippet, omitting whichever of
// those the response did not carry.
func (e *StatusError) Error() string {
	status := e.Status
	if status == "" {
		status = strconv.Itoa(e.StatusCode)
	}

	var b strings.Builder
	b.WriteString("httpclient: ")
	if e.Method != "" && e.URL != "" {
		b.WriteString(e.Method)
		b.WriteByte(' ')
		b.WriteString(e.URL)
		b.WriteString(": ")
	}
	b.WriteString(status)
	if e.Body != "" {
		b.WriteString(": ")
		b.WriteString(e.Body)
	}
	return b.String()
}

// newStatusError builds a StatusError from a non-2xx response. It reads from
// the body but does not close it; the caller owns that.
func newStatusError(r *http.Response) *StatusError {
	e := &StatusError{Status: r.Status, StatusCode: r.StatusCode}
	if r.Request != nil {
		e.Method = r.Request.Method
		if r.Request.URL != nil {
			e.URL = r.Request.URL.Redacted()
		}
	}
	// A read failure must not mask the status the caller actually needs, so
	// an unreadable body just leaves the snippet empty.
	if body, err := io.ReadAll(io.LimitReader(r.Body, maxStatusErrorBody)); err == nil {
		e.Body = strings.TrimSpace(string(body))
	}
	return e
}
