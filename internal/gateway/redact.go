package gateway

import (
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// redactor masks secrets in what the gateway logs and returns.
type redactor struct {
	secrets []string // longest first, so a secret containing another is masked whole
}

const mask = "***"

// newRedactor collects the values of every secret env var the policy names, plus the DB-backed credentials the
// settings runtime resolved for this gateway. A credential that is a JSON object (a Cardigann indexer's secret
// settings) is masked whole and value by value: a definition can put one setting, a passkey, in a path. Values
// shorter than 6 characters are skipped: masking them would garble ordinary text and they are not credible keys.
func newRedactor(p *policy.Policy, extra []string) *redactor {
	r := &redactor{}
	add := func(v string) {
		if len(v) >= 6 {
			r.secrets = append(r.secrets, v)
		}
		var obj map[string]any
		if strings.HasPrefix(strings.TrimSpace(v), "{") && json.Unmarshal([]byte(v), &obj) == nil {
			for _, x := range obj {
				if s, ok := x.(string); ok && len(s) >= 6 {
					// As is, and escaped for a query (spaces as '+') and for a path (spaces as %20, '/' kept
					// or escaped as the definition builds it): a passkey can go in either.
					r.secrets = append(r.secrets, s)
					for _, e := range []string{url.QueryEscape(s), url.PathEscape(s), (&url.URL{Path: s}).EscapedPath()} {
						if e != s && !slices.Contains(r.secrets, e) {
							r.secrets = append(r.secrets, e)
						}
					}
				}
			}
		}
	}
	for env := range p.SecretEnvs() {
		add(p.Getenv(env))
	}
	for _, v := range extra {
		add(v)
	}
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return r
}

// String masks every known secret in s.
func (r *redactor) String(s string) string {
	for _, v := range r.secrets {
		s = strings.ReplaceAll(s, v, mask)
	}
	return s
}

// URL renders u for logs: scheme, host and path, no query, no fragment, no user info. Path segments holding '='
// (torrentio-style "realdebrid=<key>" configs) keep only the part before it, and known secrets are masked.
func (r *redactor) URL(u *url.URL) string {
	if u == nil {
		return ""
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i, s := range segs {
		if k, _, ok := strings.Cut(s, "="); ok {
			segs[i] = k + "=" + mask
		}
		if j := strings.Index(segs[i], "%3D"); j >= 0 {
			segs[i] = segs[i][:j] + "=" + mask
		}
	}
	return r.String(u.Scheme + "://" + u.Host + strings.Join(segs, "/"))
}

// TargetURL renders u for logs and errors about a request to a target of class c. A webhook's path and query often
// are its token (Discord, Slack, ntfy, push relays), so a webhook shows scheme and host only; every other class uses
// URL.
func (r *redactor) TargetURL(c Class, u *url.URL) string {
	if u == nil {
		return ""
	}
	if c == ClassWebhook {
		return r.String(u.Scheme + "://" + u.Host)
	}
	return r.URL(u)
}

// Redact masks the known secrets of the active settings generation in s (the error inbox's first redaction step).
func (g *Gateway) Redact(s string) string {
	c := g.current()
	if c == nil || c.redact == nil {
		return s
	}
	return c.redact.String(s)
}
