package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

const discordHook = "https://discord.com/api/webhooks/123/token"

// newDiscordEnv is newNEnv with a Discord target "chan" and a job queue, its notifier built after the policy edit.
func newDiscordEnv(t *testing.T, enabled bool, edit func(*policy.Policy)) *nenv {
	t.Helper()
	e := newNEnv(t, enabled)
	e.pol.Notify.Targets["chan"] = &policy.NotifyTarget{URL: discordHook, Format: policy.NotifyFormatDiscord}
	if edit != nil {
		edit(e.pol)
	}
	e.rebuild()
	return e
}

// rebuild makes a new notifier on the same database, as a restart does.
func (e *nenv) rebuild() {
	e.t.Helper()
	n, err := New(Config{Store: e.st, Policy: e.pol, Doer: e.doer, Payloads: fakePayloads{},
		Health: func(context.Context) []HealthItem { return e.hlth },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now },
		Getenv: func(k string) string { return e.env[k] }, Jobs: jobs.NewQueue(e.st)})
	if err != nil {
		e.t.Fatal(err)
	}
	e.n = n
}

// discordSent are the Discord bodies recorded for chan, oldest first.
func (e *nenv) discordSent() []discordMessage {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM notify_delivery WHERE target = 'policy:chan' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []discordMessage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var m discordMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			e.t.Fatal(err)
		}
		if !strings.Contains(raw, `"allowed_mentions":{"parse":[]`) {
			e.t.Errorf("mentions not limited: %s", raw)
		}
		out = append(out, m)
	}
	return out
}

func text(m discordMessage) string {
	s := m.Content
	for _, e := range m.Embeds {
		s += "\n" + e.Title + "\n" + e.Description
	}
	return s
}

func TestDiscordTargetValidation(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Views: map[string]*policy.View{"radarr": {}}, Notify: &policy.Notify{DigestAt: "25:00",
		Targets: map[string]*policy.NotifyTarget{
			"chan":  {URL: discordHook, Format: policy.NotifyFormatDiscord},
			"views": {URL: discordHook, Format: policy.NotifyFormatDiscord, Views: []string{"radarr"}},
			"odd":   {URL: discordHook, Format: "slack", Native: true},
		}}}
	err := p.Validate()
	got := fmt.Sprint(err)
	for _, want := range []string{"notify.digest_at", "targets[views].views", `unknown format "slack"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "targets[chan]") {
		t.Errorf("a Discord target without views or native refused: %s", got)
	}
	s := (&policy.Policy{Notify: &policy.Notify{DigestAt: "18:30"}}).NotifySettings()
	if s.DigestHour != 18 || s.DigestMinute != 30 {
		t.Errorf("digest_at %d:%d", s.DigestHour, s.DigestMinute)
	}
	if d := (&policy.Policy{}).NotifySettings(); d.DigestHour != 9 || d.DigestMinute != 0 {
		t.Errorf("default digest %d:%d", d.DigestHour, d.DigestMinute)
	}
}

func TestDiscordFit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 3000)
	m := fitDiscord(long, nil, nil, "")
	if n := utf8.RuneCountInString(m.Content); n != discordContentMax || !strings.HasSuffix(m.Content, "…") {
		t.Errorf("content %d", n)
	}
	var lines []string
	for i := range 500 {
		lines = append(lines, fmt.Sprintf("line %03d %s", i, strings.Repeat("y", 20)))
	}
	var embeds []embedLines
	for i := range 12 {
		embeds = append(embeds, embedLines{title: fmt.Sprintf("embed %d", i), lines: lines})
	}
	m = fitDiscord("", embeds, nil, "")
	if len(m.Embeds) > discordEmbedsMax {
		t.Fatalf("%d embeds", len(m.Embeds))
	}
	total := 0
	for _, e := range m.Embeds {
		if utf8.RuneCountInString(e.Description) > discordDescriptionMax {
			t.Errorf("description %d", utf8.RuneCountInString(e.Description))
		}
		total += utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description)
	}
	if total > discordEmbedTotalMax {
		t.Errorf("embed text %d", total)
	}
	first := m.Embeds[0].Description
	if !strings.HasPrefix(first, "line 000") || !strings.Contains(first, " more") {
		t.Errorf("first embed %q", first[len(first)-40:])
	}
	tail := first[strings.LastIndex(first, "\n")+1:]
	kept := strings.Count(first, "\n")
	if tail != fmt.Sprintf("and %d more", 500-kept) {
		t.Errorf("tail %q after %d lines", tail, kept)
	}
	// Twelve embeds: nine and "and 3 more".
	short := make([]embedLines, 12)
	for i := range short {
		short[i] = embedLines{title: fmt.Sprint(i), lines: []string{"a"}}
	}
	m = fitDiscord("", short, nil, "")
	if len(m.Embeds) != 10 || m.Embeds[9].Description != "and 3 more" {
		t.Errorf("embeds %+v", m.Embeds)
	}
	if got := fitLines([]string{strings.Repeat("z", 50), "b", "c"}, 20); utf8.RuneCountInString(got) > 20 || !strings.HasSuffix(got, "and 2 more") {
		t.Errorf("first line cut: %q", got)
	}
}

func TestDiscordHealthAndErrorsGoToDiscordOnly(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, nil)
	ctx := context.Background()
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	e.hlth = []HealthItem{{Source: "MountCheck", Type: "error", Message: "mount not readable"},
		{Source: "DiskSpaceCheck", Type: "warning", Message: "disk 91% full"}}
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	e.hlth = []HealthItem{{Source: "DiskSpaceCheck", Type: "warning", Message: "disk 91% full"}}
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.discordSent()
	if len(got) != 2 {
		t.Fatalf("discord messages %d: %+v (a warning is not an alert)", len(got), got)
	}
	if g := got[0].Embeds[0]; g.Title != "Health check failing: MountCheck" || g.Description != "mount not readable" || g.Color != discordRed {
		t.Errorf("failing %+v", g)
	}
	if g := got[1].Embeds[0]; g.Title != "Health check OK again: MountCheck" || !strings.Contains(g.Description, "mount not readable") {
		t.Errorf("restored %+v", g)
	}
	// Native targets keep their theatarr.health and never get errors or digests.
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:automation' AND event LIKE 'theatarr.health%'`); n == 0 {
		t.Error("native health gone")
	}
	e.exec(`INSERT INTO error_event (fingerprint, source, component, level, message, sample, count, first_seen,
		last_seen, status, updated_at) VALUES ('f1', 'log', 'router', 'error', 'route failed', 'route failed for title 1',
		1, 0, 0, 'new', 0)`)
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event LIKE '%error%' AND target != 'policy:chan'`); n != 0 {
		t.Errorf("errors to other targets: %d", n)
	}
	if n := len(e.discordSent()); n != 3 {
		t.Errorf("error alert missing: %d", n)
	}
}

// An error that eases to a warning is announced once as no longer failing; the warning clearing after it is quiet.
func TestDiscordHealthLeavingErrorThroughWarning(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, nil)
	ctx := context.Background()
	for _, h := range [][]HealthItem{nil,
		{{Source: "MountCheck", Type: "error", Message: "mount not readable"}},
		{{Source: "MountCheck", Type: "warning", Message: "mount slow"}},
		{{Source: "MountCheck", Type: "warning", Message: "mount slow"}},
		nil,
		// A warning that was never an error, and its recovery, are not alerts.
		{{Source: "DiskSpaceCheck", Type: "warning", Message: "disk 91% full"}},
		nil} {
		e.hlth = h
		if err := e.n.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := e.discordSent()
	if len(got) != 2 {
		t.Fatalf("discord messages %d: %+v", len(got), got)
	}
	if g := got[0].Embeds[0]; g.Title != "Health check failing: MountCheck" {
		t.Errorf("failing %+v", g)
	}
	if g := got[1].Embeds[0]; g.Title != "Health check no longer failing, now warning: MountCheck" ||
		g.Description != "Now: mount slow\nWas: mount not readable" || g.Color != discordAmber {
		t.Errorf("eased %+v", g)
	}
	// Native targets keep every transition.
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:automation' AND event LIKE 'theatarr.health%'`); n != 5 {
		t.Errorf("native health deliveries %d", n)
	}
}

func insertError(e *nenv, fp string) int64 {
	e.t.Helper()
	res, err := e.st.Writer().Exec(`INSERT INTO error_event (fingerprint, source, component, level, message, sample,
		count, first_seen, last_seen, status, updated_at) VALUES (?, 'log', 'router', 'error', 'boom', ?, 1, ?, ?, 'new', ?)`,
		fp, "boom in "+fp, store.Millis(e.now), store.Millis(e.now), store.Millis(e.now))
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestDiscordErrorAlertsNewAndCapped(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, nil)
	ctx := context.Background()
	old := insertError(e, "before")
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.discordSent()); n != 0 {
		t.Fatalf("the baseline was announced: %d", n)
	}
	// An error storm: six new groups, three alerts this hour.
	var ids []int64
	for i := range 6 {
		ids = append(ids, insertError(e, fmt.Sprint("storm", i)))
	}
	for range 2 {
		if err := e.n.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := e.discordSent()
	if len(got) != errorAlertsPerHour {
		t.Fatalf("alerts %d, want %d", len(got), errorAlertsPerHour)
	}
	if g := got[0].Embeds[0]; g.Title != "New error: router" || !strings.Contains(g.Description, "boom in storm0") ||
		!strings.Contains(g.Description, fmt.Sprintf("#%d", ids[0])) {
		t.Errorf("alert %+v", g)
	}
	// A repeat of an open group is not announced, even after the hour.
	e.now = e.now.Add(2 * time.Hour)
	e.exec(`UPDATE error_event SET count = count + 5 WHERE id = ?`, ids[0])
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.discordSent()); n != errorAlertsPerHour {
		t.Errorf("a repeat (or a held group) was announced: %d", n)
	}
	// A resolved group that happens again is new again: announced as back. So is the old baseline one.
	for _, id := range []int64{ids[0], old} {
		e.exec(`UPDATE error_event SET status = 'new', regressions = regressions + 1 WHERE id = ?`, id)
	}
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	got = e.discordSent()
	if len(got) != errorAlertsPerHour+2 || got[len(got)-1].Embeds[0].Title != "Error back after it was resolved: router" {
		t.Fatalf("regressions: %d %+v", len(got), got[len(got)-1])
	}
}

func TestDiscordRequestReady(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, nil)
	ctx := context.Background()
	req := func(kind Kind) Event {
		return Event{Kind: kind, TitleID: 1, Request: &Request{ID: 7, Name: "Alpha", Kind: "movie", TitleID: 1,
			Status: "available", Versions: []RequestVersion{{Key: "movie-web"}},
			Users: []RequestUser{{ID: 1, Username: "ann", DisplayName: "Ann", DiscordID: "123456789012345678"},
				{ID: 2, Username: "bob"}}}}
	}
	e.n.Emit(ctx, req(RequestPending))
	e.n.Emit(ctx, req(RequestAvailable))
	ev := req(RequestPartial)
	ev.Request.Users = ev.Request.Users[1:]
	e.n.Emit(ctx, ev)
	got := e.discordSent()
	if len(got) != 2 {
		t.Fatalf("messages %d (pending is not a ready message)", len(got))
	}
	if got[0].Content != "**Alpha (2020)** is ready in Plex, requested by Ann (<@123456789012345678>) and bob" {
		t.Errorf("ready %q", got[0].Content)
	}
	if u := got[0].AllowedMentions.Users; len(u) != 1 || u[0] != "123456789012345678" {
		t.Errorf("mentions %v", u)
	}
	if got[1].Content != "**Alpha (2020)** is partly ready in Plex, requested by bob" || got[1].AllowedMentions.Users != nil {
		t.Errorf("partial %+v", got[1])
	}
	// The native payload never carries the Discord id.
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE payload LIKE '%123456789012345678%' AND target != 'policy:chan'`); n != 0 {
		t.Errorf("discord id in %d native payloads", n)
	}
}

func TestDiscordSendTest(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, false, nil)
	ctx := context.Background()
	if err := e.n.SendTest(ctx, "chan"); err == nil || len(e.doer.reqs) != 0 {
		t.Errorf("test while off: %v %d", err, len(e.doer.reqs))
	}
	e.pol.Notify.Enabled = true
	e.rebuild()
	if err := e.n.SendTest(ctx, "nope"); err == nil {
		t.Error("unknown target")
	}
	if err := e.n.SendTest(ctx, "chan"); err != nil {
		t.Fatal(err)
	}
	r := e.doer.reqs[0]
	if r.url != discordHook || r.target.Key != "chan" || !strings.HasPrefix(r.body["content"].(string), "Theatarr test message") {
		t.Errorf("test %+v", r)
	}
	if _, ok := r.body["eventType"]; ok {
		t.Errorf("theatarr JSON to Discord: %v", r.body)
	}
	e.doer.status = 429
	if err := e.n.SendTest(ctx, "chan"); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("429: %v", err)
	}
	// The queued Test goes through the outbox; a 429 is retried with back-off.
	if err := e.n.Test(ctx, "chan"); err != nil {
		t.Fatal(err)
	}
	if sent, _ := e.n.SendDue(ctx, 50); sent != 0 {
		t.Errorf("sent %d on 429", sent)
	}
	if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:chan' AND event = 'discord.test'
		AND state = 'pending' AND attempts = 1 AND last_status = 429`); n != 1 {
		t.Errorf("429 not retried: %d", n)
	}
}

func TestDigest(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, func(p *policy.Policy) { p.Notify.DigestAt = "09:00" })
	ctx := context.Background()
	e.now = time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	now := store.Millis(e.now)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	// A quiet day says so, and that Theatarr is paused.
	if err := e.n.SendDigest(ctx, day.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	got := e.discordSent()
	if len(got) != 1 || !strings.Contains(text(got[0]), "Quiet day") || !strings.Contains(text(got[0]), "Theatarr is paused") ||
		got[0].Embeds[0].Title != "Theatarr daily digest · 2026-11-06" {
		t.Fatalf("quiet %+v", got)
	}
	for i := range 12 {
		e.exec(`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (?, 'movie', ?, 2021, 0, 0)`,
			100+i, fmt.Sprint("Movie ", i))
		e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
			(?, ?, 'movie-web', 1, 'wanted', 0, 0)`, 200+i, 100+i)
		state, imported := "added", any(nil)
		if i < 2 {
			state, imported = "imported", now
		}
		if i == 11 {
			state = "failed"
		}
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
			created_at, updated_at, added_at, imported_at) VALUES (?, ?, 'movie-web', ?, 'r', 'x', ?, ?, ?, ?, ?)`,
			100+i, 200+i, fmt.Sprint("h", i), state, now, now, now-int64(i), imported)
	}
	// Old activity is outside the window.
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at, added_at) VALUES (1, 2, 'movie-web', 'old', 'r', 'x', 'added', 0, 0, 0)`)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, reason, source, created_at) VALUES
		('movie-web', 'bad', 1, 'failed', 'router', ?)`, now)
	// Dead links found in the window count even when relinked since; one found earlier and a vanished symlink do not.
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, '/l', '/t', 1, 'dead', 0, ?),
		(2, '/r', '/t2', 1, 'active', 0, ?), (3, '/o', '/t3', 1, 'dead', 0, 0), (4, '/v', '/t4', 1, 'unlinked', 0, ?)`, now, now, now)
	e.exec(`INSERT INTO library_action (at, kind, mode, link_id, path, target, detail) VALUES
		(?, 'dead', 'applied', 1, '/l', '/t', 'gone h1'), (?, 'dead', 'applied', 2, '/r', '/t2', 'gone h2'),
		(?, 'dead', 'dry_run', 3, '/o', '/t3', 'gone h3'), (0, 'dead', 'applied', 3, '/o', '/t3', 'gone h3'),
		(?, 'dead', 'applied', 4, '/v', '/t4', 'vanished: the symlink is no longer in the library')`, now, now, now, now)
	insertError(e, "open")
	e.hlth = []HealthItem{{Source: "ProviderAccountCheck", Type: "warning", Message: "Debrid accounts: rd: premium ends 2026-11-10 (in 3d)"},
		{Source: "MountCheck", Type: "error", Message: "mount not readable"},
		{Source: "RouterDisabledCheck", Type: "warning", Message: "Router disabled"},
		{Source: "ShadowModeCheck", Type: "notice", Message: "shadow"}}
	if err := e.n.SendDigest(ctx, day); err != nil {
		t.Fatal(err)
	}
	got = e.discordSent()
	if len(got) != 2 {
		t.Fatalf("digests %d", len(got))
	}
	body := text(got[1])
	for _, want := range []string{"12 grabbed · 2 imported · 1 failed · 1 blocklisted · 2 dead links found, 1 still dead",
		"Movie 0 (2021), Movie 1 (2021)", " and 2 more", "**Imported:** Movie 1 (2021), Movie 0 (2021)",
		"**Open error groups:** 1", "router: boom (1×)", "**Debrid accounts:** Debrid accounts: rd: premium ends",
		"**Health checks not OK:** 1", "error: mount not readable", "Theatarr is paused"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	for _, not := range []string{"Router disabled", "shadow", "Alpha"} {
		if strings.Contains(body, not) {
			t.Errorf("%q in\n%s", not, body)
		}
	}
	// Once per day, across a restart.
	if err := e.n.SendDigest(ctx, day); err != nil {
		t.Fatal(err)
	}
	e.rebuild()
	if err := e.n.SendDigest(ctx, day); err != nil {
		t.Fatal(err)
	}
	if n := len(e.discordSent()); n != 2 {
		t.Errorf("digest sent twice: %d", n)
	}
	// Grabbing on: no line.
	e.pol.Router = &policy.Router{Enabled: true}
	e.rebuild()
	if err := e.n.SendDigest(ctx, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if got := e.discordSent(); strings.Contains(text(got[2]), "Theatarr is paused") {
		t.Errorf("grabbing on: %s", text(got[2]))
	}
}

func TestDigestJobQueuedOncePerDay(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, func(p *policy.Policy) { p.Notify.DigestAt = "09:30" })
	ctx := context.Background()
	jobCount := func() int64 { return e.one(`SELECT COUNT(*) FROM job WHERE kind = ?`, DigestJob) }
	e.now = time.Date(2026, 11, 7, 9, 29, 0, 0, time.Local)
	if err := e.n.queueDigest(ctx); err != nil || jobCount() != 0 {
		t.Fatalf("queued before 09:30: %v %d", err, jobCount())
	}
	e.now = time.Date(2026, 11, 7, 9, 31, 0, 0, time.Local)
	for range 2 {
		if err := e.n.queueDigest(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if jobCount() != 1 {
		t.Fatalf("jobs %d", jobCount())
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ?`, DigestJob).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := e.n.runDigest(ctx, &jobs.Job{Kind: DigestJob, Payload: json.RawMessage(payload)}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.discordSent()); n != 1 {
		t.Fatalf("digest %d", n)
	}
	// Finished, and after a restart: not queued again today.
	e.exec(`UPDATE job SET state = 'done' WHERE kind = ?`, DigestJob)
	e.rebuild()
	if err := e.n.queueDigest(ctx); err != nil || jobCount() != 1 {
		t.Errorf("queued again: %v %d", err, jobCount())
	}
	e.now = e.now.AddDate(0, 0, 1)
	if err := e.n.queueDigest(ctx); err != nil || jobCount() != 2 {
		t.Errorf("next day: %v %d", err, jobCount())
	}
}
