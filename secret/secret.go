// Package secret provides [Secret], a string type that resolves secret
// values from environment variables, files, or any caller-registered source
// during unmarshaling.
package secret

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const redacted = "[redacted]"

// Secret holds a resolved secret value together with the source reference it
// was resolved from. Values with a registered prefix (e.g. "env:<VAR>",
// "file:<path>") are resolved via the matching [Resolver]; values with an
// unknown prefix or no prefix are used as literals, so connection strings like
// "postgres://user:pw@host/db" pass through unchanged.
//
// Marshaling emits the source reference ("env:MY_ENV"), never the resolved
// value, so config round-trips without either leaking the secret or losing the
// reference. Literal values have no reference to emit -- the input is the
// secret itself -- so they marshal as "[redacted]".
//
// Resolution errors surface during unmarshal so misconfigured secrets fail
// fast at startup.
//
// Built-in prefixes are "env" and "file"; additional sources can be added via
// [Register].
type Secret struct {
	// ref is the original input when it named a registered source. It stays
	// empty for literals, where the input is the secret itself.
	ref   string
	value string
}

// New returns a Secret holding an already-resolved literal value. The value
// has no source reference, so it is redacted whenever the Secret is printed or
// marshaled.
func New(value string) Secret {
	return Secret{value: value}
}

// Resolve resolves raw through the registered sources and returns a Secret
// that remembers raw as its reference. Input with an unknown prefix or no
// prefix is kept as a literal value with no reference.
//
// Resolve performs the source lookup, so it reads the environment or the
// filesystem for the built-in prefixes.
func Resolve(raw string) (Secret, error) {
	prefix, rest, ok := strings.Cut(raw, ":")
	if !ok || prefix == "" {
		return Secret{value: raw}, nil
	}

	fn, ok := sources[prefix]
	if !ok {
		return Secret{value: raw}, nil
	}

	val, err := fn(rest)
	if err != nil {
		return Secret{}, err
	}
	return Secret{ref: raw, value: val}, nil
}

// Value returns the resolved secret string.
func (s Secret) Value() string { return s.value }

// Ref returns the source reference the Secret was resolved from, such as
// "env:MY_ENV". It is empty for literal values.
func (s Secret) Ref() string { return s.ref }

// String implements fmt.Stringer. It returns the source reference when the
// Secret has one and "[redacted]" otherwise, so the resolved value never
// reaches a log line.
func (s Secret) String() string { return s.text() }

// MarshalText implements encoding.TextMarshaler. It emits the source
// reference, or "[redacted]" for literal values.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.text()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. Resolves registered
// prefixes immediately so misconfigured secrets fail at config load time.
func (s *Secret) UnmarshalText(b []byte) error {
	resolved, err := Resolve(string(b))
	if err != nil {
		return err
	}
	*s = resolved
	return nil
}

// MarshalJSON implements json.Marshaler. It emits the source reference, or
// `"[redacted]"` for literal values.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.text())
}

// UnmarshalJSON implements json.Unmarshaler. Resolves registered prefixes
// immediately so misconfigured secrets fail at config load time.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return fmt.Errorf("secret: expected JSON string: %w", err)
	}
	resolved, err := Resolve(str)
	if err != nil {
		return err
	}
	*s = resolved
	return nil
}

// text is the only representation of a Secret that leaves the package. An
// empty Secret renders as an empty string rather than "[redacted]" so an
// absent config field does not round-trip into a literal "[redacted]" secret.
func (s Secret) text() string {
	switch {
	case s.ref != "":
		return s.ref
	case s.value != "":
		return redacted
	default:
		return ""
	}
}

// Resolver converts the portion of a secret value after its prefix and colon
// into the resolved secret. For input "vault:kv/data/app", the Resolver
// registered under "vault" receives "kv/data/app".
type Resolver func(value string) (string, error)

// sources holds registered prefixes.
//
// Intentionally unsynchronised: [Register] is documented as init-only, and Go
// guarantees init functions run sequentially before main. That ordering
// happens-before any secret resolution, so reads from runtime goroutines are
// safe without a lock.
var sources = map[string]Resolver{
	"env":  resolveEnv,
	"file": resolveFile,
}

// Register adds prefix as a recognized secret source.
//
// Register MUST be called during package init()  or otherwise before any [Secret] is
// unmarshaled. Calling Register from a goroutine, or after secrets have begun
// resolving, is a data race.
//
// Panics if prefix is empty, fn is nil, or prefix is already registered — so
// misuse fails loudly at startup rather than silently.
func Register(prefix string, fn Resolver) {
	if prefix == "" {
		panic("secret: Register called with empty prefix")
	}
	if fn == nil {
		panic("secret: Register called with nil Resolver")
	}
	if _, exists := sources[prefix]; exists {
		panic(fmt.Sprintf("secret: prefix %q already registered", prefix))
	}
	sources[prefix] = fn
}

func resolveEnv(name string) (string, error) {
	val, set := os.LookupEnv(name)
	if !set {
		return "", fmt.Errorf("secret env var %q is not set", name)
	}
	return val, nil
}

func resolveFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secret file %q: %w", path, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
