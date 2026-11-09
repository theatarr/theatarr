package provider

// CleanMessage and CleanCode are this package's sanitisers for provider error text (see cleanMessage), shared with
// the write capability in internal/provider/adder so its errors and logs follow the same rules.
func CleanMessage(s string) string { return cleanMessage(s) }

// CleanCode keeps a provider error code to a short token.
func CleanCode(s string) string { return cleanCode(s) }
