package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

const ntfyURL = "https://ntfy.example/theatarr-alerts"

// newNtfyEnv is newNEnv with an ntfy target "phone" (and no Discord target) and a job queue.
func newNtfyEnv(t *testing.T, edit func(*policy.Policy)) *nenv {
	t.Helper()
	e := newNEnv(t, true)
	e.env["THEATARR_NTFY_AUTH"] = "tk_secret"
	e.pol.Notify.Targets["phone"] = &policy.NotifyTarget{URL: ntfyURL, Format: policy.NotifyFormatNtfy,
		AuthHeaderEnv: "THEATARR_NTFY_AUTH"}
	if edit != nil {
		edit(e.pol)
	}
	e.rebuild()
	return e
}

// ntfySent are the ntfy bodies recorded for phone, oldest first. None holds the topic.
func (e *nenv) ntfySent() []ntfyMessage {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT event, payload FROM notify_delivery WHERE target = 'policy:phone' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []ntfyMessage
	for rows.Next() {
		var event, raw string
		if err := rows.Scan(&event, &raw); err != nil {
			e.t.Fatal(err)
		}
		if !strings.HasPrefix(event, "ntfy.") {
			e.t.Errorf("event %q to an ntfy target", event)
		}
		if strings.Contains(raw, `"topic"`) || strings.Contains(raw, "theatarr-alerts") || strings.Contains(raw, "tk_secret") {
			e.t.Errorf("topic or token stored: %s", raw)
		}
		var m ntfyMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			e.t.Fatal(err)
		}
		if m.Message == "" || m.Priority < 1 || m.Priority > 5 {
			e.t.Errorf("message %+v", m)
		}
		out = append(out, m)
	}
	return out
}

func TestNtfyText(t *testing.T) {
	t.Parallel()
	was := &HealthItem{Source: "MountCheck", Type: "error", Message: "mount not readable"}
	for _, c := range []struct {
		name string
		ev   Event
		want ntfyMessage
	}{
		{"failing", Event{Kind: Health, Health: []HealthItem{*was}},
			ntfyMessage{"Health check failing: MountCheck", "mount not readable", 4, []string{"warning"}}},
		{"eased", Event{Kind: Health, Health: []HealthItem{{Source: "MountCheck", Type: "warning", Message: "mount slow"}}, HealthWas: was},
			ntfyMessage{"Health check no longer failing, now warning: MountCheck", "Now: mount slow\nWas: mount not readable", 3, []string{"warning"}}},
		{"restored", Event{Kind: HealthRestored, Health: []HealthItem{*was}},
			ntfyMessage{"Health check OK again: MountCheck", "Was: mount not readable", 3, []string{"white_check_mark"}}},
		{"error", Event{Kind: Error, Error: &ErrorItem{ID: 7, Source: "log", Component: "router", Sample: "route `failed`", Count: 2}},
			ntfyMessage{"New error: router", "route `failed`\nSource log · seen 2 times · Admin › System › Errors #7", 4, []string{"warning"}}},
		{"error again", Event{Kind: Error, Error: &ErrorItem{ID: 7, Source: "log", Component: "router", Sample: "x", Count: 1, Reopened: true}},
			ntfyMessage{"Error back after it was resolved: router", "x\nSource log · seen 1 times · Admin › System › Errors #7", 4, []string{"warning"}}},
		{"quiet digest", Event{Kind: Digest, Digest: &DigestSummary{Day: "2026-11-06"}},
			ntfyMessage{"Theatarr daily digest · 2026-11-06", "Quiet day: nothing grabbed, imported, failed or blocklisted; no dead links, open errors or health problems.", 3, nil}},
		{"digest", Event{Kind: Digest, Digest: &DigestSummary{Day: "2026-11-06", Grabs: 3, Imports: 1, Grabbed: []string{"A (2020)", "B (2021)"},
			Imported: []string{"A (2020)"}, OpenErrors: 1, Errors: []string{"router: boom (1×)"}, GrabbingOff: true}},
			ntfyMessage{"Theatarr daily digest · 2026-11-06", "Last 24 h: 3 grabbed · 1 imported · 0 failed · 0 blocklisted · 0 dead links found\n" +
				"Grabbed: A (2020), B (2021) and 1 more\nImported: A (2020)\nOpen error groups: 1 (Admin › System › Errors)\n• router: boom (1×)\n" +
				"Theatarr is paused: nothing is downloaded or linked.", 3, []string{"warning"}}},
		{"test", Event{Kind: Test}, ntfyMessage{"Theatarr test message", "This topic gets Theatarr's notifications.", 3, nil}},
	} {
		got, err := (&Notifier{}).ntfyPayload(context.Background(), c.ev)
		if err != nil || fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", c.want) {
			t.Errorf("%s: %v\n got %+v\nwant %+v", c.name, err, got, c.want)
		}
	}
	if _, err := (&Notifier{}).ntfyPayload(context.Background(), Event{Kind: Grab}); err == nil {
		t.Error("a grab has no ntfy message")
	}
	// The body is the JSON publish without its topic.
	b, _ := json.Marshal(fitNtfy("T", []string{"m"}, levelBad))
	if string(b) != `{"title":"T","message":"m","priority":4,"tags":["warning"]}` {
		t.Errorf("body %s", b)
	}
}

func TestNtfyFit(t *testing.T) {
	t.Parallel()
	// A long digest fits in bytes, not characters, and says how much was left out.
	var lines []string
	for i := range 400 {
		lines = append(lines, fmt.Sprintf("• Ünïcödé title %d ééééééééééééééééééééééé", i))
	}
	m := fitNtfy(strings.Repeat("é", 400), lines, levelInfo)
	if len(m.Message) > ntfyMessageMax || len(m.Message) < ntfyMessageMax/2 || !strings.Contains(m.Message, " more") {
		t.Errorf("message %d bytes: …%q", len(m.Message), m.Message[max(0, len(m.Message)-40):])
	}
	if len(m.Title) > ntfyTitleMax || !strings.HasSuffix(m.Title, "…") {
		t.Errorf("title %d bytes", len(m.Title))
	}
	if m := fitNtfy("A", []string{strings.Repeat("語", 5000)}, levelInfo); len(m.Message) > ntfyMessageMax || !strings.HasSuffix(m.Message, "…") {
		t.Errorf("one long line: %d bytes", len(m.Message))
	}
	// Never an empty message.
	if m := fitNtfy("Health check failing: X", []string{""}, levelBad); m.Message != "Health check failing: X" {
		t.Errorf("empty message: %+v", m)
	}
}

func TestNtfyEndpointAndAuthorization(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string][2]string{
		"https://ntfy.example/alerts":           {"https://ntfy.example/", "alerts"},
		"https://ntfy.example/alerts/":          {"https://ntfy.example/", "alerts"},
		"http://ntfy.example:8080/sub/my_topic": {"http://ntfy.example:8080/sub/", "my_topic"},
	} {
		server, topic, ok := policy.NtfyEndpoint(raw)
		if !ok || server != want[0] || topic != want[1] {
			t.Errorf("%s: %q %q %v", raw, server, topic, ok)
		}
	}
	for _, raw := range []string{"https://ntfy.example", "https://ntfy.example/", "https://ntfy.example//", "", "/alerts", "::"} {
		if _, _, ok := policy.NtfyEndpoint(raw); ok {
			t.Errorf("%q has no topic", raw)
		}
	}
	for in, want := range map[string]string{"tk_abc": "Bearer tk_abc", " tk_abc\n": "Bearer tk_abc", "Bearer tk_abc": "Bearer tk_abc",
		"Basic dTpw": "Basic dTpw", "": ""} {
		if got := ntfyAuthorization(in); got != want {
			t.Errorf("authorization %q: %q", in, got)
		}
	}
}

// An install with an ntfy target and no Discord target gets health, error, digest and request-ready messages, and
// no grab or download.
func TestNtfyTargetGetsMessages(t *testing.T) {
	t.Parallel()
	e := newNtfyEnv(t, func(p *policy.Policy) {
		p.Notify.Targets["errors-only"] = &policy.NotifyTarget{URL: "https://ntfy.example/errors", Format: policy.NotifyFormatNtfy,
			Events: []string{"error"}}
	})
	ctx := context.Background()
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	e.n.Emit(ctx, Event{Kind: Grab, TitleID: 1, Version: "movie-web"})
	e.n.Emit(ctx, Event{Kind: Download, TitleID: 1, Version: "movie-web"})
	e.hlth = []HealthItem{{Source: "MountCheck", Type: "error", Message: "mount not readable"},
		{Source: "DiskSpaceCheck", Type: "warning", Message: "disk 91% full"}}
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.ntfySent()
	if len(got) != 1 || got[0].Title != "Health check failing: MountCheck" || got[0].Message != "mount not readable" || got[0].Priority != 4 {
		t.Fatalf("health (a warning, a grab and a download are not messages): %+v", got)
	}
	insertError(e, "f1")
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.n.SendDigest(ctx, e.now); err != nil {
		t.Fatal(err)
	}
	e.n.Emit(ctx, Event{Kind: RequestPending, TitleID: 1, Request: &Request{ID: 7, Name: "Alpha", TitleID: 1}})
	e.n.Emit(ctx, Event{Kind: RequestAvailable, TitleID: 1, Request: &Request{ID: 7, Name: "Alpha", Kind: "movie", TitleID: 1,
		Status: "available", Users: []RequestUser{{ID: 1, Username: "ann", DisplayName: "Ann", DiscordID: "123456789012345678"},
			{ID: 2, Username: "bob"}}}})
	e.n.Emit(ctx, Event{Kind: RequestPartial, TitleID: 1, Request: &Request{ID: 8, Name: "Alpha", TitleID: 1}})
	got = e.ntfySent()
	if len(got) != 5 {
		t.Fatalf("messages %d: %+v", len(got), got)
	}
	if got[1].Title != "New error: router" || !strings.HasPrefix(got[1].Message, "boom in f1\n") || got[1].Priority != 4 {
		t.Errorf("error %+v", got[1])
	}
	if !strings.HasPrefix(got[2].Title, "Theatarr daily digest · ") || !strings.Contains(got[2].Message, "Open error groups: 1") ||
		strings.Contains(got[2].Message, "**") {
		t.Errorf("digest %+v", got[2])
	}
	// Request ready names the requesters; a Discord user id never leaves for ntfy.
	if got[3].Title != "Request ready" || got[3].Message != "Alpha (2020) is ready in Plex, requested by Ann and bob" ||
		len(got[3].Tags) != 1 || got[3].Tags[0] != "white_check_mark" {
		t.Errorf("ready %+v", got[3])
	}
	if got[4].Title != "Request partly ready" || got[4].Message != "Alpha (2020) is partly ready in Plex" {
		t.Errorf("partial %+v", got[4])
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE payload LIKE '%123456789012345678%'`); n != 0 {
		t.Errorf("discord id in %d payloads", n)
	}
	// The events filter applies; other targets are as before: no error or digest to a JSON target.
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:errors-only'`); n != 1 {
		t.Errorf("errors-only got %d", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:errors-only' AND event = 'ntfy.error'`); n != 1 {
		t.Errorf("errors-only error %d", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target NOT IN ('policy:phone', 'policy:errors-only')
		AND (event LIKE '%error%' OR event LIKE '%digest%' OR event LIKE 'ntfy.%')`); n != 0 {
		t.Errorf("message events to other targets: %d", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:automation' AND event = 'theatarr.grab'`); n != 1 {
		t.Errorf("native grab %d", n)
	}
}

func TestNtfySend(t *testing.T) {
	t.Parallel()
	e := newNtfyEnv(t, nil)
	ctx := context.Background()
	if err := e.n.SendTest(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	r := e.doer.reqs[0]
	if r.method != "POST" || r.url != "https://ntfy.example/" || r.target.Key != "phone" || r.auth != "Bearer tk_secret" {
		t.Errorf("request %+v", r)
	}
	if r.body["topic"] != "theatarr-alerts" || r.body["title"] != "Theatarr test message" ||
		r.body["message"] != "This topic gets Theatarr's notifications." || r.body["priority"] != float64(3) {
		t.Errorf("body %v", r.body)
	}
	if _, ok := r.body["eventType"]; ok {
		t.Errorf("theatarr JSON to ntfy: %v", r.body)
	}
	// Through the outbox: the stored payload has no topic (ntfySent checks), the sent one has the topic of the URL
	// in force, here a server under a sub-path with no token.
	if err := e.n.Test(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if got := e.ntfySent(); len(got) != 1 {
		t.Fatalf("queued %d", len(got))
	}
	e.pol.Notify.Targets["phone"] = &policy.NotifyTarget{URL: "https://ntfy.example/push/other", Format: policy.NotifyFormatNtfy}
	e.rebuild()
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 1 {
		t.Fatalf("sent %d %v", sent, err)
	}
	r = e.doer.reqs[1]
	if r.url != "https://ntfy.example/push/" || r.body["topic"] != "other" || r.auth != "" {
		t.Errorf("request %+v", r)
	}
	// A failure is retried like any delivery, and its stored error names no topic.
	e.doer.status = 500
	if err := e.n.Test(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if sent, _ := e.n.SendDue(ctx, 50); sent != 0 {
		t.Errorf("sent %d on 500", sent)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:phone' AND state = 'pending' AND attempts = 1
		AND last_status = 500 AND last_error NOT LIKE '%other%'`); n != 1 {
		t.Errorf("500 not retried: %d", n)
	}
	// A delivery queued while the target had another format is dropped, not posted to the ntfy server.
	e.doer.status = 0
	e.exec(`UPDATE notify_delivery SET state = 'sent' WHERE target = 'policy:phone'`)
	e.exec(`INSERT INTO notify_delivery (target, event, view, payload, state, next_at, last_error, created_at)
		VALUES ('policy:phone', 'discord.test', '', '{"content":"x","allowed_mentions":{"parse":[]}}', 'pending', 0, '', 0)`)
	before := len(e.doer.reqs)
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 0 || len(e.doer.reqs) != before {
		t.Errorf("another format's delivery: sent %d %v, requests %d", sent, err, len(e.doer.reqs)-before)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'discord.test' AND state = 'dropped'`); n != 1 {
		t.Errorf("not dropped: %d", n)
	}
	// So is a native request notification, whose body also has a "message" field, queued before the switch to ntfy;
	// and an ntfy message queued before the target became a JSON webhook.
	e.pol.Notify.Targets["phone"] = &policy.NotifyTarget{URL: "https://hooks.example/hook", Native: true}
	e.rebuild()
	e.n.Emit(ctx, Event{Kind: RequestPending, TitleID: 1, Request: &Request{ID: 7, Name: "Alpha", TitleID: 1, Status: "pending"}})
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:phone' AND event = 'theatarr.request_pending'
		AND state = 'pending' AND payload LIKE '%"message"%'`); n != 1 {
		t.Fatalf("native request notification queued: %d", n)
	}
	e.pol.Notify.Targets["phone"] = &policy.NotifyTarget{URL: ntfyURL, Format: policy.NotifyFormatNtfy}
	e.rebuild()
	e.exec(`UPDATE notify_delivery SET state = 'sent' WHERE target != 'policy:phone'`)
	before = len(e.doer.reqs)
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 0 || len(e.doer.reqs) != before {
		t.Errorf("a native request notification to ntfy: sent %d %v, requests %d", sent, err, len(e.doer.reqs)-before)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'theatarr.request_pending' AND target = 'policy:phone'
		AND state = 'dropped'`); n != 1 {
		t.Errorf("native request notification not dropped: %d", n)
	}
	if err := e.n.Test(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	e.pol.Notify.Targets["phone"] = &policy.NotifyTarget{URL: "https://hooks.example/hook", Native: true}
	e.rebuild()
	if sent, err := e.n.SendDue(ctx, 50); err != nil || sent != 0 || len(e.doer.reqs) != before {
		t.Errorf("an ntfy message to a webhook: sent %d %v, requests %d", sent, err, len(e.doer.reqs)-before)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = 'ntfy.test' AND state = 'dropped'`); n != 1 {
		t.Errorf("ntfy message not dropped: %d", n)
	}
	// Other targets are sent as before: to their URL, with the header value as stored.
	if err := e.n.SendTest(ctx, "automation"); err != nil {
		t.Fatal(err)
	}
	if r := e.doer.reqs[len(e.doer.reqs)-1]; r.url != "http://192.168.1.50:8080/hook" || r.auth != "Bearer secret" || r.body["topic"] != nil {
		t.Errorf("webhook request %+v", r)
	}
}

func TestNtfyTargetValidation(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Views: map[string]*policy.View{"radarr": {}}, Notify: &policy.Notify{
		Targets: map[string]*policy.NotifyTarget{
			"phone":   {URL: ntfyURL, Format: policy.NotifyFormatNtfy, Events: []string{"error", "digest"}},
			"views":   {URL: ntfyURL, Format: policy.NotifyFormatNtfy, Views: []string{"radarr"}},
			"notopic": {URL: "https://ntfy.example/", Format: policy.NotifyFormatNtfy},
			"chan":    {URL: discordHook, Format: policy.NotifyFormatDiscord},
			"hook":    {URL: "https://hooks.example/", Native: true},
		}}}
	got := fmt.Sprint(p.Validate())
	for _, want := range []string{"targets[views].views: an ntfy target", "targets[notopic].url: an ntfy target's URL ends in the topic"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, not := range []string{"targets[phone]", "targets[chan]", "targets[hook]", "ntfy.example"} {
		if strings.Contains(got, not) {
			t.Errorf("%q in %s", not, got)
		}
	}
	p.Notify.Targets = map[string]*policy.NotifyTarget{"odd": {URL: ntfyURL, Format: "gotify"}}
	if got := fmt.Sprint(p.Validate()); !strings.Contains(got, `unknown format "gotify" (want discord or ntfy`) {
		t.Errorf("unknown format: %s", got)
	}
}
