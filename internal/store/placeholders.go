package store

import "strings"

// Placeholders returns n positional SQL parameters for an IN list. Callers handle empty lists before querying.
func Placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
