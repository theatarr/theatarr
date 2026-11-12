package policy

// ResolveRoot follows symlinks and records the directory's identity when it exists.
func ResolveRoot(path string) (ResolvedRoot, error) { return resolveRoot(path, nil) }
