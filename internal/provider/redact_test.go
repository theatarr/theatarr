package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// echoes are the forms of key a provider could put in its error text: raw, in another case, URL-escaped and base64.
func echoes(key string) []string {
	return []string{key, strings.ToLower(key), url.QueryEscape(key + "/"), base64.StdEncoding.EncodeToString([]byte(key)),
		base64.RawURLEncoding.EncodeToString([]byte(key))}
}

// A provider's error text can echo the key it was sent. The key is masked before the text is cleaned (so the cap on
// a message's length cannot cut it into a prefix that slips through) and before it reaches an error.
func TestProviderErrorsRedactTheKey(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		prov, key, host string
		body            func(echo string) (int, any)
	}{
		{"tb", keyTB, "tb.test", func(e string) (int, any) {
			return 401, map[string]any{"success": false, "error": "BAD_TOKEN", "detail": "invalid token " + e}
		}},
		{"tb", keyTB, "tb.test", func(e string) (int, any) {
			return 200, map[string]any{"success": false, "error": "BAD_TOKEN",
				"detail": strings.Repeat("x", 100) + " " + e}
		}},
		{"ad", keyAD, "ad.test", func(e string) (int, any) {
			return 200, map[string]any{"status": "error", "error": map[string]any{"code": "AUTH_BAD_APIKEY:" + e,
				"message": "The auth apikey " + e + " is invalid"}}
		}},
		{"ad", keyAD, "ad.test", func(e string) (int, any) {
			return 400, map[string]any{"status": "error", "error": map[string]any{"code": "BAD", "message": e}}
		}},
		{"rd", keyRD, "rd.test", func(e string) (int, any) {
			return 401, map[string]any{"error": e, "error_code": 8}
		}},
	} {
		for _, echo := range echoes(c.key) {
			f := newFakeNet(t)
			f.serve(c.host, c.key, func(w http.ResponseWriter, r *http.Request) {
				status, body := c.body(echo)
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(body)
			})
			_, err := f.client(c.prov, c.key).Account(ctx)
			if err == nil {
				t.Fatalf("%s: want an error", c.prov)
			}
			msg := fmt.Sprintf("%v %+v", err, err)
			for _, part := range []string{echo, echo[:12], echo[len(echo)-12:]} {
				if strings.Contains(msg, part) {
					t.Errorf("%s: error %q holds %q (echo %q)", c.prov, msg, part, echo)
				}
			}
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	key := "Abc-123/def+456=="
	for _, e := range []string{key, strings.ToUpper(key), url.QueryEscape(key), url.PathEscape(key),
		base64.StdEncoding.EncodeToString([]byte(key)), base64.RawStdEncoding.EncodeToString([]byte(key)),
		base64.URLEncoding.EncodeToString([]byte(key)), base64.RawURLEncoding.EncodeToString([]byte(key)),
		base64.StdEncoding.EncodeToString([]byte(":" + key))} {
		if got := RedactSecrets("x "+e+" y", key); strings.Contains(got, e) || !strings.HasPrefix(got, "x ") {
			t.Errorf("RedactSecrets(%q) = %q", e, got)
		}
	}
	if got := RedactSecrets("short abc", "abc"); got != "short abc" {
		t.Errorf("a key under 6 characters is not a credible key: %q", got)
	}
}
