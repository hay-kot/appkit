package secret_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hay-kot/appkit/secret"
)

func TestSecret_LiteralValue(t *testing.T) {
	var s secret.Secret
	if err := s.UnmarshalText([]byte("plain-value")); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "plain-value" {
		t.Errorf("want plain-value, got %q", s.Value())
	}
}

func TestSecret_EnvPrefix(t *testing.T) {
	t.Setenv("TEST_SECRET_VAR", "from-env")
	var s secret.Secret
	if err := s.UnmarshalText([]byte("env:TEST_SECRET_VAR")); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "from-env" {
		t.Errorf("want from-env, got %q", s.Value())
	}
}

func TestSecret_EnvMissingReturnsError(t *testing.T) {
	_ = os.Unsetenv("DEFINITELY_NOT_SET_XYZ")
	var s secret.Secret
	err := s.UnmarshalText([]byte("env:DEFINITELY_NOT_SET_XYZ"))
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
	if !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_XYZ") {
		t.Errorf("error should name missing var: %v", err)
	}
}

func TestSecret_FilePrefixTrimsTrailingNewlines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("file-value\n\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var s secret.Secret
	if err := s.UnmarshalText([]byte("file:" + path)); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "file-value" {
		t.Errorf("want file-value, got %q", s.Value())
	}
}

func TestSecret_FileMissingReturnsError(t *testing.T) {
	var s secret.Secret
	err := s.UnmarshalText([]byte("file:/nonexistent/secret"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSecret_StringAlwaysRedacted(t *testing.T) {
	s := secret.Secret("super-secret-value")
	if got := s.String(); got != "[redacted]" {
		t.Errorf("want [redacted], got %q", got)
	}
}

func TestSecret_MarshalTextRedacts(t *testing.T) {
	s := secret.Secret("super-secret-value")
	b, err := s.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[redacted]" {
		t.Errorf("want [redacted], got %q", string(b))
	}
}

func TestSecret_MarshalJSONRedacts(t *testing.T) {
	s := secret.Secret("super-secret-value")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"[redacted]"` {
		t.Errorf("want \"[redacted]\", got %s", string(b))
	}
}

func TestSecret_UnmarshalJSONLiteral(t *testing.T) {
	var s secret.Secret
	if err := json.Unmarshal([]byte(`"literal-v"`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "literal-v" {
		t.Errorf("want literal-v, got %q", s.Value())
	}
}

func TestSecret_UnmarshalJSONEnv(t *testing.T) {
	t.Setenv("SECRET_JSON_VAR", "json-env-value")
	var s secret.Secret
	if err := json.Unmarshal([]byte(`"env:SECRET_JSON_VAR"`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "json-env-value" {
		t.Errorf("want json-env-value, got %q", s.Value())
	}
}

func TestSecret_UnmarshalJSONNonStringReturnsError(t *testing.T) {
	var s secret.Secret
	if err := json.Unmarshal([]byte(`42`), &s); err == nil {
		t.Fatal("expected error for non-string JSON")
	}
}

// JSON strings with escape sequences must be unescaped before resolution so
// values containing quotes, backslashes, or unicode escapes round-trip
// correctly.
func TestSecret_UnmarshalJSONUnescapesBeforeResolving(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"escaped quote", `"pass\"word"`, `pass"word`},
		{"unicode escape", `"\u0041BC"`, "ABC"},
		{"escaped backslash", `"a\\b"`, `a\b`},
		{"escaped newline", `"a\nb"`, "a\nb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s secret.Secret
			if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
				t.Fatal(err)
			}
			if s.Value() != tc.want {
				t.Errorf("want %q, got %q", tc.want, s.Value())
			}
		})
	}
}

// Env lookups must use the unescaped variable name, not the raw JSON bytes.
func TestSecret_UnmarshalJSONEnvWithEscapes(t *testing.T) {
	t.Setenv("ESCAPED_NAME", "resolved")
	var s secret.Secret
	// "env:\u0045SCAPED_NAME" unescapes to "env:ESCAPED_NAME".
	if err := json.Unmarshal([]byte(`"env:\u0045SCAPED_NAME"`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "resolved" {
		t.Errorf("want resolved, got %q", s.Value())
	}
}

// Values with an unknown prefix (or no prefix at all) must pass through
// unchanged so connection strings like postgres://user:pw@host/db work.
func TestSecret_UnknownPrefixPassesThrough(t *testing.T) {
	cases := []string{
		"postgres://user:pw@host/db",
		"no-prefix-here",
		"unknown:something",
		":leading-colon",
	}
	for _, in := range cases {
		var s secret.Secret
		if err := s.UnmarshalText([]byte(in)); err != nil {
			t.Errorf("%q: unexpected error: %v", in, err)
			continue
		}
		if s.Value() != in {
			t.Errorf("%q: want unchanged, got %q", in, s.Value())
		}
	}
}

func TestRegister_AddsCustomSource(t *testing.T) {
	t.Cleanup(secret.ResetSources)
	secret.Register("testreg", func(v string) (string, error) {
		return "resolved-" + v, nil
	})

	var s secret.Secret
	if err := s.UnmarshalText([]byte("testreg:hello")); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "resolved-hello" {
		t.Errorf("want resolved-hello, got %q", s.Value())
	}
}

func TestRegister_PanicsOnEmptyPrefix(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for empty prefix")
		}
	}()
	secret.Register("", func(string) (string, error) { return "", nil })
}

func TestRegister_PanicsOnNilResolver(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil resolver")
		}
	}()
	secret.Register("someprefix", nil)
}

func TestRegister_PanicsOnDuplicatePrefix(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for duplicate prefix")
		}
	}()
	// "env" is built-in, so registering it again must panic.
	secret.Register("env", func(string) (string, error) { return "", nil })
}

func TestRegister_ResolverErrorsPropagate(t *testing.T) {
	t.Cleanup(secret.ResetSources)
	secret.Register("failsource", func(string) (string, error) {
		return "", errors.New("source down")
	})

	var s secret.Secret
	err := s.UnmarshalText([]byte("failsource:any"))
	if err == nil {
		t.Fatal("expected error from resolver")
	}
	if !strings.Contains(err.Error(), "source down") {
		t.Errorf("want resolver error, got %v", err)
	}
}

// Embedded-in-struct test to confirm the type works with stdlib json decoding
// of real config shapes.
func TestSecret_NestedInStruct(t *testing.T) {
	t.Setenv("NESTED_KEY", "sk_live_xyz")
	type Config struct {
		APIKey secret.Secret `json:"api_key"`
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"api_key":"env:NESTED_KEY"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey.Value() != "sk_live_xyz" {
		t.Errorf("want sk_live_xyz, got %q", cfg.APIKey.Value())
	}
	// Round-trip should redact.
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"[redacted]"`) {
		t.Errorf("round-trip did not redact: %s", b)
	}
}
