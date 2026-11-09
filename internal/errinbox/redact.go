package errinbox

import (
	"regexp"
	"strings"

	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/secrets"
)

const redactMask = "********"

var (
	// credentialPairRE is a credential-named key with its value: apikey=..., "token": "...", password: ....
	credentialPairRE = regexp.MustCompile(`(?i)([A-Za-z0-9_.\-]*(?:api[_\-]?key|apikey|passkey|authkey|rsskey|token|secret|password|passwd|pwd|authorization|cookie|session|sig))("?\s*[=:]\s*"?)([^\s"&,;}]+)`)
	// bearerRE is an Authorization header value.
	bearerRE = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=\-]{8,}`)
	// longTokenRE finds bare generated-looking strings: long, letters and digits, not a plain hex hash (info hashes
	// and digests are identifiers we want to see).
	longTokenRE = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)
	hexOnlyRE   = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// Redact masks credentials in free text with the existing rules: the known secrets of the running settings (the
// gateway's redactor, when serve installed one), every URL through the credential URL masker (user information,
// credential-named query values, token-like path segments), credential-named key/value pairs, Authorization values
// and long generated-looking tokens.
func (in *Inbox) Redact(s string) string {
	if in != nil {
		if fn := in.known.Load(); fn != nil {
			s = (*fn)(s)
		}
	}
	return RedactText(s)
}

// RedactText is Redact without the known secrets.
func RedactText(s string) string {
	if s == "" {
		return s
	}
	s = urlRE.ReplaceAllStringFunc(s, func(u string) string {
		return (secrets.URLMasker{}).Mask(u)
	})
	s = bearerRE.ReplaceAllString(s, "$1 "+redactMask)
	s = credentialPairRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := credentialPairRE.FindStringSubmatch(m)
		switch strings.ToLower(sub[3]) {
		case redactMask, "[redacted]", "bearer", "basic", "token":
			return m
		}
		return sub[1] + sub[2] + redactMask
	})
	s = longTokenRE.ReplaceAllStringFunc(s, func(t string) string {
		if hexOnlyRE.MatchString(t) || !strings.ContainsAny(t, "0123456789") || strings.Trim(t, "0123456789_-") == "" {
			return t
		}
		return redactMask
	})
	return s
}

// redactAttrs redacts a context map: values under secret-named keys are dropped to the mask (as the log ring does),
// the rest go through Redact.
func (in *Inbox) redactAttrs(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if logring.SecretKey(k) {
			out[k] = "[redacted]"
			continue
		}
		out[k] = in.Redact(v)
	}
	return out
}
