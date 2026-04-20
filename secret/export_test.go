package secret

// ResetSources restores the registry to only the built-in resolvers. Tests
// that call Register use this via t.Cleanup so registrations do not leak
// across test runs.
func ResetSources() {
	sources = map[string]Resolver{
		"env":  resolveEnv,
		"file": resolveFile,
	}
}
