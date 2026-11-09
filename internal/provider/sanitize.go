package provider

import (
	"encoding/base64"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// maxMessage caps a provider's error message in our errors and logs, in runes.
const maxMessage = 120

var (
	tagRE   = regexp.MustCompile(`<[^>]*>`)
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	codeRE  = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)
)

// normalizeMessage decodes entities, drops tags and e-mail addresses, and collapses whitespace without truncating.
// Providers put account details in messages (AllDebrid's AUTH_BLOCKED names the account's address).
func normalizeMessage(s string) string {
	s = html.UnescapeString(s)
	s = emailRE.ReplaceAllString(s, "[email]")
	s = tagRE.ReplaceAllString(s, "")
	s = emailRE.ReplaceAllString(s, "[email]") // an address split by a tag
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	return s
}

func truncateMessage(s string) string {
	if r := []rune(s); len(r) > maxMessage {
		s = strings.TrimSpace(string(r[:maxMessage-1])) + "…"
	}
	return s
}

// cleanMessage normalizes and bounds a message without credentials.
func cleanMessage(s string) string { return truncateMessage(normalizeMessage(s)) }

func cleanCode(s string) string { return truncateCode(normalizeMessage(s)) }

// truncateCode keeps a redacted, normalized provider error code to a short token.
func truncateCode(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		s = f[0]
	}
	s = codeRE.ReplaceAllString(s, "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// redactMask replaces a credential in provider text, as the gateway's redactor does.
const redactMask = "***"

// RedactSecrets masks each secret (a provider key) in s: as is and URL-escaped in any letter case, and base64 in
// the standard and URL alphabets at any alignment (the part of the encoding that only the secret's bytes decide, so
// "user:key" in a Basic header is caught too). Secrets shorter than 6 characters are skipped, as in the gateway:
// they are not credible keys and masking them would garble ordinary text. Provider error text is decoded after the
// gateway's redaction, so whatever turns it into an error or a log runs it through here first, before cleaning
// truncates it.
func RedactSecrets(s string, secrets ...string) string {
	return NewRedactor(secrets...).Redact(s)
}

// Redactor holds immutable matchers for one client's credentials. It is owned by that runtime generation, never
// cached globally or shared with a client using a different credential.
type Redactor struct {
	plain []*regexp.Regexp
	exact []string
}

// NewRedactor prepares the plaintext, URL and base64 credential variants once, longest plaintext first.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	var plain, exact []string
	for _, k := range secrets {
		if len(k) < 6 {
			continue
		}
		plain = append(plain, k, url.QueryEscape(k), url.PathEscape(k))
		for pad := range 3 {
			b := append(make([]byte, pad), k...)
			start, end := []int{0, 2, 3}[pad], len(b)*8/6
			for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
				if e := enc.EncodeToString(b); end-start >= 6 {
					exact = append(exact, e[start:end])
				}
			}
		}
	}
	sort.SliceStable(plain, func(i, j int) bool { return len(plain[i]) > len(plain[j]) })
	seen := map[string]bool{}
	for _, p := range plain {
		key := strings.ToLower(p)
		if !seen[key] {
			seen[key] = true
			r.plain = append(r.plain, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(p)))
		}
	}
	clear(seen)
	for _, e := range exact {
		if !seen[e] {
			seen[e] = true
			r.exact = append(r.exact, e)
		}
	}
	return r
}

// Redact masks credential values without decoding or truncating the surrounding text.
func (r *Redactor) Redact(s string) string {
	for _, p := range r.plain {
		s = p.ReplaceAllLiteralString(s, redactMask)
	}
	for _, e := range r.exact {
		s = strings.ReplaceAll(s, e, redactMask)
	}
	return s
}

// Sanitize normalizes and redacts both fields before shortening them. Raw redaction also catches credentials
// whose spelling normalization would change (entities, tags or whitespace in the credential itself).
func (r *Redactor) Sanitize(code, message string) (string, string) {
	return truncateCode(r.clean(code)), truncateMessage(r.clean(message))
}

// clean redacts and normalizes s until normalizing changes nothing more, so text that is decoded again later (a
// stored error shown through the journal's cleaner) cannot turn a credential encoded several times back into the
// credential. Text still changing after a few rounds loses its '&', which ends any further decoding.
func (r *Redactor) clean(s string) string {
	s = r.Redact(s)
	for range 4 {
		n := r.Redact(normalizeMessage(s))
		if n == s {
			return s
		}
		s = n
	}
	if normalizeMessage(s) != s {
		s = strings.ReplaceAll(s, "&", "")
	}
	return s
}
