package policy

import (
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Notify configures outbound webhooks (docs/design/devices.md): arr-shaped payloads per view and Theatarr's own
// per-version events, sent through the gateway to the targets listed here and to the notifications clients install
// through a view (Ruddarr's webhook, POST notification). Nothing is sent unless notify.enabled is true, and an
// installed notification is sent only when its URL's host is in allow_hosts (or is a target's host). Sending never
// blocks the pipeline: deliveries queue in the database and a sender retries them with back-off.
type Notify struct {
	// Enabled allows sending at all. Default false: events are still recorded, nothing leaves the process.
	Enabled bool `yaml:"enabled"`
	// AllowHosts lists the hosts (host or host:port) an installed notification may target. Each joins the gateway's
	// allowlist. Empty: installed notifications are stored but never sent.
	AllowHosts []string `yaml:"allow_hosts"`
	// Budget names the budget every webhook request takes a token from (optional).
	Budget string `yaml:"budget"`
	// Retries is how many times a failed delivery is retried. Default 5, 0 to 20.
	Retries *int `yaml:"retries"`
	// Poll is how often the notifier looks for new events and due deliveries. Default 15s, 1s to 10m.
	Poll Duration `yaml:"poll"`
	// Targets lists webhook targets defined in the policy, by key.
	Targets map[string]*NotifyTarget `yaml:"targets"`
	// DigestAt is the local time (HH:MM, the container's TZ) the daily digest goes to Discord and ntfy targets.
	// Default 09:00.
	DigestAt string `yaml:"digest_at"`
}

// NotifyTarget is a webhook defined in the policy.
type NotifyTarget struct {
	// URL is where payloads are POSTed. Its host joins the gateway's allowlist.
	URL string `yaml:"url"`
	// Views lists the arr views (keys of views, or v/<version>) whose arr-shaped payloads it gets.
	Views []string `yaml:"views"`
	// Native sends Theatarr's own per-version events (JSON, eventType theatarr.*).
	Native bool `yaml:"native"`
	// Format is the message format: empty for the payloads above, discord for a Discord channel webhook, or ntfy.
	// For ntfy the URL is the server address followed by the topic, and an access token goes in the Authorization
	// header. Both formats get Theatarr's own messages (instant health and error alerts, the daily digest, request
	// ready) and take no views.
	Format string `yaml:"format"`
	// Events limits what this target is sent to the events chosen. Empty: all.
	Events []string `yaml:"events"`
	// Versions limits native events to these versions. Empty: all.
	Versions []string `yaml:"versions"`
	// AuthHeaderEnv names an environment variable holding the Authorization header value, when the target needs one.
	AuthHeaderEnv string `yaml:"auth_header_env"`
}

// NotifyEvents are the event names a target may list.
var NotifyEvents = []string{"grab", "download", "upgrade", "rename", "delete", "health", "health_restored", "series_add", "movie_added", "manual_interaction_required", "test", "error", "digest", "request_ready"}

// NotifyFormatDiscord is the target format that sends Discord messages.
const NotifyFormatDiscord = "discord"

// NotifyFormatNtfy is the target format that publishes ntfy messages.
const NotifyFormatNtfy = "ntfy"

// ntfyTopic is what ntfy accepts as a topic name.
var ntfyTopic = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// NtfyEndpoint splits an ntfy target's URL into the address messages are POSTed to and the topic: the topic is the
// last path segment, the address is the URL without it (https://host/topic gives https://host/ and topic; a server
// under a sub-path keeps the path before the topic). ok is false when the URL has no topic.
func NtfyEndpoint(raw string) (server, topic string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	path := strings.TrimRight(u.Path, "/")
	i := strings.LastIndex(path, "/")
	if i < 0 || path[i+1:] == "" {
		return "", "", false
	}
	base := *u
	base.Path, base.RawPath, base.Fragment = path[:i+1], "", ""
	return base.String(), path[i+1:], true
}

// Notify defaults.
const (
	DefaultNotifyRetries = 5
	DefaultNotifyPoll    = 15 * time.Second
	DefaultDigestAt      = "09:00"
)

// NotifySettings is the notify section with defaults.
type NotifySettings struct {
	Enabled    bool
	AllowHosts []string
	Budget     string
	Retries    int
	Poll       time.Duration
	Targets    map[string]*NotifyTarget
	// DigestHour and DigestMinute are notify.digest_at.
	DigestHour, DigestMinute int
}

// NotifySettings returns the notify section with defaults; without one nothing is sent.
func (p *Policy) NotifySettings() NotifySettings {
	s := NotifySettings{Retries: DefaultNotifyRetries, Poll: DefaultNotifyPoll, Targets: map[string]*NotifyTarget{},
		DigestHour: 9}
	n := p.Notify
	if n == nil {
		return s
	}
	if h, m, ok := parseClock(n.DigestAt); ok {
		s.DigestHour, s.DigestMinute = h, m
	}
	s.Enabled, s.AllowHosts, s.Budget = n.Enabled, n.AllowHosts, n.Budget
	if n.Retries != nil {
		s.Retries = *n.Retries
	}
	if n.Poll != 0 {
		s.Poll = n.Poll.D()
	}
	if n.Targets != nil {
		s.Targets = n.Targets
	}
	return s
}

// NotifyHostAllowed reports whether an installed notification may send to u: its host is in allow_hosts, or is the
// host of a policy target.
func (p *Policy) NotifyHostAllowed(u *url.URL) bool {
	if p.Notify == nil || u == nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	for _, h := range p.Notify.AllowHosts {
		if strings.ToLower(h) == host {
			return true
		}
	}
	for _, t := range p.Notify.Targets {
		if tu, err := url.Parse(t.URL); err == nil && strings.ToLower(tu.Host) == host {
			return true
		}
	}
	return false
}

// parseClock reads HH:MM (00:00 to 23:59).
func parseClock(v string) (int, int, bool) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

func (p *Policy) validateNotify(e *Errors) {
	n := p.Notify
	if n == nil {
		return
	}
	if _, _, ok := parseClock(n.DigestAt); n.DigestAt != "" && !ok {
		e.add("notify.digest_at", "%q: want HH:MM, 00:00 to 23:59", n.DigestAt)
	}
	if n.Retries != nil && (*n.Retries < 0 || *n.Retries > 20) {
		e.add("notify.retries", "%d: want 0 to 20", *n.Retries)
	}
	if n.Poll != 0 && (n.Poll.D() < time.Second || n.Poll.D() > 10*time.Minute) {
		e.add("notify.poll", "%v: want 1s to 10m", n.Poll.D())
	}
	if n.Budget != "" && p.Budgets[n.Budget] == nil {
		e.add("notify.budget", "unknown budget %q", n.Budget)
	}
	for i, h := range n.AllowHosts {
		if h == "" || strings.ContainsAny(h, "/?#@ ") {
			e.add("notify.allow_hosts", "entry %d %q: want host or host:port", i, h)
			continue
		}
		if _, _, err := net.SplitHostPort(h); err != nil && strings.Count(h, ":") == 1 {
			e.add("notify.allow_hosts", "entry %d %q: bad port", i, h)
		}
	}
	views := map[string]bool{}
	for k := range p.Views {
		views[k] = true
	}
	for k := range p.Versions {
		views["v/"+k] = true
	}
	for _, k := range sortedKeys(n.Targets) {
		t := n.Targets[k]
		path := "notify.targets[" + k + "]"
		checkKey(e, path, k)
		if k == "installed" {
			e.add(path, "the key installed is reserved for the notifications clients install through a view")
		}
		if t == nil {
			e.add(path, "empty")
			continue
		}
		u, err := url.Parse(t.URL)
		validURL := err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
		if !validURL {
			e.add(path+".url", "want an http(s) URL")
		} else if u.User != nil {
			e.add(path+".url", "no credentials in the URL; use auth_header_env")
		}
		for _, v := range t.Views {
			if !views[v] {
				e.add(path+".views", "unknown view %q", v)
			}
		}
		for _, ev := range t.Events {
			if !slices.Contains(NotifyEvents, ev) {
				e.add(path+".events", "unknown event %q (want %s)", ev, strings.Join(NotifyEvents, ", "))
			}
		}
		for _, v := range t.Versions {
			if p.Versions[v] == nil {
				e.add(path+".versions", "unknown version %q", v)
			}
		}
		switch t.Format {
		case "":
			if len(t.Views) == 0 && !t.Native {
				e.add(path, "sends nothing: set views or native")
			}
		case NotifyFormatDiscord:
			if len(t.Views) > 0 {
				e.add(path+".views", "a Discord target gets Theatarr's own messages, not arr payloads: remove views")
			}
		case NotifyFormatNtfy:
			if len(t.Views) > 0 {
				e.add(path+".views", "an ntfy target gets Theatarr's own messages, not arr payloads: remove views")
			}
			if _, topic, ok := NtfyEndpoint(t.URL); !ok && validURL {
				e.add(path+".url", "an ntfy target's URL ends in the topic: https://<server>/<topic>")
			} else if ok && validURL && !ntfyTopic.MatchString(topic) {
				e.add(path+".url", "the ntfy topic at the end of the URL must be 1 to 64 letters, digits, - or _")
			}
		default:
			e.add(path+".format", "unknown format %q (want discord or ntfy, or leave it empty)", t.Format)
		}
		checkEnv(e, path+".auth_header_env", t.AuthHeaderEnv, false)
	}
}
