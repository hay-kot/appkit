package plugs

import (
	"errors"
	"fmt"
	"strings"
)

// ErrManagerAlreadyStarted is returned by [Manager.Start] if called more than
// once concurrently.
var ErrManagerAlreadyStarted = errors.New("plugs: manager already started")

// PluginErrors aggregates terminal errors from one or more plugins. It
// implements error and supports errors.Is / errors.As via Unwrap.
//
// Callers who want the individual errors can type-assert:
//
//	var pe *plugs.PluginErrors
//	if errors.As(err, &pe) {
//	    for _, e := range pe.Errors { ... }
//	}
type PluginErrors struct {
	Errors []error
}

func (e *PluginErrors) Error() string {
	switch len(e.Errors) {
	case 0:
		return "plugs: no errors"
	case 1:
		return e.Errors[0].Error()
	}
	parts := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		parts[i] = err.Error()
	}
	return fmt.Sprintf("plugs: %d plugin errors: %s", len(e.Errors), strings.Join(parts, "; "))
}

// Unwrap exposes the contained errors for errors.Is / errors.As traversal.
func (e *PluginErrors) Unwrap() []error { return e.Errors }

// PanicError is returned when a plugin's Start panics. The recovered value is
// preserved for diagnostics.
type PanicError struct {
	Name  string
	Value any
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("plugs: plugin %q panicked: %v", e.Name, e.Value)
}
