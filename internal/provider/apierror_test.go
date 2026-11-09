package provider

import (
	"fmt"
	"strings"
	"testing"
)

// A credential the provider's text carries entity-encoded twice survives one decode; Error must not decode the
// sanitised message again, and the stored text must stay clean when a later reader (the journal's cleaner) decodes
// it once more.
func TestAPIErrorNeverDecodesSanitizedMessageAgain(t *testing.T) {
	const key = "SECRETKEY123"
	for _, depth := range []int{1, 2, 3, 6} {
		enc := key
		for range depth {
			var b strings.Builder
			for _, r := range enc {
				fmt.Fprintf(&b, "&#%d;", r)
			}
			enc = b.String()
		}
		enc = strings.ReplaceAll(enc, "&", "&amp;") // one more layer in the usual spelling
		e := &APIError{Provider: "rd", Status: 400, Code: "bad_token", Message: "bad token " + enc}
		scrub(e, NewRedactor(key))
		for what, s := range map[string]string{"stored": e.Message, "Error": e.Error(),
			"decoded again": CleanMessage(e.Error())} {
			if strings.Contains(s, key) {
				t.Fatalf("depth %d: %s text holds the key: %q", depth, what, s)
			}
		}
		if !strings.HasPrefix(e.Error(), "rd: HTTP 400 bad_token: bad token") {
			t.Fatalf("depth %d: Error %q", depth, e.Error())
		}
	}
}
