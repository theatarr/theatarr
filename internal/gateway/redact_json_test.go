package gateway

import (
	"strings"
	"testing"
)

// A Cardigann credential is a JSON object of secret settings: each value is masked on its own (a definition can put
// a passkey in a path), not only the whole object.
func TestRedactorMasksJSONCredentialValues(t *testing.T) {
	r := newRedactor(testPolicy(), []string{`{"username":"alice-user","passkey":"0123456789abcdef","pin":"12"}`})
	got := r.String("GET http://site.test/rss/0123456789abcdef/feed failed for alice-user (pin 12)")
	if strings.Contains(got, "0123456789abcdef") || strings.Contains(got, "alice-user") {
		t.Errorf("credential values left in %q", got)
	}
	if !strings.Contains(got, "pin 12") {
		t.Errorf("a value under 6 characters was masked: %q", got)
	}
}
