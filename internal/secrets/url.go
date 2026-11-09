// Package secrets shares credential display and restoration rules without holding stored credentials.
package secrets

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// URLMasker selects one of the existing display policies: Whole keeps only the scheme and host, otherwise only
// credential-like URL parts are masked. Unmask restores values only from the corresponding stored URL.
type URLMasker struct{ Whole bool }

const tokenMask = "********"

func maskWholeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if raw != "" {
			return "[redacted]"
		}
		return raw
	}
	if u.Path == "" && u.RawQuery == "" && u.User == nil {
		return raw
	}
	return u.Scheme + "://" + u.Host + "/[redacted]"
}

var secretName = regexp.MustCompile(`(?i)token|key|secret|pass|pwd|auth|sig|session|cred|cookie|code`)

// tokenLike reports whether s looks like a generated credential.
func tokenLike(s string) bool {
	if len(s) < 16 {
		return false
	}
	var letter, digit bool
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		case c >= '0' && c <= '9':
			digit = true
		case strings.ContainsRune("-_.~+/=", c):
		default:
			return false
		}
	}
	return letter && digit
}

// CredentialValue reports whether a named value (a query parameter, a header) looks like a credential: its name
// (CredentialQueryName or a credential-like word) or a generated-looking value.
func CredentialValue(name, value string) bool {
	return value != "" && (CredentialQueryName(name) || secretName.MatchString(name) || tokenLike(value))
}

// credentialNames are query parameter names that carry a credential, lower case without separators; credentialEnds
// are endings that do (x-plex-token, apiKey, rss_key).
var (
	credentialNames = []string{"key", "auth", "pass", "pwd", "sig", "signature", "session", "sid", "cookie"}
	credentialEnds  = []string{"apikey", "passkey", "authkey", "rsskey", "token", "secret", "password", "passwd"}
)

// CredentialQueryName reports whether a URL query parameter of this name carries a credential, compared without case
// and separators. Policy validation refuses such parameters in configured URLs (the *_env fields hold credentials);
// the masker masks them.
func CredentialQueryName(name string) bool {
	n := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(name))
	if n == "" {
		return false
	}
	if slices.Contains(credentialNames, n) {
		return true
	}
	for _, end := range credentialEnds {
		if strings.HasSuffix(n, end) {
			return true
		}
	}
	return false
}

// Masked reports whether s carries this masker's mask: a value read back masked that was not restored.
func (m URLMasker) Masked(s string) bool {
	if m.Whole {
		return strings.Contains(s, "[redacted]")
	}
	return strings.Contains(s, tokenMask) || strings.Contains(s, "%2A%2A%2A%2A")
}

// webhookURL is a URL split into the parts we mask, raw (escaped) as they appear in the string.
type webhookURL struct {
	prefix   string // scheme://
	user     *url.Userinfo
	host     string
	segs     []string // EscapedPath split on "/"
	query    []string // RawQuery split on "&"
	fragment string   // with its "#", or ""
}

func parseWebhookURL(raw string) (*webhookURL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return nil, false
	}
	w := &webhookURL{prefix: u.Scheme + "://", user: u.User, host: u.Host, segs: strings.Split(u.EscapedPath(), "/")}
	if u.RawQuery != "" {
		w.query = strings.Split(u.RawQuery, "&")
	}
	if u.Fragment != "" {
		w.fragment = "#" + u.EscapedFragment()
	}
	return w, true
}

func (w *webhookURL) sameTarget(o *webhookURL) bool {
	return strings.EqualFold(w.prefix, o.prefix) && strings.EqualFold(w.host, o.host)
}

// String writes the URL back; a masked user name or password is written as the plain mask.
func (w *webhookURL) String() string {
	var b strings.Builder
	b.WriteString(w.prefix)
	if w.user != nil {
		name := w.user.Username()
		if name == tokenMask {
			b.WriteString(tokenMask)
		} else {
			b.WriteString(strings.TrimSuffix(url.User(name).String(), "@"))
		}
		if p, ok := w.user.Password(); ok {
			b.WriteString(":")
			if p == tokenMask {
				b.WriteString(tokenMask)
			} else {
				b.WriteString(strings.TrimPrefix(url.UserPassword("", p).String(), ":"))
			}
		}
		b.WriteString("@")
	}
	b.WriteString(w.host)
	b.WriteString(strings.Join(w.segs, "/"))
	if len(w.query) > 0 {
		b.WriteString("?" + strings.Join(w.query, "&"))
	}
	b.WriteString(w.fragment)
	return b.String()
}

func unescape(s string, query bool) string {
	var v string
	var err error
	if query {
		v, err = url.QueryUnescape(s)
	} else {
		v, err = url.PathUnescape(s)
	}
	if err != nil {
		return s
	}
	return v
}

// maskURL masks a webhook URL's credentials. A string that is not an absolute URL is returned unchanged.
func (m URLMasker) Mask(raw string) string {
	if m.Whole {
		return maskWholeURL(raw)
	}
	w, ok := parseWebhookURL(raw)
	if !ok {
		return raw
	}
	changed := false
	if w.user != nil {
		if p, has := w.user.Password(); has && p != "" {
			w.user, changed = url.UserPassword(w.user.Username(), tokenMask), true
		} else if !has && w.user.Username() != "" {
			w.user, changed = url.User(tokenMask), true
		}
	}
	for i, s := range w.segs {
		if tokenLike(unescape(s, false)) {
			w.segs[i], changed = tokenMask, true
		}
	}
	for i, kv := range w.query {
		k, v, _ := strings.Cut(kv, "=")
		if CredentialValue(unescape(k, true), unescape(v, true)) {
			w.query[i], changed = k+"="+tokenMask, true
		}
	}
	if !changed {
		return raw
	}
	return w.String()
}

// unmaskURL puts the stored URL's values back where in carries masks, when in keeps old's scheme and host.
func (m URLMasker) Unmask(in, old string) string {
	if m.Whole {
		if in != old && m.Mask(old) == in {
			return old
		}
		return in
	}
	if !strings.Contains(in, tokenMask) && !strings.Contains(in, "%2A%2A%2A%2A") {
		return in
	}
	w, ok := parseWebhookURL(in)
	o, ok2 := parseWebhookURL(old)
	if !ok || !ok2 || !w.sameTarget(o) {
		return in
	}
	if w.user != nil && o.user != nil {
		name := w.user.Username()
		if name == tokenMask {
			name = o.user.Username()
		}
		if p, has := w.user.Password(); has {
			if op, ohas := o.user.Password(); p == tokenMask && ohas {
				p = op
			}
			w.user = url.UserPassword(name, p)
		} else {
			w.user = url.User(name)
		}
	}
	if len(w.segs) == len(o.segs) {
		for i, s := range w.segs {
			if unescape(s, false) == tokenMask {
				w.segs[i] = o.segs[i]
			}
		}
	}
	seen := map[string]int{}
	for i, kv := range w.query {
		k, v, _ := strings.Cut(kv, "=")
		key := unescape(k, true)
		n := seen[key]
		seen[key]++
		if unescape(v, true) != tokenMask {
			continue
		}
		for _, okv := range o.query {
			ok, ov, _ := strings.Cut(okv, "=")
			if unescape(ok, true) != key {
				continue
			}
			if n == 0 {
				w.query[i] = k + "=" + ov
				break
			}
			n--
		}
	}
	return w.String()
}

// SameTarget reports whether two absolute webhook URLs use the same scheme and host. Header/password restoration
// uses the same boundary as URL restoration.
func (m URLMasker) SameTarget(a, b string) bool {
	x, ok := parseWebhookURL(a)
	y, ok2 := parseWebhookURL(b)
	return ok && ok2 && x.sameTarget(y)
}
