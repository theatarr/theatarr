package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
)

const (
	tgToken = "123456789:AAFakeBotTokenInvented0123456789"
	tgChat  = "-1001234567890"
)

// newTelegramEnv is newNEnv with a Telegram target "tg" (bot token in THEATARR_TG_TOKEN) and a job queue.
func newTelegramEnv(t *testing.T, edit func(*policy.Policy)) *nenv {
	t.Helper()
	e := newNEnv(t, true)
	e.env["THEATARR_TG_TOKEN"] = tgToken
	e.pol.Notify.Targets["tg"] = &policy.NotifyTarget{Format: policy.NotifyFormatTelegram, ChatID: tgChat,
		BotTokenEnv: "THEATARR_TG_TOKEN"}
	if edit != nil {
		edit(e.pol)
	}
	e.rebuild()
	return e
}

// telegramSent are the stored Telegram bodies for tg, oldest first. None holds the token or the chat.
func (e *nenv) telegramSent() []telegramMessage {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT event, payload, last_error FROM notify_delivery WHERE target = 'policy:tg' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []telegramMessage
	for rows.Next() {
		var event, raw, last string
		if err := rows.Scan(&event, &raw, &last); err != nil {
			e.t.Fatal(err)
		}
		if !strings.HasPrefix(event, "telegram.") {
			e.t.Errorf("event %q to a Telegram target", event)
		}
		if strings.Contains(raw+last, tgToken) || strings.Contains(raw, tgChat) || strings.Contains(raw, "chat_id") {
			e.t.Errorf("token or chat stored: %s %s", raw, last)
		}
		var m telegramMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			e.t.Fatal(err)
		}
		if strings.TrimSpace(m.Text) == "" {
			e.t.Errorf("empty text: %s", raw)
		}
		out = append(out, m)
	}
	return out
}

func playingOutcome(kind string) outcome.Outcome {
	return outcome.Outcome{Incident: 7, Kind: kind, TitleID: 1, VersionKey: "movie-web", Title: "Alpha (2020)",
		OldRelease: "Alpha.2020.1080p.WEB-DL.DDP5.1.H.264-OLD", NewRelease: "Alpha.2020.1080p.BluRay.x264-NEW",
		Account: "torbox", Viewer: "ann", Player: "Living room TV", At: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
}

// Every message kind a Telegram target gets has text: requests, problems, the digest, the test, health and the
// three playing messages (the shared plain-text builder, as ntfy uses).
func TestTelegramText(t *testing.T) {
	t.Parallel()
	e := newTelegramEnv(t, nil)
	ctx := context.Background()
	was := &HealthItem{Source: "MountCheck", Type: "error", Message: "mount not readable"}
	same := playingOutcome(outcome.SameRelease)
	for _, c := range []struct {
		name string
		ev   Event
		want []string
	}{
		{"health", Event{Kind: Health, Health: []HealthItem{*was}}, []string{"Health check failing: MountCheck\nmount not readable"}},
		{"digest", Event{Kind: Digest, Digest: &DigestSummary{Day: "2026-11-06"}}, []string{"Theatarr daily digest · 2026-11-06\nQuiet day"}},
		{"request", Event{Kind: RequestAvailable, Request: &Request{Name: "Alpha", TitleID: 1, Users: []RequestUser{{Username: "ann", DiscordID: "123456789012345678"}}}},
			[]string{"Request ready\nAlpha (2020) is ready in Plex, requested by ann"}},
		{"partial", Event{Kind: RequestPartial, Request: &Request{Name: "Alpha", TitleID: 1}}, []string{"Request partly ready\nAlpha (2020) is partly ready in Plex"}},
		{"problem", Event{Kind: ProblemReported, Problem: &Problem{Name: "Alpha (2020)", Label: "Wrong language", Note: "French audio", Reporter: RequestUser{Username: "bob"}}},
			[]string{"Problem reported: Alpha (2020)\nWrong language · reported by bob\nFrench audio"}},
		{"test", Event{Kind: Test}, []string{"Theatarr test message\nThis chat gets Theatarr's notifications."}},
		{"replaced", Event{Kind: PlayingReplaced, Playing: ptr(playingOutcome(outcome.Replaced))},
			[]string{"Replaced while playing: Alpha (2020)", "while ann was watching on Living room TV", "Was: Alpha.2020.1080p.WEB-DL.DDP5.1.H.264-OLD", "Now: Alpha.2020.1080p.BluRay.x264-NEW"}},
		{"readable again", Event{Kind: PlayingReplaced, Playing: &same}, []string{"Readable again: Alpha (2020)", "reads again from torbox"}},
		{"unavailable", Event{Kind: PlayingUnavailable, Playing: ptr(playingOutcome(outcome.Unavailable))}, []string{"No other copy: Alpha (2020)", "No other copy is available right now"}},
		{"restored", Event{Kind: PlayingRestored, Playing: ptr(playingOutcome(outcome.Restored))}, []string{"Earlier copy put back: Alpha (2020)", "back in place"}},
	} {
		m, err := e.n.telegramPayload(ctx, c.ev)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(m.Text, w) {
				t.Errorf("%s: %q not in %q", c.name, w, m.Text)
			}
		}
		if strings.Contains(m.Text, "123456789012345678") || strings.Contains(m.Text, "**") {
			t.Errorf("%s: Discord markup or id: %q", c.name, m.Text)
		}
	}
	if _, err := e.n.telegramPayload(ctx, Event{Kind: Grab}); err == nil {
		t.Error("a grab has no Telegram message")
	}
	// The body is the text alone: chat_id joins it only when sent.
	b, _ := json.Marshal(fitTelegram("T", []string{"m"}))
	if string(b) != `{"text":"T\nm"}` {
		t.Errorf("body %s", b)
	}
}

func ptr[T any](v T) *T { return &v }

// A long message is cut by characters (UTF-16 code units, as Telegram counts) to 4096, with "and N more".
func TestTelegramFit(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := range 400 {
		lines = append(lines, fmt.Sprintf("• Ünïcödé title %d ééééééééééééééééééééééé 🎬", i))
	}
	m := fitTelegram("Theatarr daily digest · 2026-11-06", lines)
	n := len(utf16.Encode([]rune(m.Text)))
	if n > telegramTextMax || n < telegramTextMax-200 || !strings.Contains(m.Text, " more") ||
		!strings.HasPrefix(m.Text, "Theatarr daily digest") {
		t.Errorf("text %d units: …%q", n, m.Text[max(0, len(m.Text)-40):])
	}
	// Characters, not bytes: 4000 two-byte letters fit whole.
	if m := fitTelegram("A", []string{strings.Repeat("é", 4000)}); strings.Contains(m.Text, "…") {
		t.Errorf("cut by bytes: %d", len(m.Text))
	}
	if m := fitTelegram("A", []string{strings.Repeat("語", 5000)}); m.Text != "A\nand 1 more" {
		t.Errorf("a line too long: %q", m.Text)
	}
	if m := fitTelegram(strings.Repeat("語", 5000), nil); len(utf16.Encode([]rune(m.Text))) > telegramTextMax || !strings.HasSuffix(m.Text, "…") {
		t.Errorf("a title too long: %d", len(m.Text))
	}
}

// The request: POST to api.telegram.org/bot<token>/sendMessage with the chat and text, never an Authorization
// header (also when the target still names an auth_header secret). Stored deliveries hold neither token nor chat,
// and a delivery queued under another format is dropped both ways.
func TestTelegramSend(t *testing.T) {
	t.Parallel()
	e := newTelegramEnv(t, func(p *policy.Policy) { p.Notify.Targets["tg"].AuthHeaderEnv = "THEATARR_HOOK_AUTH" })
	ctx := context.Background()
	if err := e.n.SendTest(ctx, "tg"); err != nil {
		t.Fatal(err)
	}
	r := e.doer.reqs[0]
	if r.method != "POST" || r.url != "https://api.telegram.org/bot"+tgToken+"/sendMessage" || r.target.Key != "tg" ||
		r.target.Class != gateway.ClassWebhook || r.auth != "" {
		t.Errorf("request %+v", r)
	}
	if r.body["chat_id"] != tgChat || r.body["text"] != "Theatarr test message\nThis chat gets Theatarr's notifications." || len(r.body) != 2 {
		t.Errorf("body %v", r.body)
	}
	// Through the outbox.
	e.now = e.now.Add(time.Minute)
	if err := e.n.Test(ctx, "tg"); err != nil {
		t.Fatal(err)
	}
	if got := e.telegramSent(); len(got) != 1 {
		t.Fatalf("queued %d", len(got))
	}
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 1 || e.doer.reqs[1].body["chat_id"] != tgChat {
		t.Fatalf("sent %d %v %+v", sent, err, e.doer.reqs)
	}
	// A Discord delivery queued before the switch to Telegram is not posted to Telegram; a Telegram one queued before
	// the switch away is not posted to the Discord URL (where the token-free text would land, harmless, but wrong).
	e.exec(`INSERT INTO notify_delivery (target, event, view, payload, state, next_at, last_error, created_at)
		VALUES ('policy:tg', 'discord.test', '', '{"content":"x","allowed_mentions":{"parse":[]}}', 'pending', 0, '', 0)`)
	before := len(e.doer.reqs)
	e.now = e.now.Add(time.Minute)
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 0 || len(e.doer.reqs) != before {
		t.Errorf("another format's delivery: sent %d %v", sent, err)
	}
	if err := e.n.Test(ctx, "tg"); err != nil {
		t.Fatal(err)
	}
	e.pol.Notify.Targets["tg"] = &policy.NotifyTarget{URL: discordHook, Format: policy.NotifyFormatDiscord,
		BotTokenEnv: "THEATARR_TG_TOKEN"}
	e.rebuild()
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 0 || len(e.doer.reqs) != before {
		t.Errorf("a Telegram message to Discord: sent %d %v", sent, err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:tg' AND state = 'dropped'`); n != 2 {
		t.Errorf("dropped %d", n)
	}
	// The bot token never goes anywhere but Telegram's address, even on a target that names it after a format change.
	if err := e.n.SendTest(ctx, "tg"); err != nil {
		t.Fatal(err)
	}
	if r := e.doer.reqs[len(e.doer.reqs)-1]; r.url != discordHook || strings.Contains(fmt.Sprint(r), tgToken) {
		t.Errorf("discord request %+v", r)
	}
	// No token: nothing is sent, and the error says so without naming anything secret.
	e.pol.Notify.Targets["tg"] = &policy.NotifyTarget{Format: policy.NotifyFormatTelegram, ChatID: tgChat, BotTokenEnv: "THEATARR_TG_UNSET"}
	e.rebuild()
	e.now = e.now.Add(time.Minute)
	before = len(e.doer.reqs)
	if err := e.n.SendTest(ctx, "tg"); err == nil || !strings.Contains(err.Error(), "bot token") || len(e.doer.reqs) != before {
		t.Errorf("no token: %v", err)
	}
}

// One message every 3 s per chat, Test included; deliveries due sooner wait in the outbox (no attempt counted) and
// the sender is never held.
func TestTelegramPace(t *testing.T) {
	t.Parallel()
	e := newTelegramEnv(t, nil)
	ctx := context.Background()
	var slept []time.Duration
	e.n.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	for range 3 {
		if err := e.n.Test(ctx, "tg"); err != nil {
			t.Fatal(err)
		}
	}
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 1 || len(slept) != 0 {
		t.Fatalf("first round sent %d %v, slept %v", sent, err, slept)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE state = 'pending' AND attempts = 0 AND next_at = ?`,
		e.now.Add(3*time.Second).UnixMilli()); n != 2 {
		t.Errorf("paced deliveries %d", n)
	}
	e.now = e.now.Add(3 * time.Second)
	if sent, _ := e.n.SendDue(ctx, 50); sent != 1 {
		t.Errorf("second round sent %d", sent)
	}
	// Send test right after waits for the chat's next slot instead of failing.
	e.now = e.now.Add(time.Second)
	if err := e.n.SendTest(ctx, "tg"); err != nil || len(slept) != 1 || slept[0] != 2*time.Second {
		t.Errorf("test: %v, slept %v", err, slept)
	}
	// The remaining delivery waits behind the Test's slot.
	if sent, _ := e.n.SendDue(ctx, 50); sent != 0 {
		t.Errorf("sent %d inside the test's slot", sent)
	}
	e.now = e.now.Add(5 * time.Second)
	if sent, _ := e.n.SendDue(ctx, 50); sent != 1 {
		t.Errorf("last round sent %d", sent)
	}
}

// A 429 names its wait in parameters.retry_after: the delivery is retried after it, the chat waits as long, and the
// sender goes on with other targets meanwhile.
func TestTelegramRetryAfter(t *testing.T) {
	t.Parallel()
	e := newTelegramEnv(t, nil)
	ctx := context.Background()
	e.doer.status = 429
	e.doer.answer = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 17","parameters":{"retry_after":17}}`
	if err := e.n.Test(ctx, "tg"); err != nil {
		t.Fatal(err)
	}
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 0 {
		t.Fatalf("sent %d %v", sent, err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE state = 'pending' AND attempts = 1 AND last_status = 429
		AND next_at = ?`, e.now.Add(17*time.Second).UnixMilli()); n != 1 {
		t.Errorf("not retried after 17 s: %d", n)
	}
	// The chat's slot moved too: Send test says how long to wait instead of holding the page.
	var slept []time.Duration
	e.n.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	e.doer.status, e.doer.answer = 0, ""
	before := len(e.doer.reqs)
	if err := e.n.SendTest(ctx, "tg"); err == nil || !strings.Contains(err.Error(), "try again in 17 s") || len(slept) != 0 || len(e.doer.reqs) != before {
		t.Errorf("test after 429: %v %v", err, slept)
	}
	e.now = e.now.Add(17 * time.Second)
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 1 {
		t.Errorf("retry after the wait: %d %v", sent, err)
	}
	e.now = e.now.Add(telegramPace)
	// A Send test answered with 429 says how long to wait.
	e.doer.status = 429
	e.doer.answer = `{"ok":false,"error_code":429,"parameters":{"retry_after":5}}`
	if err := e.n.SendTest(ctx, "tg"); err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "5 s") {
		t.Errorf("test 429: %v", err)
	}
}

// The bot token is in no log line and no stored error: the gateway shows a webhook as scheme and host and masks the
// bot token secret.
func TestTelegramTokenNotLogged(t *testing.T) {
	e := newNEnv(t, true)
	ctx := context.Background()
	t.Setenv("THEATARR_TG_TOKEN_LOG", tgToken)
	e.pol.Notify.Targets["tg"] = &policy.NotifyTarget{Format: policy.NotifyFormatTelegram, ChatID: tgChat, BotTokenEnv: "THEATARR_TG_TOKEN_LOG"}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture: connection refused " + tgToken)
	}
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.10")}, nil
	}
	gw, err := gateway.New(ctx, e.pol, gateway.Options{DialContext: dial, LookupIP: lookup, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	e.env["THEATARR_TG_TOKEN_LOG"] = tgToken
	e.n.doer, e.n.log = gw, log.With("component", "notify")
	if err := e.n.Test(ctx, "tg"); err != nil {
		t.Fatal(err)
	}
	testErr := e.n.SendTest(ctx, "tg")
	if testErr == nil || strings.Contains(testErr.Error(), tgToken) {
		t.Errorf("test error %v", testErr)
	}
	for range 3 {
		if _, err := e.n.SendDue(ctx, 50); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE notify_delivery SET next_at = 0`)
		e.now = e.now.Add(time.Minute)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:tg' AND last_error <> ''`); n != 1 {
		t.Errorf("no failure recorded: %d", n)
	}
	e.telegramSent()
	if text := logs.String(); strings.Contains(text, tgToken) || strings.Contains(text, "/bot") || !strings.Contains(text, "api.telegram.org") {
		t.Fatalf("logs keep the token or lost the host:\n%s", text)
	}
}

// PlayingOutcome: the three kinds go at once to Discord, ntfy, Telegram and native JSON targets (theatarr.playing_*
// with the outcome in snake_case under playing), under the policy event playing and the library filter; never to
// arr views.
func TestPlayingOutcome(t *testing.T) {
	t.Parallel()
	e := newTelegramEnv(t, func(p *policy.Policy) {
		p.Notify.Targets["chan"] = &policy.NotifyTarget{URL: discordHook, Format: policy.NotifyFormatDiscord}
		p.Notify.Targets["phone"] = &policy.NotifyTarget{URL: ntfyURL, Format: policy.NotifyFormatNtfy}
		p.Notify.Targets["playing-only"] = &policy.NotifyTarget{URL: "http://192.168.1.52/hook", Native: true, Events: []string{"playing"}}
		p.Notify.Targets["other-lib"] = &policy.NotifyTarget{URL: discordHook, Format: policy.NotifyFormatDiscord, Versions: []string{"series-web"}}
		p.Versions["series-web"] = &policy.VersionPolicy{Kind: policy.KindSeries}
	})
	ctx := context.Background()
	for _, k := range []string{outcome.Replaced, outcome.SameRelease, outcome.Unavailable, outcome.Restored} {
		o := playingOutcome(k)
		o.Episodes = nil
		e.n.PlayingOutcome(ctx, o)
	}
	e.n.PlayingOutcome(ctx, outcome.Outcome{Kind: "bogus", TitleID: 1})
	count := func(q string) int64 { return e.one(`SELECT COUNT(*) FROM notify_delivery WHERE ` + q) }
	for target, want := range map[string]int64{"tg": 4, "chan": 4, "phone": 4, "automation": 4, "playing-only": 4,
		"grabs-only": 0, "other-lib": 0} {
		if n := count(`target = 'policy:` + target + `'`); n != want {
			t.Errorf("%s got %d, want %d", target, n, want)
		}
	}
	if n := count(`target LIKE 'notification:%' OR (target = 'policy:automation' AND event NOT LIKE 'theatarr.playing_%')`); n != 0 {
		t.Errorf("arr payloads %d", n)
	}
	for ev, want := range map[string]int64{"theatarr.playing_replaced": 4, "theatarr.playing_unavailable": 2,
		"theatarr.playing_restored": 2, "discord.playing_replaced": 2, "ntfy.playing_unavailable": 1, "telegram.playing_restored": 1} {
		if n := count(`event = '` + ev + `'`); n != want {
			t.Errorf("%s: %d, want %d", ev, n, want)
		}
	}
	if got := e.telegramSent(); len(got) != 4 || !strings.Contains(got[1].Text, "Readable again") {
		t.Errorf("telegram %+v", got)
	}
	var raw string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM notify_delivery WHERE target = 'policy:playing-only' ORDER BY id LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var native struct {
		EventType string          `json:"eventType"`
		Version   string          `json:"version"`
		Playing   map[string]any  `json:"playing"`
		Title     map[string]any  `json:"title"`
		Extra     json.RawMessage `json:"health"`
	}
	if err := json.Unmarshal([]byte(raw), &native); err != nil {
		t.Fatal(err)
	}
	p := native.Playing
	if native.EventType != "theatarr.playing_replaced" || native.Version != "movie-web" || native.Title["name"] != "Alpha" ||
		p["incident"] != float64(7) || p["kind"] != "replaced" || p["title_id"] != float64(1) || p["version_key"] != "movie-web" ||
		p["title"] != "Alpha (2020)" || p["old_release"] != "Alpha.2020.1080p.WEB-DL.DDP5.1.H.264-OLD" ||
		p["new_release"] != "Alpha.2020.1080p.BluRay.x264-NEW" || p["account"] != "torbox" || p["viewer"] != "ann" ||
		p["player"] != "Living room TV" || p["at"] != "2026-10-26T12:00:00Z" || native.Extra != nil {
		t.Errorf("native %s", raw)
	}
	if eps, ok := p["episodes"].([]any); !ok || len(eps) != 0 {
		t.Errorf("episodes %v", p["episodes"])
	}
	// Discord: an embed with the title and the releases, markdown characters kept literal.
	got := e.discordSent()
	if len(got) != 4 || got[0].Embeds[0].Title != "Replaced while playing: Alpha (2020)" ||
		!strings.Contains(got[0].Embeds[0].Description, `Alpha.2020.1080p.WEB-DL.DDP5.1.H.264-OLD`) ||
		!strings.Contains(got[2].Embeds[0].Title, "No other copy") {
		t.Errorf("discord %+v", got)
	}
	// ntfy: plain text from the same builder.
	ntfy := e.ntfySent()
	if len(ntfy) != 4 || ntfy[3].Title != "Earlier copy put back: Alpha (2020)" || !strings.Contains(ntfy[3].Message, "ann") {
		t.Errorf("ntfy %+v", ntfy)
	}
}

// Episodes, a missing viewer and Discord's markdown characters.
func TestPlayingText(t *testing.T) {
	t.Parallel()
	o := playingOutcome(outcome.Replaced)
	o.Title, o.Episodes, o.Viewer, o.Player = "Show (2020)", []string{"S02E07", "S02E08"}, "", ""
	o.NewRelease = "Show_S02_*x*"
	title, lines, _ := messageText(Event{Kind: PlayingReplaced, Playing: &o}, true)
	text := strings.Join(lines, "\n")
	if title != "Replaced while playing: Show (2020) S02E07, S02E08" || !strings.Contains(text, "while someone was watching.") ||
		!strings.Contains(text, `Show\_S02\_\*x\*`) {
		t.Errorf("%q %q", title, text)
	}
	if _, plain, _ := messageText(Event{Kind: PlayingReplaced, Playing: &o}, false); !strings.Contains(strings.Join(plain, "\n"), "Show_S02_*x*") {
		t.Errorf("plain %q", plain)
	}
}
