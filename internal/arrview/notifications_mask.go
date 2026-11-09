package arrview

import (
	"strings"

	"github.com/theatarr/theatarr/internal/secrets"
)

// Webhook credentials besides the password field. The arrs return the URL and headers as stored; we mask what is a
// credential there too, and a client that sends the masks back (GET, change a flag, PUT) keeps the stored values:
//   - the URL's user information: the password, or a user name with no password (https://<token>@host);
//   - query values under a credential-like key (token, apikey, sig, ...) or that look like a token;
//   - path segments that look like a token (Discord /api/webhooks/<id>/<token>, Slack /services/T/B/<token>);
//   - header values under a credential-like name (Authorization, Cookie, X-Api-Key, ...) or that look like a token.
//
// A token looks like at least 16 characters of letters and digits (and URL-safe punctuation), with both letters
// and digits. Stored values are put back only while the URL keeps its scheme and host: masks sent along with a
// URL pointing elsewhere stay masks, so a client cannot redirect a stored secret to another host.

var notificationURLs = secrets.URLMasker{}

func secretValue(name, value string) bool { return secrets.CredentialValue(name, value) }
func maskURL(raw string) string           { return notificationURLs.Mask(raw) }
func unmaskURL(in, old string) string     { return notificationURLs.Unmask(in, old) }

// maskHeaders masks the credential values of a headers field ([{key, value}]).
func maskHeaders(v any) any {
	hs, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(hs))
	for _, h := range hs {
		m, ok := h.(map[string]any)
		if !ok {
			out = append(out, h)
			continue
		}
		c := map[string]any{}
		for k, x := range m {
			c[k] = x
		}
		k, _ := c["key"].(string)
		if val, _ := c["value"].(string); secretValue(k, val) {
			c["value"] = maskedValue
		}
		out = append(out, c)
	}
	return out
}

// unmaskHeaders puts the stored value back for each header sent with the mask, by header name.
func unmaskHeaders(v, old any) {
	hs, _ := v.([]any)
	olds, _ := old.([]any)
	for _, h := range hs {
		m, ok := h.(map[string]any)
		if !ok || m["value"] != maskedValue {
			continue
		}
		k, _ := m["key"].(string)
		for _, oh := range olds {
			if om, ok := oh.(map[string]any); ok {
				if ok2, _ := om["key"].(string); strings.EqualFold(ok2, k) {
					m["value"] = om["value"]
					break
				}
			}
		}
	}
}
