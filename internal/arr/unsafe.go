package arr

// UnsafeValue reports whether s looks like a URL or carries a key, passkey, token or magnet: the test SanitizeHistory
// drops values by. The grab journal masks free text with it.
func UnsafeValue(s string) bool { return unsafeValue(s) }
