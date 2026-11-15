package logring

import (
	"regexp"
	"strings"

	"github.com/theatarr/theatarr/internal/secrets"
)

const redactMask = "********"

var (
	urlRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>]+`)
	// credentialPairRE is a credential-named key with its value: apikey=..., "token": "...", password: ....
	credentialPairRE = regexp.MustCompile(`(?i)([A-Za-z0-9_.\-]*(?:api[_\-]?key|apikey|passkey|authkey|rsskey|token|secret|password|passwd|pwd|authorization|cookie|session|sig))("?\s*[=:]\s*"?)([^\s"&,;}]+)`)
	// bearerRE is an Authorization header value.
	bearerRE = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=\-]{8,}`)
	// longTokenRE finds bare generated-looking strings: long, letters and digits, not a plain hex hash (info hashes
	// and digests are identifiers we want to see).
	longTokenRE = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)
	hexOnlyRE   = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// RedactText masks credentials in free text (theatarr debug bundle): every URL through the credential URL masker
// (user information, credential-named query values, token-like path segments), credential-named key/value pairs,
// Authorization values and long generated-looking tokens.
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
