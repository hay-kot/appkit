package plugs_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hay-kot/appkit/plugs"
)

func TestPluginErrors_ErrorStringSingle(t *testing.T) {
	err := &plugs.PluginErrors{Errors: []error{errors.New("boom")}}
	if got := err.Error(); got != "boom" {
		t.Errorf("want %q, got %q", "boom", got)
	}
}

func TestPluginErrors_ErrorStringMultiple(t *testing.T) {
	err := &plugs.PluginErrors{Errors: []error{
		errors.New("a failed"),
		errors.New("b failed"),
	}}
	got := err.Error()
	for _, want := range []string{"2 plugin errors", "a failed", "b failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("want substring %q in %q", want, got)
		}
	}
}

func TestPluginErrors_ErrorStringEmpty(t *testing.T) {
	err := &plugs.PluginErrors{}
	if got := err.Error(); got != "plugs: no errors" {
		t.Errorf("unexpected empty message: %q", got)
	}
}

// errors.Is must traverse PluginErrors via Unwrap() []error so callers can
// check for sentinel errors anywhere in the aggregate.
func TestPluginErrors_IsTraversesUnwrap(t *testing.T) {
	pe := &plugs.PluginErrors{Errors: []error{
		errors.New("other"),
		io.EOF,
	}}
	if !errors.Is(pe, io.EOF) {
		t.Fatalf("errors.Is should find io.EOF inside PluginErrors")
	}
}

// errors.As must reach nested typed errors.
func TestPluginErrors_AsFindsPanicError(t *testing.T) {
	pe := &plugs.PluginErrors{Errors: []error{
		&plugs.PanicError{Name: "http", Value: "kaboom"},
	}}
	var panicErr *plugs.PanicError
	if !errors.As(pe, &panicErr) {
		t.Fatal("errors.As should find *PanicError")
	}
	if panicErr.Name != "http" {
		t.Errorf("want http, got %q", panicErr.Name)
	}
}

func TestPanicError_ErrorFormat(t *testing.T) {
	e := &plugs.PanicError{Name: "queue", Value: "nil map"}
	got := e.Error()
	for _, want := range []string{"queue", "panicked", "nil map"} {
		if !strings.Contains(got, want) {
			t.Errorf("want substring %q in %q", want, got)
		}
	}
}

func TestDefaultBackoff_ExponentialCapped(t *testing.T) {
	// Each call should double until 5s cap.
	prev := plugs.DefaultBackoff(0)
	if prev != 100_000_000 { // 100ms in ns
		t.Errorf("attempt 0: want 100ms, got %v", prev)
	}
	for i := 1; i < 10; i++ {
		d := plugs.DefaultBackoff(i)
		if d < prev && d != 5_000_000_000 {
			t.Errorf("attempt %d: duration decreased from %v to %v", i, prev, d)
		}
		if d > 5_000_000_000 {
			t.Errorf("attempt %d: exceeded 5s cap: %v", i, d)
		}
		prev = d
	}
}

func TestDefaultBackoff_NegativeReturnsZero(t *testing.T) {
	if d := plugs.DefaultBackoff(-1); d != 0 {
		t.Errorf("want 0 for negative attempt, got %v", d)
	}
}
