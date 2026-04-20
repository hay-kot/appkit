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

// Secret is a string type that resolves secret values at unmarshal time.
// Values with a known prefix (e.g. "env:<VAR>", "file:<path>") are resolved
// via the matching [Resolver]; values with an unknown prefix or no prefix are
// used as literals, so connection strings like "postgres://user:pw@host/db"
// pass through unchanged.
//
// Marshaling always emits "[redacted]" to prevent secret leakage in logs or
// serialized config output. Resolution errors surface during unmarshal so
// misconfigured secrets fail fast at startup.
//
// Built-in prefixes are "env" and "file"; additional sources can be added via
// [Register].
type Secret string

// Value returns the resolved secret string.
func (s Secret) Value() string { return string(s) }

// String implements fmt.Stringer and always returns "[redacted]".
func (s Secret) String() string { return redacted }

// MarshalText implements encoding.TextMarshaler. Always emits "[redacted]".
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. Resolves registered
// prefixes immediately so misconfigured secrets fail at config load time.
func (s *Secret) UnmarshalText(b []byte) error {
	val, err := resolve(string(b))
	if err != nil {
		return err
	}
	*s = Secret(val)
	return nil
}

// MarshalJSON implements json.Marshaler. Always emits `"[redacted]"`.
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler. Resolves registered prefixes
// immediately so misconfigured secrets fail at config load time.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return fmt.Errorf("secret: expected JSON string: %w", err)
	}
	val, err := resolve(str)
	if err != nil {
		return err
	}
	*s = Secret(val)
	return nil
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

func resolve(raw string) (string, error) {
	prefix, rest, ok := strings.Cut(raw, ":")
	if !ok || prefix == "" {
		return raw, nil
	}
	fn, ok := sources[prefix]
	if !ok {
		return raw, nil
	}
	return fn(rest)
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
