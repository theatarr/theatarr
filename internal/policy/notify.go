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
	// Enabled sends notifications. Off (the default): events are recorded, nothing is sent.
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
	// DigestAt is the local time (HH:MM, the container's TZ) the daily digest goes to Discord, ntfy and Telegram targets.
	// Default 09:00.
	DigestAt string `yaml:"digest_at"`
}

// NotifyTarget is a webhook defined in the policy.
type NotifyTarget struct {
	// URL is where payloads are POSTed. Its host joins the gateway's allowlist. Empty for a telegram target.
	URL string `yaml:"url"`
	// Views lists the arr views (keys of views, or v/<version>) whose arr-shaped payloads it gets.
	Views []string `yaml:"views"`
	// Native sends Theatarr's own per-version events (JSON, eventType theatarr.*).
	Native bool `yaml:"native"`
	// Format is the message format: empty for the payloads above, discord for a Discord channel webhook, ntfy, or
	// telegram for a Telegram chat. For ntfy the URL is the server address followed by the topic, and an access token
	// goes in the Authorization header. A Telegram target has no URL (it sends to api.telegram.org) and takes a chat
	// id and a bot token instead. These formats get Theatarr's own messages (instant health and error alerts, the
	// daily digest, request ready, a file replaced while playing) and take no views.
	Format string `yaml:"format"`
	// Events limits what this target is sent to the events chosen. Empty: all.
	Events []string `yaml:"events"`
	// Versions limits native events to these versions. Empty: all.
	Versions []string `yaml:"versions"`
	// AuthHeaderEnv names an environment variable holding the Authorization header value, when the target needs one.
	AuthHeaderEnv string `yaml:"auth_header_env"`
	// ChatID is the Telegram chat a telegram target posts to: the chat's number (a group's starts with -100) or a
	// public channel's @name.
	ChatID string `yaml:"chat_id,omitempty"`
	// BotTokenEnv names an environment variable holding the Telegram bot token of a telegram target. It is only ever
	// sent to api.telegram.org, never as an Authorization header.
	BotTokenEnv string `yaml:"bot_token_env,omitempty"`
	// Warnings also sends health warnings, such as a mount that stopped answering, to this message target (format
	// discord, ntfy or telegram). Errors are always sent; notices never are. Default false.
	Warnings bool `yaml:"warnings,omitempty"`
}

// NotifyEvents are the event names a target may list. "error" is no longer sent (the error inbox went in 0.4.5); it
// stays accepted so stored targets that list it still load.
var NotifyEvents = []string{"grab", "download", "upgrade", "rename", "delete", "health", "health_restored", "series_add", "movie_added", "manual_interaction_required", "test", "error", "digest", "request_ready", "playing"}

// NotifyFormatDiscord is the target format that sends Discord messages.
const NotifyFormatDiscord = "discord"

// NotifyFormatNtfy is the target format that publishes ntfy messages.
const NotifyFormatNtfy = "ntfy"

// NotifyFormatTelegram is the target format that sends Telegram messages through a bot.
const NotifyFormatTelegram = "telegram"

// TelegramAPI is where a Telegram target sends: the Bot API's fixed address (https://core.telegram.org/bots/api,
// "Making requests"). It is the only host a Telegram target uses.
const TelegramAPI = "https://api.telegram.org"

// telegramChat is a Telegram chat id: a number (negative for groups and channels) or a public @username.
var telegramChat = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{3,})$`)

// BotTokenEnvRefused is the refusal of a bot_token_env that is not an environment variable name: it never quotes the
// value, which is most likely the bot token itself.
const BotTokenEnvRefused = "want the name of an environment variable (A-Z, 0-9, _), not the token itself"

// IsEnvName reports whether v is an environment variable name (A-Z, 0-9, _), as the *_env fields take.
func IsEnvName(v string) bool { return envRE.MatchString(v) }

// Endpoint is where target t sends: TelegramAPI for a Telegram target (its URL is never used), else its URL.
func (t *NotifyTarget) Endpoint() string {
	if t.Format == NotifyFormatTelegram {
		return TelegramAPI
	}
	return t.URL
}

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
		if t.Format == NotifyFormatTelegram {
			if t.URL != "" {
				e.add(path+".url", "a Telegram target sends to api.telegram.org: leave the URL empty")
			}
		} else if !validURL {
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
				e.add(path+".versions", "unknown library %q", v)
			}
		}
		switch t.Format {
		case "":
			if len(t.Views) == 0 && !t.Native {
				e.add(path, "sends nothing: set views or native")
			}
			if t.Warnings {
				e.add(path+".warnings", "only a message target (format discord, ntfy or telegram) takes warnings; a webhook target is filtered by events")
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
		case NotifyFormatTelegram:
			if len(t.Views) > 0 {
				e.add(path+".views", "a Telegram target gets Theatarr's own messages, not arr payloads: remove views")
			}
			if t.ChatID == "" {
				e.add(path+".chat_id", "required: the Telegram chat to post to")
			} else if !telegramChat.MatchString(t.ChatID) {
				e.add(path+".chat_id", "want a chat id: a number such as -1001234567890, or a public channel's @name")
			}
			if t.BotTokenEnv == "" {
				e.add(path+".bot_token_env", "required: the name of the environment variable that holds the bot token")
			}
		default:
			e.add(path+".format", "unknown format %q (want discord, ntfy or telegram, or leave it empty)", t.Format)
		}
		// Never echoed: a bot token pasted here instead of a variable name must not reach a log or an answer.
		if t.BotTokenEnv != "" && !envRE.MatchString(t.BotTokenEnv) {
			e.add(path+".bot_token_env", BotTokenEnvRefused)
		}
		checkEnv(e, path+".auth_header_env", t.AuthHeaderEnv, false)
	}
}
