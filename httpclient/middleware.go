package httpclient

import "net/http"

// DoerFunc lets a plain function satisfy [Doer].
type DoerFunc func(*http.Request) (*http.Response, error)

// Do implements [Doer].
func (f DoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// BearerAuth returns a Middleware that sets `Authorization: Bearer <token>`
// on every outgoing request. Token is resolved per-request via the supplied
// function, so rotation is handled by returning a fresh value; empty strings
// are skipped so anonymous requests pass through.
func BearerAuth(token func() string) Middleware {
	return func(next Doer) Doer {
		return DoerFunc(func(req *http.Request) (*http.Response, error) {
			if t := token(); t != "" {
				req.Header.Set("Authorization", "Bearer "+t)
			}
			return next.Do(req)
		})
	}
}

// JSONContent returns a Middleware that sets `Content-Type: application/json`
// on requests that have a body and do not already specify a content type.
func JSONContent() Middleware {
	return func(next Doer) Doer {
		return DoerFunc(func(req *http.Request) (*http.Response, error) {
			if req.Body != nil && req.Header.Get("Content-Type") == "" {
				req.Header.Set("Content-Type", "application/json")
			}
			return next.Do(req)
		})
	}
}

// Header returns a Middleware that sets a static header on every request.
// Existing values for the same key are replaced.
func Header(key, value string) Middleware {
	return func(next Doer) Doer {
		return DoerFunc(func(req *http.Request) (*http.Response, error) {
			req.Header.Set(key, value)
			return next.Do(req)
		})
	}
}
