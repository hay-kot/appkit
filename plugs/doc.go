// Package plugs provides a small service lifecycle manager. Plugins are long-
// running functions that block until their context is cancelled. The Manager
// starts them concurrently, captures panics, optionally retries transient
// failures, and coordinates graceful shutdown on signals or explicit Shutdown
// calls.
//
// Plugins should do construction-time wiring in their constructor and use Start
// only to run the main loop. Startup order is not deterministic; any state a
// plugin exposes to its peers must be safe to use before Start begins blocking.
package plugs
