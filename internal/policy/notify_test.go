package policy

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNotifyValidation(t *testing.T) {
	t.Parallel()
	r := -1
	p := &Policy{Versions: map[string]*VersionPolicy{"movie-web": {}}, Views: map[string]*View{"radarr": {}},
		Notify: &Notify{Retries: &r, AllowHosts: []string{"notify.ruddarr.com", "bad/host"}, Budget: "nope",
			Targets: map[string]*NotifyTarget{
				"automation": {URL: "http://192.168.1.50:8080/hook", Views: []string{"radarr", "v/movie-web"}, Native: true},
				"installed":  {URL: "http://x/", Native: true},
				"silent":     {URL: "http://x/"},
				"creds":      {URL: "http://u:p@x/", Views: []string{"sonarr"}, Events: []string{"boom"}, Native: true},
			}}}
	e := &Errors{}
	p.validateNotify(e)
	got := e.Error()
	for _, want := range []string{"notify.retries", "bad/host", "unknown budget", "targets[installed]: the key installed",
		"targets[silent]: sends nothing", "no credentials", `unknown view "sonarr"`, `unknown event "boom"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "targets[automation]") {
		t.Errorf("automation refused: %s", got)
	}
	hosts := map[string]bool{}
	for _, h := range p.OutboundHosts() {
		hosts[h.Host] = true
	}
	if !hosts["notify.ruddarr.com"] || !hosts["192.168.1.50:8080"] {
		t.Errorf("outbound hosts %v", hosts)
	}
	u, _ := url.Parse("https://notify.ruddarr.com/push/x")
	if !p.NotifyHostAllowed(u) {
		t.Error("allowed host refused")
	}
	u, _ = url.Parse("https://evil.example/")
	if p.NotifyHostAllowed(u) {
		t.Error("other host allowed")
	}
	if s := (&Policy{}).NotifySettings(); s.Enabled || s.Retries != DefaultNotifyRetries {
		t.Errorf("defaults %+v", s)
	}
}

func TestNotifyParityEvents(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"health_restored", "series_add", "movie_added", "manual_interaction_required", "rename"} {
		t.Run(event, func(t *testing.T) {
			p := &Policy{Notify: &Notify{Targets: map[string]*NotifyTarget{
				"hook": {URL: "https://hook.example/event", Native: true, Events: []string{event}},
			}}}
			err := &Errors{}
			p.validateNotify(err)
			if got := err.Error(); got != "" {
				t.Fatal(got)
			}
		})
	}
}

func TestNotifyFormats(t *testing.T) {
	t.Parallel()
	p := &Policy{Views: map[string]*View{"radarr": {}}, Notify: &Notify{Targets: map[string]*NotifyTarget{
		"json":       {URL: "http://hooks.example/hook", Native: true},
		"chan":       {URL: "https://discord.com/api/webhooks/1/token", Format: NotifyFormatDiscord},
		"phone":      {URL: "https://ntfy.example/alerts", Format: NotifyFormatNtfy, AuthHeaderEnv: "THEATARR_NTFY_AUTH"},
		"sub":        {URL: "http://ntfy.example:8080/push/alerts/", Format: NotifyFormatNtfy},
		"ntfy-views": {URL: "https://ntfy.example/alerts", Format: NotifyFormatNtfy, Views: []string{"radarr"}},
		"ntfy-root":  {URL: "https://ntfy.example", Format: NotifyFormatNtfy},
		"ntfy-bad":   {URL: "ntfy.example/alerts", Format: NotifyFormatNtfy},
		"max":        {URL: "https://ntfy.example/" + strings.Repeat("a", 64), Format: NotifyFormatNtfy},
		"t-space":    {URL: "https://ntfy.example/a%20b", Format: NotifyFormatNtfy},
		"t-dot":      {URL: "https://ntfy.example/alerts.json", Format: NotifyFormatNtfy},
		"t-unicode":  {URL: "https://ntfy.example/alérts", Format: NotifyFormatNtfy},
		"t-long":     {URL: "https://ntfy.example/" + strings.Repeat("a", 65), Format: NotifyFormatNtfy},
		"slack":      {URL: "https://hooks.example/x", Format: "slack"},
	}}}
	e := &Errors{}
	p.validateNotify(e)
	got := e.Error()
	for _, want := range []string{"targets[ntfy-views].views: an ntfy target gets Theatarr's own messages",
		"targets[ntfy-root].url: an ntfy target's URL ends in the topic", "targets[ntfy-bad].url: want an http(s) URL",
		`targets[slack].format: unknown format "slack" (want discord, ntfy or telegram, or leave it empty)`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, k := range []string{"t-space", "t-dot", "t-unicode", "t-long"} {
		if !strings.Contains(got, "targets["+k+"].url: the ntfy topic at the end of the URL must be 1 to 64 letters, digits, - or _") {
			t.Errorf("topic of %s accepted: %s", k, got)
		}
	}
	if strings.Contains(got, "a b") || strings.Contains(got, "alerts.json") {
		t.Errorf("a topic echoed: %s", got)
	}
	for _, not := range []string{"targets[json]", "targets[chan]", "targets[phone]", "targets[sub]", "targets[max]"} {
		if strings.Contains(got, not) {
			t.Errorf("%s refused: %s", not, got)
		}
	}
	if n := strings.Count(got, "targets[ntfy-bad]"); n != 1 {
		t.Errorf("a bad URL reported %d times: %s", n, got)
	}
	if server, topic, ok := NtfyEndpoint("http://ntfy.example:8080/push/alerts/?x=1"); !ok ||
		server != "http://ntfy.example:8080/push/?x=1" || topic != "alerts" {
		t.Errorf("endpoint %q %q %v", server, topic, ok)
	}
}

// warnings is an option of a message target; a config without it marshals as it did before the field existed.
func TestNotifyTargetWarnings(t *testing.T) {
	t.Parallel()
	targets := map[string]*NotifyTarget{
		"chan":  {URL: "https://discord.example/api/webhooks/1/x", Format: NotifyFormatDiscord, Warnings: true},
		"phone": {URL: "https://ntfy.example/alerts", Format: NotifyFormatNtfy, Warnings: true},
		"hook":  {URL: "http://192.168.1.50:8080/hook", Native: true, Warnings: true},
		"plain": {URL: "http://192.168.1.50:8080/hook", Native: true},
	}
	e := &Errors{}
	(&Policy{Notify: &Notify{Targets: targets}}).validateNotify(e)
	if len(e.List) != 1 || e.List[0].Path != "notify.targets[hook].warnings" {
		t.Errorf("want only the webhook target's warnings refused: %v", e.List)
	}
	b, err := yaml.Marshal(targets["plain"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "warnings") {
		t.Errorf("unset warnings written: %s", b)
	}
	if b, _ = yaml.Marshal(targets["chan"]); !strings.Contains(string(b), "warnings: true") {
		t.Errorf("set warnings not written: %s", b)
	}
}

// A Telegram target sends to the fixed Bot API host with its own bot token secret and a chat id; it takes no URL, no
// views. Its host joins the allowlist and its secret is known to the redactor (SecretEnvs).
func TestNotifyTelegramTarget(t *testing.T) {
	t.Parallel()
	p := &Policy{Views: map[string]*View{"radarr": {}}, Notify: &Notify{Targets: map[string]*NotifyTarget{
		"tg":       {Format: NotifyFormatTelegram, ChatID: "-1001234567890", BotTokenEnv: "THEATARR_TG_TOKEN"},
		"tg-name":  {Format: NotifyFormatTelegram, ChatID: "@theatarr_alerts", BotTokenEnv: "THEATARR_TG_TOKEN", Events: []string{"playing"}},
		"tg-url":   {URL: "https://api.telegram.org/botX/sendMessage", Format: NotifyFormatTelegram, ChatID: "1", BotTokenEnv: "T"},
		"tg-bare":  {Format: NotifyFormatTelegram},
		"tg-chat":  {Format: NotifyFormatTelegram, ChatID: "not a chat", BotTokenEnv: "T"},
		"tg-views": {Format: NotifyFormatTelegram, ChatID: "1", BotTokenEnv: "T", Views: []string{"radarr"}},
	}}}
	e := &Errors{}
	p.validateNotify(e)
	got := e.Error()
	for _, want := range []string{"targets[tg-url].url: a Telegram target sends to api.telegram.org: leave the URL empty",
		"targets[tg-bare].chat_id: required", "targets[tg-bare].bot_token_env: required",
		"targets[tg-chat].chat_id: want a chat id", "targets[tg-views].views: a Telegram target gets Theatarr's own messages"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, not := range []string{"targets[tg]", "targets[tg-name]", "not a chat"} {
		if strings.Contains(got, not) {
			t.Errorf("%s in %s", not, got)
		}
	}
	if got := p.Notify.Targets["tg"].Endpoint(); got != TelegramAPI || TelegramAPI != "https://api.telegram.org" {
		t.Errorf("endpoint %q", got)
	}
	if got := p.Notify.Targets["tg-url"].Endpoint(); got != TelegramAPI {
		t.Errorf("a Telegram target's URL is never its endpoint: %q", got)
	}
	hosts := map[string][]string{}
	for _, h := range p.OutboundHosts() {
		hosts[h.Host] = h.Users
	}
	if users := hosts["api.telegram.org"]; len(users) == 0 || users[0] != "notify.targets[tg]" {
		t.Errorf("hosts %v", hosts)
	}
	if users := p.SecretEnvs()["THEATARR_TG_TOKEN"]; !slices.Contains(users, "notify.targets[tg].bot_token_env") {
		t.Errorf("secret envs %v", p.SecretEnvs())
	}
	if !slices.Contains(NotifyEvents, "playing") {
		t.Error("no playing event name")
	}
	// The new fields are left out while unset, so a config without them marshals as before.
	b, err := yaml.Marshal(&NotifyTarget{URL: "http://192.168.1.50:8080/hook", Native: true})
	if err != nil || strings.Contains(string(b), "chat_id") || strings.Contains(string(b), "bot_token") {
		t.Errorf("unset Telegram fields written: %s %v", b, err)
	}
}

// A bot token pasted into bot_token_env is refused without being echoed.
func TestNotifyTelegramTokenNotEchoed(t *testing.T) {
	t.Parallel()
	const token = "123456789:AAFakeBotTokenInvented0123456789"
	for _, format := range []string{NotifyFormatTelegram, NotifyFormatDiscord} {
		p := &Policy{Notify: &Notify{Targets: map[string]*NotifyTarget{
			"tg": {URL: "https://discord.com/api/webhooks/1/x", Format: format, ChatID: "-1001234567890", BotTokenEnv: token}}}}
		if format == NotifyFormatTelegram {
			p.Notify.Targets["tg"].URL = ""
		}
		got := fmt.Sprint(p.Validate())
		if strings.Contains(got, token) || strings.Contains(got, "AAFake") || !strings.Contains(got, "targets[tg].bot_token_env: want the name of an environment variable") {
			t.Errorf("%s: %s", format, got)
		}
	}
}
