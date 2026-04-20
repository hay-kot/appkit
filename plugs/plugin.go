package plugs

import "context"

// Plugin is a long-running component managed by [Manager]. Start should block
// until ctx is cancelled and then perform any cleanup. Returning nil on clean
// shutdown is the conventional form; returning ctx.Err() is also accepted and
// treated as a clean shutdown.
//
// Startup order between plugins is not guaranteed. Any state that other
// plugins rely on must be set up in the plugin's constructor, not Start.
type Plugin interface {
	// Name returns a short identifier used in logs and error messages.
	Name() string

	// Start runs the plugin's main loop. It should block until ctx is
	// cancelled. Returning an error triggers manager shutdown and aggregates
	// into the manager's final error.
	Start(ctx context.Context) error
}

// PluginFunc adapts a plain function into a [Plugin] with the given name.
func PluginFunc(name string, start func(ctx context.Context) error) Plugin {
	return &pluginFunc{name: name, start: start}
}

type pluginFunc struct {
	name  string
	start func(ctx context.Context) error
}

func (p *pluginFunc) Name() string                    { return p.name }
func (p *pluginFunc) Start(ctx context.Context) error { return p.start(ctx) }
