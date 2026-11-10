package errinbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, t.TempDir()+"/e.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func newInbox(t *testing.T, st *store.Store, o Options) *Inbox {
	t.Helper()
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	}
	if o.Version == "" {
		o.Version = "v-test"
	}
	return New(st, o)
}

func flush(t *testing.T, in *Inbox) {
	t.Helper()
	if err := in.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func all(t *testing.T, st *store.Store) []Row {
	t.Helper()
	p, err := List(context.Background(), st, Filter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		`download 1234 failed: open /media/Movies/Film (2001)/a.mkv: no such file`: `download <n> failed: open <path>: no such file`,
		`hash 5d41402abc4b2a76b9719d911017c592 not found after 3.5s`:               `hash <hex> not found after <n>s`,
		`GET https://indexer.example/api?apikey=abc&t=search: status 500`:          `GET <url>: status <n>`,
		`title "The Matrix" id 6ba7b810-9dad-11d1-80b4-00c04fd430c8`:               `title "…" id <id>`,
		"  spaced\n\tout  ":              `spaced out`,
		`a decade of 0xdeadbeef1 errors`: `a decade of <hex> errors`,
		`version 'hd' of title 42`:       `version "…" of title <n>`,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Normalize(strings.Repeat("x", 5000)); len(got) > maxMessage {
		t.Errorf("Normalize kept %d bytes, cap %d", len(got), maxMessage)
	}
}

// TestDedupe: sightings that differ only in ids, paths and hashes share one row with a count; a different message
// or component is another row.
func TestDedupe(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	in.Report(Event{Source: SourceLog, Component: "library", Message: "import 12 failed: open /mnt/a/b.mkv: gone"})
	in.Report(Event{Source: SourceLog, Component: "library", Message: "import 99 failed: open /mnt/c/d e.mkv: gone"})
	in.Report(Event{Source: SourceLog, Component: "library", Message: "import 7 failed: permission denied"})
	in.Report(Event{Source: SourceLog, Component: "router", Message: "import 12 failed: open /mnt/a/b.mkv: gone"})
	flush(t, in)
	in.Report(Event{Source: SourceLog, Component: "library", Message: "import 100 failed: open /x/y.mkv: gone"})
	flush(t, in)
	rows := all(t, st)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(rows), rows)
	}
	var found bool
	for _, r := range rows {
		if r.Component == "library" && r.Message == "import <n> failed: open <path>: gone" {
			found = true
			if r.Count != 3 || r.Status != StatusNew || r.Sample != "import 100 failed: open /x/y.mkv: gone" {
				t.Errorf("grouped row = %+v", r)
			}
			if r.FirstVersion != "v-test" || r.LastVersion != "v-test" {
				t.Errorf("versions = %q %q", r.FirstVersion, r.LastVersion)
			}
		}
	}
	if !found {
		t.Errorf("no grouped row: %+v", rows)
	}
}

// TestDedupeKey: events with a Key group per key even when their messages normalize alike.
func TestDedupeKey(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	for _, k := range []string{"ep:41", "ep:42", "ep:41"} {
		in.Report(Event{Source: SourceJob, Component: "prewarm", Key: k, Message: "next episode " + k + " is missing"})
	}
	flush(t, in)
	rows := all(t, st)
	if len(rows) != 2 || rows[0].Count+rows[1].Count != 3 {
		t.Fatalf("rows = %+v", rows)
	}
}

// TestRegressionReopens: a resolved fingerprint that recurs is new again and counts a regression; an ignored one
// stays ignored.
func TestRegressionReopens(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{Version: "v1"})
	ctx := context.Background()
	in.Report(Event{Source: SourceJob, Component: "router.add", Message: "add failed: boom 1"})
	in.Report(Event{Source: SourceJob, Component: "router.poll", Message: "poll failed"})
	flush(t, in)
	rows := all(t, st)
	var add, poll Row
	for _, r := range rows {
		if r.Component == "router.add" {
			add = r
		} else {
			poll = r
		}
	}
	note := "fixed in abc"
	if _, err := Update(ctx, st, add.ID, StatusResolved, &note, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(ctx, st, poll.ID, StatusIgnored, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.opts.Version = "v2"
	in.Report(Event{Source: SourceJob, Component: "router.add", Message: "add failed: boom 2"})
	in.Report(Event{Source: SourceJob, Component: "router.poll", Message: "poll failed"})
	flush(t, in)
	got, err := Get(ctx, st, add.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNew || got.ResolvedAt != nil || got.Regressions != 1 || got.Count != 2 || got.Note != note {
		t.Errorf("regressed row = %+v", got)
	}
	if got.FirstVersion != "v1" || got.LastVersion != "v2" {
		t.Errorf("versions = %q %q, want v1 v2", got.FirstVersion, got.LastVersion)
	}
	if p, _ := Get(ctx, st, poll.ID); p.Status != StatusIgnored || p.Count != 2 {
		t.Errorf("ignored row = %+v", p)
	}
}

const plantedKey = "Zq81xVb7Lm02Pr6tKd9wYh3sNc5uJe4f"

// TestRedaction: a planted API key in a log attribute, in a URL query, in a panic message, and a known secret from
// the running settings never reach the stored row.
func TestRedaction(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	in.SetKnownSecrets(func(s string) string { return strings.ReplaceAll(s, "hunter2-known", "***") })
	ring := logring.New(10)
	ring.OnError(in.LogTee)
	log := slog.New(ring.Handler(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	log.Error("indexer search failed", "component", "indexer", "api_key", plantedKey, "url",
		"https://idx.example/api?t=search&apikey="+plantedKey, "err", errors.New("upstream said key "+plantedKey+" is bad"))
	log.Error("plain failure", "detail", "password=hunter2-known and Bearer "+plantedKey)
	func() {
		defer func() {
			if p := recover(); p != nil {
				in.Report(panicEvent("test", p, map[string]string{"route": "GET /x?token=" + plantedKey}))
			}
		}()
		panic("bad request for https://user:pw@host.example/feed?passkey=" + plantedKey)
	}()
	flush(t, in)
	rows := all(t, st)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for _, r := range rows {
		full, err := Get(context.Background(), st, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{full.Message, full.Sample, string(full.Context), full.Stack} {
			for _, secret := range []string{plantedKey, "hunter2-known", "pw@"} {
				if strings.Contains(field, secret) {
					t.Errorf("row %d (%s) keeps %q: %s", r.ID, r.Source, secret, field)
				}
			}
		}
	}
}

func TestRedactText(t *testing.T) {
	cases := map[string]string{
		"https://x.example/api?apikey=" + plantedKey + "&t=1": "https://x.example/api?apikey=********&t=1",
		"token: " + plantedKey:                                "token: ********",
		`{"password":"p@ss"}`:                                 `{"password":"********"}`,
		"Authorization: Bearer abcdefgh12345":                 "Authorization: Bearer ********",
		"hash 5d41402abc4b2a76b9719d911017c5925d41402a ok":    "hash 5d41402abc4b2a76b9719d911017c5925d41402a ok",
		"key " + plantedKey + " refused":                      "key ******** refused",
	}
	for in, want := range cases {
		if got := RedactText(in); got != want {
			t.Errorf("RedactText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestQueueFullDrops: with the writer stopped, reports beyond the queue are dropped and counted, never blocking.
func TestQueueFullDrops(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{QueueSize: 2})
	done := make(chan struct{})
	go func() {
		for range 50 {
			in.Report(Event{Source: SourceLog, Message: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Report blocked on a full queue")
	}
	if in.Drops() != 48 {
		t.Errorf("drops = %d, want 48", in.Drops())
	}
	in.saveDrops(context.Background())
	if n, _ := StateInt(context.Background(), st, "drops"); n != 48 {
		t.Errorf("saved drops = %d, want 48", n)
	}
}

// TestWriteFailureDoesNotRecurse: when the database write fails, the failure is logged at warn level with a quiet
// context and nothing comes back into the queue, even with the log ring teeing into the inbox.
func TestWriteFailureDoesNotRecurse(t *testing.T) {
	st := openStore(t)
	var out bytes.Buffer
	ring := logring.New(10)
	log := slog.New(ring.Handler(slog.NewTextHandler(&out, nil)))
	in := newInbox(t, st, Options{Logger: log, FlushEvery: 10 * time.Millisecond})
	ring.OnError(in.LogTee)
	st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { in.Run(ctx); close(stopped) }()
	log.Error("something broke", "err", "boom")
	deadline := time.Now().Add(5 * time.Second)
	for in.Failures() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-stopped
	if in.Failures() != 1 {
		t.Errorf("failed writes = %d, want 1 (a failure must not produce another event)", in.Failures())
	}
	if len(in.queue) != 0 || in.Drops() != 0 {
		t.Errorf("queue %d, drops %d after a failed write", len(in.queue), in.Drops())
	}
	if !strings.Contains(out.String(), "error inbox write failed") || !strings.Contains(out.String(), "level=WARN") {
		t.Errorf("no warning logged: %s", out.String())
	}
}

// TestLogTeeSkips: quiet contexts, warnings and context cancellation stay out.
func TestLogTeeSkips(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	ring := logring.New(10)
	ring.OnError(in.LogTee)
	log := slog.New(ring.Handler(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	log.ErrorContext(Quiet(context.Background()), "request", "code", 500)
	log.Warn("job error", "err", "boom")
	log.Error("loop", "err", context.Canceled)
	log.Error("pass", "err", "import: context canceled")
	log.Error("counted", "component", "x", "err", "real failure")
	flush(t, in)
	rows := all(t, st)
	if len(rows) != 1 || rows[0].Message != "counted: real failure" || rows[0].Component != "x" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestTopFrameAndPanicFingerprint(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	boom := func(id int) {
		defer func() {
			if p := recover(); p != nil {
				in.Report(panicEvent("http", p, nil))
			}
		}()
		var s []int
		if id > 0 {
			_ = s[id] // index out of range: a runtime error whose message differs per id
		}
	}
	boom(1)
	boom(2)
	flush(t, in)
	rows := all(t, st)
	if len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	r, _ := Get(context.Background(), st, rows[0].ID)
	if !strings.Contains(r.Stack, "TestTopFrameAndPanicFingerprint") {
		t.Errorf("stack lacks the panicking frame: %s", r.Stack)
	}
	if f := TopFrame(r.Stack); !strings.HasPrefix(f, "internal/errinbox.TestTopFrameAndPanicFingerprint") {
		t.Errorf("TopFrame = %q", f)
	}
}

// TestHealthTransitions: ok to error records a health event; back to ok resolves it with the recovery noted; a
// second failure reopens it as a regression.
func TestHealthTransitions(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	ctx := context.Background()
	in.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "ok"}, {Source: "BackupCheck", Type: "warning", Message: "old"}})
	flush(t, in)
	if rows := all(t, st); len(rows) != 0 {
		t.Fatalf("ok and warning recorded: %+v", rows)
	}
	in.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "error", Message: "mount /mnt/zurg not answering"}})
	in.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "error", Message: "mount /mnt/zurg not answering"}})
	flush(t, in)
	rows := all(t, st)
	if len(rows) != 1 || rows[0].Source != SourceHealth || rows[0].Count != 1 || rows[0].Component != "MountCheck" {
		t.Fatalf("rows = %+v", rows)
	}
	in.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "ok", Message: "listed"}})
	flush(t, in)
	r, _ := Get(ctx, st, rows[0].ID)
	if r.Status != StatusResolved || r.ResolvedAt == nil || !strings.Contains(r.Note, "recovered") {
		t.Errorf("after recovery = %+v", r)
	}
	in.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "error", Message: "mount /mnt/zurg not answering"}})
	flush(t, in)
	r, _ = Get(ctx, st, rows[0].ID)
	if r.Status != StatusNew || r.Regressions != 1 || r.Count != 2 {
		t.Errorf("after a second failure = %+v", r)
	}
	if v, _, _ := State(ctx, st, "health"); !strings.Contains(v, "MountCheck") {
		t.Errorf("health snapshot = %q", v)
	}
}

// TestHealthRecoveryAfterRestart: a check failing when the process stopped resolves its row once a new process sees
// it fine.
func TestHealthRecoveryAfterRestart(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	before := newInbox(t, st, Options{})
	before.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "error", Message: "mount /mnt/zurg not answering"}})
	flush(t, before)
	rows := all(t, st)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	after := newInbox(t, st, Options{})
	after.ObserveHealth([]HealthCheck{{Source: "MountCheck", Type: "ok", Message: "listed"}})
	flush(t, after)
	if r, _ := Get(ctx, st, rows[0].ID); r.Status != StatusResolved || !strings.Contains(r.Note, "recovered") {
		t.Errorf("after restart and recovery = %+v", r)
	}
}

func TestCapRows(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{MaxRows: 3})
	ctx := context.Background()
	in.Report(Event{Source: SourceLog, Component: "a", Message: "first", Time: time.Now().Add(-3 * time.Hour)})
	in.Report(Event{Source: SourceLog, Component: "b", Message: "second", Time: time.Now().Add(-2 * time.Hour)})
	in.Report(Event{Source: SourceLog, Component: "c", Message: "third", Time: time.Now().Add(-1 * time.Hour)})
	flush(t, in)
	var b Row
	for _, r := range all(t, st) {
		if r.Component == "b" {
			b = r
		}
	}
	if _, err := Update(ctx, st, b.ID, StatusResolved, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Report(Event{Source: SourceLog, Component: "d", Message: "fourth"})
	flush(t, in)
	in.Report(Event{Source: SourceLog, Component: "e", Message: "fifth"})
	flush(t, in)
	var comps []string
	for _, r := range all(t, st) {
		comps = append(comps, r.Component)
	}
	if strings.Join(comps, ",") != "e,d,c" {
		t.Errorf("kept %v, want e,d,c (resolved b first, then the oldest a)", comps)
	}
}

func TestListAndBulk(t *testing.T) {
	st := openStore(t)
	in := newInbox(t, st, Options{})
	ctx := context.Background()
	in.Report(Event{Source: SourceLog, Component: "a", Message: "one"})
	in.Report(Event{Source: SourceHTTP, Component: "http", Message: "GET /x answered 500"})
	in.Report(Event{Source: SourceJob, Component: "k", Message: "three"})
	flush(t, in)
	p, err := List(ctx, st, Filter{Source: SourceHTTP})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 1 || len(p.Items) != 1 || p.Counts[StatusNew] != 3 {
		t.Errorf("page = %+v", p)
	}
	if _, err := Bulk(ctx, st, nil, Filter{}, StatusResolved, nil, time.Now()); err == nil {
		t.Error("bulk with no ids and no filter accepted")
	}
	n, err := Bulk(ctx, st, nil, Filter{Status: "open"}, StatusResolved, nil, time.Now())
	if err != nil || n != 3 {
		t.Errorf("bulk resolve = %d, %v", n, err)
	}
	p, _ = List(ctx, st, Filter{Status: "open"})
	if p.Total != 0 || p.Counts[StatusResolved] != 3 {
		t.Errorf("after bulk = %+v", p)
	}
	if _, err := Get(ctx, st, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(999) = %v", err)
	}
}
