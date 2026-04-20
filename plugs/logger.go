package plugs

// Logger is the structured logging contract required by appkit packages that
// emit logs. Key-value pairs follow the slog convention: alternating string
// keys and typed values.
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}

// Nop returns a Logger that discards all messages. Use as a safe default when
// no logger is configured.
func Nop() Logger { return nopLogger{} }

type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}
