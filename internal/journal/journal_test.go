package journal

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
)

type env struct {
	t   *testing.T
	st  *store.Store
	pol *policy.Policy
	q   *jobs.Queue
	j   *Journal
}

func msOf(t time.Time) int64 { return store.Millis(t) }

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.NewQueue(st)
	q.SetClock(func() time.Time { return testNow })
	e := &env{t: t, st: st, pol: pol, q: q}
	e.j = New(Config{Store: st, Policy: pol, Queue: q, Now: func() time.Time { return testNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.seed()
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func gz(t *testing.T, v any) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.Bytes()
}

// seed: movie 1 "Alpha" (2020, 120 min, English) with movie-remux (version 1) and movie-web (version 2); series 3
// "Gamma" with tv-2160p (version 3), seasons 1 (10 episodes) and 2 (8 episodes); the arr instance radarr_4k_debrid
// (movie-remux) and sonarr_4k_debrid (tv-2160p).
func (e *env) seed() {
	now := msOf(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES
		(1, 'movie', 'Alpha', 2020, 101, ?, ?), (2, 'movie', 'Beta Story', 2021, 102, ?, ?)`, now, now, now, now)
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, runtime_minutes, original_language,
		created_at, updated_at) VALUES (3, 'series', 'Gamma', 2019, 303, 'standard', 50, 'English', ?, ?)`, now, now)
	for _, id := range []int{101, 102} {
		e.exec(`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES ('tmdb', ?, 200, ?,
			?, ?)`, fmt.Sprintf("/movie/%d?append_to_response=external_ids%%2Crelease_dates", id),
			gz(e.t, map[string]any{"runtime": 120, "original_language": "en", "original_title": ""}), now, now)
	}
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-remux', ?, ?),
		(2, 1, 'movie-web', ?, ?), (3, 3, 'tv-2160p', ?, ?), (4, 2, 'movie-remux', ?, ?)`, now, now, now, now, now, now,
		now, now)
	id := 0
	for s, n := range map[int]int{1: 10, 2: 8} {
		for i := 1; i <= n; i++ {
			id++
			e.exec(`INSERT INTO episode (id, title_id, season, number, runtime_minutes, created_at, updated_at)
				VALUES (?, 3, ?, ?, 50, ?, ?)`, s*100+i, s, i, now, now)
		}
	}
}

func (e *env) list(f Filter) *Page {
	e.t.Helper()
	p, err := e.j.List(context.Background(), f)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) get(id string) *Entry {
	e.t.Helper()
	en, err := e.j.Get(context.Background(), id)
	if err != nil {
		e.t.Fatalf("get %s: %v", id, err)
	}
	return en
}

func hasFlag(en *Entry, code string) *Flag {
	for i := range en.Flags {
		if en.Flags[i].Code == code {
			return &en.Flags[i]
		}
	}
	return nil
}

func dec(release string, accepted bool, quality string, score int, reasons ...string) *decide.Decision {
	d := &decide.Decision{Release: release, Accepted: accepted, Quality: quality, Score: score, Version: "movie-remux",
		Formats: []decide.FormatScore{{Name: "TrueHD ATMOS", Score: score}}}
	for _, r := range reasons {
		d.Rejections = append(d.Rejections, decide.Rejection{Reason: r, Message: r + " at https://idx.example/api?apikey=SECRETKEY1"})
	}
	return d
}

// seedRoute records an acquire search and the download the router made from it, with attempts and cache evidence.
func (e *env) seedRoute() {
	at := testNow.Add(-2 * time.Hour)
	ranked := []*decide.Decision{
		dec("Alpha.2020.2160p.UHD.BluRay.REMUX-GRP", true, "Remux-2160p", 8550),
		dec("Alpha.2020.2160p.WEB-DL-OTHER", true, "WEBDL-2160p", 6200),
		dec("Alpha.2020.720p.HDTV-X", false, "HDTV-720p", 100, "QualityNotWanted"),
		dec("Alpha.2020.2160p.CAM-Y", false, "CAM", 9000, "QualityNotWanted", "CustomFormatMinimumScore"),
	}
	rels := map[*decide.Decision]indexer.Release{
		ranked[0]: {Title: ranked[0].Release, Indexer: "dmm", InfoHash: strings.ToUpper(hashA), Size: 60 << 30},
		ranked[1]: {Title: ranked[1].Release, Indexer: "torrentio", InfoHash: hashB, Size: 20 << 30},
	}
	if err := snapshot.Record(context.Background(), e.st.Writer(), snapshot.Search{Source: "acquire:7", RunID: 7,
		TitleID: 1, Version: "movie-remux", Target: "movie", Ranked: ranked, Releases: rels,
		Notes: []string{"prowlarr-tpb: GET https://prowlarr.lan/1/api?apikey=SECRETKEY2: 429"}, CreatedAt: at}); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, provider, provider_torrent_id, state, error, source, created_at, updated_at, added_at)
		VALUES (1, 1, 1, 'movie-remux', ?, 'Alpha.2020.2160p.UHD.BluRay.REMUX-GRP', 'dmm', 'Remux-2160p', 8550, ?, 'ad',
		'77', 'failed', 'stalled: not ready 10m0s after the add (token=SECRETKEY3)', 'acquire:7', ?, ?, ?)`, hashA,
		int64(60)<<30, msOf(at), msOf(at.Add(15*time.Minute)), msOf(at.Add(time.Minute)))
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, message, attempted_at) VALUES
		(1, 'rd', 'skipped', 'rd_name_rule', 'Real-Debrid refuses this name', ?),
		(1, 'ad', 'accepted', 'add', 'added at https://api.alldebrid.com/v4?apikey=SECRETKEY4', ?)`, msOf(at), msOf(at))
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at) VALUES
		(?, 'ad', 'cached', 'feed:dmm-ad', ?, ?)`, hashA, msOf(at), msOf(at.Add(24*time.Hour)))
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
		created_at, researched_at) VALUES ('movie-remux', ?, 1, 'Alpha.2020.2160p.UHD.BluRay.REMUX-GRP',
		'stalled: not ready', 'stalled', 1, ?, ?)`, hashA, msOf(at.Add(15*time.Minute)), msOf(at.Add(16*time.Minute)))
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, research, message,
		created_at, updated_at) VALUES (8, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'stalled',
		'no accepted release among 4 results', ?, ?)`, msOf(at.Add(16*time.Minute)), msOf(at.Add(17*time.Minute)))
}

func TestDownloadEntries(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	g := e.get("dl:1")
	if g.Kind != KindGrab || g.Source != "theatarr" || g.Title.Name != "Alpha" || g.Mode != "shadow" {
		t.Fatalf("grab: %+v", g)
	}
	r := g.Reasoning
	if r == nil || r.Chosen == nil || r.Chosen.Release != "Alpha.2020.2160p.UHD.BluRay.REMUX-GRP" ||
		r.Chosen.Score != 8550 || r.Results != 4 || r.Accepted != 2 {
		t.Fatalf("reasoning: %+v", r)
	}
	if len(r.RunnersUp) != 3 || r.RunnersUp[0].Release != "Alpha.2020.2160p.WEB-DL-OTHER" ||
		r.RunnersUp[1].Score != 9000 || r.Rejections["QualityNotWanted"] != 2 || r.Rejections["CustomFormatMinimumScore"] != 1 {
		t.Errorf("runners-up: %+v %+v", r.RunnersUp, r.Rejections)
	}
	if len(g.Attempts) != 2 || g.Attempts[1].Outcome != "accepted" || len(g.Cache) != 1 || g.Cache[0].Source != "feed:dmm-ad" {
		t.Errorf("attempts %+v cache %+v", g.Attempts, g.Cache)
	}
	kinds := []string{}
	for _, ev := range g.After {
		kinds = append(kinds, ev.Kind)
	}
	if !slices.Contains(kinds, "stall") || !slices.Contains(kinds, "blocklist") || !slices.Contains(kinds, "research") {
		t.Errorf("after: %v", kinds)
	}
	if !slices.Contains(g.Related, "dl:1:fail") || !slices.Contains(g.Related, "run:8") {
		t.Errorf("related: %v", g.Related)
	}
	f := e.get("dl:1:fail")
	if f.Kind != KindStall || !strings.HasPrefix(f.Summary, "download failed: stalled") {
		t.Errorf("stall: %+v", f)
	}
	if b := e.get("bl:1"); b.Kind != KindBlocklist || b.Source != "router" || b.Release.Quality != "Remux-2160p" {
		t.Errorf("blocklist: %+v", b)
	}
	if rs := e.get("run:8"); rs.Kind != KindResearch || !strings.Contains(rs.Summary, "re-search (stalled)") {
		t.Errorf("research: %+v", rs)
	}
	// Nothing secret in any output, JSON or text.
	page := e.list(Filter{})
	if len(page.Items) != 4 {
		t.Fatalf("entries: %d", len(page.Items))
	}
	b, _ := json.Marshal(page)
	var text strings.Builder
	for _, it := range page.Items {
		text.WriteString(it.Text())
	}
	for _, out := range []string{string(b), text.String()} {
		for _, bad := range []string{"SECRETKEY", "apikey", "://", "token="} {
			if strings.Contains(out, bad) {
				t.Errorf("output holds %q", bad)
			}
		}
	}
	if !strings.Contains(text.String(), "chosen Alpha.2020.2160p.UHD.BluRay.REMUX-GRP (Remux-2160p 8550)") {
		t.Errorf("text:\n%s", text.String())
	}
}

// Runs the search schedule marks asked (Search now) or new (a retry of a new release) are ordinary searches: they are
// not listed as re-searches after a failed download, and their own entries are searches.
func TestScheduledRunsAreNotFailureResearches(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	at := testNow.Add(-2 * time.Hour)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, research, message,
		created_at, updated_at) VALUES (9, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'asked', 'nothing found', ?, ?),
		(10, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'new', 'nothing found', ?, ?)`,
		msOf(at.Add(20*time.Minute)), msOf(at.Add(21*time.Minute)), msOf(at.Add(30*time.Minute)), msOf(at.Add(31*time.Minute)))
	var refs []string
	for _, ev := range e.get("dl:1").After {
		if ev.Kind == "research" {
			refs = append(refs, ev.Ref)
		}
	}
	if !slices.Equal(refs, []string{"run:8"}) {
		t.Errorf("re-searches after the failure: %v", refs)
	}
	for _, id := range []string{"run:9", "run:10"} {
		if g := e.get(id); g.Kind != KindSearch || strings.Contains(g.Summary, "re-search") {
			t.Errorf("%s: %s %q", id, g.Kind, g.Summary)
		}
	}
	if g := e.get("run:8"); g.Kind != KindResearch {
		t.Errorf("run:8: %s", g.Kind)
	}
}

func TestSnapshotCandidates(t *testing.T) {
	var ranked []*decide.Decision
	for i := range 12 {
		ranked = append(ranked, dec(fmt.Sprintf("A%d", i), true, "WEBDL-2160p", 100-i))
	}
	for i := range 6 {
		ranked = append(ranked, dec(fmt.Sprintf("R%d", i), false, "HDTV-720p", i*10, "QualityNotWanted"))
	}
	cs, counts := snapshot.Candidates(ranked, nil)
	if len(cs) != snapshot.KeepAccepted+snapshot.KeepRejected || cs[0].Release != "A0" || cs[0].Rank != 1 {
		t.Fatalf("kept: %d %+v", len(cs), cs[0])
	}
	if last := cs[len(cs)-1]; last.Release != "R2" || cs[snapshot.KeepAccepted].Release != "R5" {
		t.Errorf("rejected order: %+v", cs[snapshot.KeepAccepted:])
	}
	if counts["QualityNotWanted"] != 6 {
		t.Errorf("counts: %v", counts)
	}
}

// dlEvent inserts one download of ours: added (a grab), imported, or failed with errText.
func (e *env) dlEvent(id int64, state string, at time.Time, title, version int64, key, hash, release, quality string,
	score int, size int64, eps []int64, errText string) {
	if eps == nil {
		eps = []int64{}
	}
	ej, _ := json.Marshal(eps)
	var imported any
	if state == "imported" {
		imported = msOf(at.Add(time.Hour))
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
		size_bytes, episodes, state, error, source, created_at, updated_at, added_at, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, 'dmm', ?, ?, ?, ?, ?, ?, 'rss', ?, ?, ?, ?)`, id, title, version, key, hash, release,
		quality, score, size, string(ej), state, nullStr(errText), msOf(at), msOf(at), msOf(at), imported)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func TestDownloadChecks(t *testing.T) {
	e := newEnv(t)
	base := testNow.Add(-40 * 24 * time.Hour)
	// 20 ordinary WEBDL-2160p grabs of Beta (~170 MB/min over 120 min) make the size median.
	for i := range 20 {
		e.dlEvent(int64(1000+i), "imported", base.Add(time.Duration(i)*time.Hour), 2, 4, "movie-remux",
			fmt.Sprintf("%040d", i), "Beta.Story.2021.2160p.WEB-DL-G", "WEBDL-2160p", 1000, 20<<30, nil, "")
	}
	// Alpha: imported Remux at 8550, then an "upgrade" to a 1 GB WEBDL named for another movie and year.
	e.dlEvent(1, "imported", base.Add(-48*time.Hour), 1, 1, "movie-remux", hashC, "Alpha.2020.Remux-2160p-GRP",
		"Remux-2160p", 8550, 60<<30, nil, "")
	e.dlEvent(3, "added", base.Add(24*time.Hour), 1, 1, "movie-remux", hashA,
		"Totally.Different.Film.2011.2160p.WEB-DL.ITA-X", "WEBDL-2160p", 200, 1<<30, nil, "")
	// Gamma: a season 5 pack (the series has 2 seasons) filed under season 1 episodes; then failures.
	e.dlEvent(50, "added", base.Add(30*time.Hour), 3, 3, "tv-2160p", hashB, "Gamma.S05.2160p.WEB-DL-X",
		"WEBDL-2160p", 100, 40<<30, []int64{101, 102}, "")
	for i := range 4 {
		e.dlEvent(int64(60+i), "failed", base.Add(time.Duration(31+i)*time.Hour), 3, 3, "tv-2160p",
			fmt.Sprintf("%040d", 60+i), "Gamma.S01E01.2160p", "WEBDL-2160p", 0, 0, []int64{101},
			"see https://tracker.example/announce?passkey=SECRETKEY5")
	}
	up := e.get("dl:3")
	if up.Kind != KindUpgrade || up.Source != "theatarr" {
		t.Fatalf("upgrade: %+v", up)
	}
	for _, code := range []string{CheckTitle, CheckYear, CheckSize, CheckLowerScore, CheckLowerQual} {
		if code == CheckLowerScore {
			continue // the lower quality flag wins
		}
		if hasFlag(up, code) == nil {
			t.Errorf("upgrade lacks %s: %+v", code, up.Flags)
		}
	}
	if f := hasFlag(up, CheckSize); f == nil || f.Severity != SevError {
		t.Errorf("size: %+v", f)
	}
	if !up.Suspect || up.Severity != SevError {
		t.Errorf("suspect: %v %s", up.Suspect, up.Severity)
	}
	pack := e.get("dl:50")
	if f := hasFlag(pack, CheckEpisodes); f == nil || f.Severity != SevError || len(pack.Release.Episodes) != 2 {
		t.Errorf("pack: %+v %+v", pack.Flags, pack.Release)
	}
	// Only the latest failure of the run is flagged.
	if f := hasFlag(e.get("dl:63:fail"), CheckFailures); f == nil || f.Severity != SevWarn {
		t.Errorf("latest failure: %+v", f)
	}
	if f := hasFlag(e.get("dl:62:fail"), CheckFailures); f != nil {
		t.Errorf("earlier failure flagged: %+v", f)
	}
	fail := e.get("dl:63:fail")
	if b, _ := json.Marshal(fail); strings.Contains(string(b), "SECRETKEY") || strings.Contains(string(b), "passkey") {
		t.Errorf("failure leaks: %s", b)
	}
	// Filters.
	sus := e.list(Filter{Suspect: true, Limit: 500})
	ids := map[string]bool{}
	for _, it := range sus.Items {
		ids[it.ID] = true
		if !it.Suspect {
			t.Errorf("not suspect: %s", it.ID)
		}
	}
	if !ids["dl:3"] || !ids["dl:50"] || !ids["dl:63:fail"] || len(ids) != 3 {
		t.Errorf("suspects: %v", ids)
	}
	if p := e.list(Filter{Severity: SevError, Limit: 500}); len(p.Items) != 2 {
		t.Errorf("errors: %d", len(p.Items))
	}
	if p := e.list(Filter{Kinds: []string{KindFailure}, Title: "gam"}); len(p.Items) != 4 {
		t.Errorf("failures of Gamma: %d", len(p.Items))
	}
	if p := e.list(Filter{Version: "tv-2160p", Since: base.Add(33 * time.Hour)}); len(p.Items) != 4 {
		t.Errorf("since: %d", len(p.Items))
	}
	if p := e.list(Filter{Title: "1"}); len(p.Items) != 2 {
		t.Errorf("title id: %d", len(p.Items))
	}
	// Cursor: pages of 3 cover everything once, newest first.
	all := e.list(Filter{Limit: 500})
	var paged []string
	f := Filter{Limit: 3}
	for {
		p := e.list(f)
		for _, it := range p.Items {
			paged = append(paged, it.ID)
		}
		if p.NextCursor == "" {
			break
		}
		f.Cursor = p.NextCursor
	}
	var want []string
	for i, it := range all.Items {
		want = append(want, it.ID)
		if i > 0 && it.At.After(all.Items[i-1].At) {
			t.Errorf("order: %s after %s", it.ID, all.Items[i-1].ID)
		}
	}
	if !slices.Equal(paged, want) {
		t.Errorf("paged %v\nwant %v", paged, want)
	}
	// A suspect scan with a small scan limit returns a cursor to continue.
	if p := e.list(Filter{Suspect: true, Limit: 10, ScanLimit: 2}); p.NextCursor == "" || p.Scanned != 2 {
		t.Errorf("scan limit: %+v", p)
	}
	if _, err := e.j.List(context.Background(), Filter{Kinds: []string{"nope"}}); err == nil {
		t.Error("bad kind accepted")
	}
	if _, err := e.j.Get(context.Background(), "dl:999"); err != ErrNotFound {
		t.Errorf("missing: %v", err)
	}
}

func TestVerdicts(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	ctx := context.Background()
	// Shadow mode: recorded only, and the flags are acknowledged.
	e.dlEvent(3, "added", testNow.Add(-time.Hour), 1, 1, "movie-remux", hashC, "Other.Film.1999.2160p.WEB-DL-X",
		"WEBDL-2160p", 100, 0, nil, "")
	before := e.get("dl:3")
	if !before.Suspect {
		t.Fatalf("not suspect: %+v", before.Flags)
	}
	v, err := e.j.Judge(ctx, "dl:3", VerdictRequest{Verdict: "wrong", Reason: "another film"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if v.Action != ActionRecorded || !strings.Contains(v.Note, "Watch only") || v.ResearchJob != 0 {
		t.Errorf("shadow verdict: %+v", v)
	}
	after := e.get("dl:3")
	if after.Suspect || after.Verdict == nil || after.Verdict.Verdict != "wrong" || !after.Flags[0].Acked {
		t.Errorf("after: %+v", after)
	}
	var n int
	e.st.Reader().QueryRow(`SELECT COUNT(*) FROM router_blocklist WHERE source = 'journal'`).Scan(&n)
	if n != 0 {
		t.Errorf("shadow verdict blocklisted")
	}
	// The verdict is an entry too.
	if ve := e.get("v:1"); ve.Kind != KindVerdict || ve.Judged != "dl:3" {
		t.Errorf("verdict entry: %+v", ve)
	}
	if _, err := e.j.Judge(ctx, "v:1", VerdictRequest{Verdict: "ok", Reason: "x"}, "admin"); err == nil {
		t.Error("judged a verdict")
	}
	for _, bad := range []VerdictRequest{{Verdict: "maybe", Reason: "x"}, {Verdict: "ok"}} {
		if _, err := e.j.Judge(ctx, "dl:1", bad, "admin"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := e.j.Judge(ctx, "dl:99", VerdictRequest{Verdict: "ok", Reason: "x"}, "admin"); err != ErrNotFound {
		t.Errorf("missing entry: %v", err)
	}
	// Active, router off: still recorded only.
	e.pol.Versions["movie-remux"].Mode = policy.ModeActive
	v, err = e.j.Judge(ctx, "dl:1", VerdictRequest{Verdict: "wrong", Reason: "bad"}, "key:agent")
	if err != nil || v.Action != ActionRecorded || !strings.Contains(v.Note, "router.enabled is false") {
		t.Fatalf("router off: %+v %v", v, err)
	}
	// Active, router on: blocklisted for the version, and a re-search queued.
	if e.pol.Router == nil {
		e.pol.Router = &policy.Router{}
	}
	e.pol.Router.Enabled = true
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, state, source, created_at, updated_at) VALUES (2, 1, 1, 'movie-remux', ?, 'Alpha.2020.2160p.WEB-DL-OTHER',
		'torrentio', 'WEBDL-2160p', 6200, 'imported', 'acquire:9', ?, ?)`, hashB, msOf(testNow), msOf(testNow))
	v, err = e.j.Judge(ctx, "dl:2", VerdictRequest{Verdict: "wrong", Reason: "wrong cut"}, "key:agent")
	if err != nil || v.Action != ActionBlocklisted || v.ResearchJob == 0 {
		t.Fatalf("active: %+v %v", v, err)
	}
	var src, reason string
	var researched any
	if err := e.st.Reader().QueryRow(`SELECT source, reason, researched_at FROM router_blocklist WHERE version_key =
		'movie-remux' AND info_hash = ?`, hashB).Scan(&src, &reason, &researched); err != nil || src != "journal" ||
		!strings.Contains(reason, "wrong cut") || researched == nil {
		t.Errorf("blocklist: %s %s %v %v", src, reason, researched, err)
	}
	var kind, payload string
	if err := e.st.Reader().QueryRow(`SELECT kind, payload FROM job WHERE id = ?`, v.ResearchJob).Scan(&kind,
		&payload); err != nil || kind != "acquire.research" || !strings.Contains(payload, `"reason":"journal"`) ||
		!strings.Contains(payload, `"download_id":2`) {
		t.Errorf("job: %s %s %v", kind, payload, err)
	}
	// An ok verdict records only, even on an active version.
	v, err = e.j.Judge(ctx, "dl:1:fail", VerdictRequest{Verdict: "ok", Reason: "expected"}, "admin")
	if err != nil || v.Action != ActionRecorded || v.Note != "" {
		t.Errorf("ok: %+v %v", v, err)
	}
	if p := e.list(Filter{Kinds: []string{KindVerdict}}); len(p.Items) != 4 {
		t.Errorf("verdict entries: %d", len(p.Items))
	}
}

func TestPrune(t *testing.T) {
	e := newEnv(t)
	old := testNow.Add(-200 * 24 * time.Hour)
	var ranked []*decide.Decision
	for i := range 6 {
		ranked = append(ranked, dec(fmt.Sprintf("R%d", i), true, "WEBDL-2160p", 100-i))
	}
	for _, s := range []snapshot.Search{
		{Source: "acquire:1", TitleID: 1, Version: "movie-remux", Ranked: ranked, CreatedAt: old},
		{Source: "acquire:2", TitleID: 1, Version: "movie-remux", Ranked: ranked, CreatedAt: old},
		{Source: "acquire:3", TitleID: 1, Version: "movie-remux", Ranked: ranked, CreatedAt: testNow},
	} {
		if err := snapshot.Record(context.Background(), e.st.Writer(), s); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, source,
		created_at, updated_at) VALUES (1, 1, 1, 'movie-remux', ?, 'R0', 'dmm', 'imported', 'acquire:1', ?, ?)`, hashA,
		msOf(old), msOf(old))
	sum, err := Prune(context.Background(), e.st, testNow)
	if err != nil || sum.Deleted != 1 || sum.Trimmed != 1 {
		t.Fatalf("prune: %+v %v", sum, err)
	}
	var cands string
	var trimmed int
	e.st.Reader().QueryRow(`SELECT candidates, trimmed FROM journal_decision WHERE source = 'acquire:1'`).Scan(&cands,
		&trimmed)
	var cs []snapshot.Candidate
	json.Unmarshal([]byte(cands), &cs)
	if trimmed != 1 || len(cs) != 2 {
		t.Errorf("trimmed: %d %d", trimmed, len(cs))
	}
	g := e.get("dl:1")
	if g.Reasoning == nil || g.Reasoning.Chosen == nil || g.Reasoning.Chosen.Release != "R0" ||
		!strings.Contains(g.Reasoning.Missing, "trimmed") {
		t.Errorf("trimmed reasoning: %+v", g.Reasoning)
	}
	if sum, _ := Prune(context.Background(), e.st, testNow); sum.Deleted+sum.Trimmed != 0 {
		t.Errorf("second prune: %+v", sum)
	}
}

// A download taken over by a later search (router: openDownload) carries that search's source and dates from the
// takeover, after the search decided: the entry shows that search's candidates, and retention keeps its decision
// and deletes the one of the search the row first came from, which no download holds any more.
func TestTakenOverDownloadKeepsItsDecision(t *testing.T) {
	e := newEnv(t)
	old := testNow.Add(-200 * 24 * time.Hour)
	first := []*decide.Decision{dec("R0", true, "WEBDL-2160p", 100), dec("R1", true, "WEBDL-2160p", 99)}
	second := []*decide.Decision{dec("S0", true, "WEBDL-2160p", 100), dec("S1", true, "WEBDL-2160p", 99)}
	for _, s := range []snapshot.Search{
		{Source: "acquire:1", TitleID: 1, Version: "movie-remux", Ranked: first, CreatedAt: old},
		{Source: "acquire:2", TitleID: 1, Version: "movie-remux", Ranked: second, CreatedAt: old.Add(time.Hour)},
	} {
		if err := snapshot.Record(context.Background(), e.st.Writer(), s); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, source,
		created_at, updated_at) VALUES (1, 1, 1, 'movie-remux', ?, 'S0', 'dmm', 'imported', 'acquire:2', ?, ?)`, hashA,
		msOf(old.Add(time.Hour+time.Minute)), msOf(old.Add(2*time.Hour)))
	if g := e.get("dl:1"); g.Reasoning == nil || g.Reasoning.Chosen == nil || g.Reasoning.Chosen.Release != "S0" {
		t.Fatalf("reasoning: %+v", g.Reasoning)
	}
	if sum, err := Prune(context.Background(), e.st, testNow); err != nil || sum.Deleted != 1 {
		t.Fatalf("prune: %+v %v", sum, err)
	}
	var sources string
	e.st.Reader().QueryRow(`SELECT group_concat(source) FROM journal_decision`).Scan(&sources)
	if sources != "acquire:2" {
		t.Errorf("decisions after the prune: %q", sources)
	}
	if g := e.get("dl:1"); g.Reasoning == nil || g.Reasoning.Chosen == nil || g.Reasoning.Chosen.Release != "S0" {
		t.Errorf("reasoning after the prune: %+v", g.Reasoning)
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                      "plain text",
		"see https://x.example/a?b=1 now": "see [redacted] now",
		"magnet:?xt=urn:btih:abc":         "[redacted]",
		"apikey=abc123 failed":            "[redacted] failed",
		"Alpha.2020.2160p.REMUX-GRP":      "Alpha.2020.2160p.REMUX-GRP",
		"token= split":                    "[redacted] split",
		"line\nbreak":                     "line break",
		"The Passkey Movie (2020)":        "The [redacted] Movie (2020)",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnmigratedDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The live schema before the journal: no journal_verdict (and so no verdicts or verdict entries).
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`DROP TABLE journal_verdict`); err != nil {
		t.Fatal(err)
	}
	j := New(Config{Store: st})
	if _, err := j.List(ctx, Filter{}); err != nil {
		t.Errorf("list without the journal tables: %v", err)
	}
	if _, err := j.Judge(ctx, "dl:1", VerdictRequest{Verdict: "ok", Reason: "x"}, ""); err != ErrNotMigrated {
		t.Errorf("judge: %v", err)
	}
}

// A decision carries the revision of the policy that made it: the search's, shown on the run and on the download it
// routed, and the revision the run was queued under when that differs.
func TestPolicyRevision(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-time.Hour)
	ranked := []*decide.Decision{dec("Alpha.2020.2160p.UHD.BluRay.REMUX-GRP", true, "Remux-2160p", 8550)}
	rels := map[*decide.Decision]indexer.Release{ranked[0]: {Title: ranked[0].Release, Indexer: "dmm", InfoHash: hashA}}
	if e.pol.Revision == "" {
		t.Fatal("the example policy has no revision")
	}
	if err := snapshot.Record(context.Background(), e.st.Writer(), snapshot.Search{Policy: e.pol, Source: "acquire:9",
		RunID: 9, TitleID: 1, Version: "movie-remux", Target: "movie", Ranked: ranked, Releases: rels,
		CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, message, policy_rev,
		created_at, updated_at) VALUES (9, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', '', 'olderrev0000', ?, ?)`,
		msOf(at.Add(-time.Minute)), msOf(at))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, state, source, policy_rev, created_at, updated_at) VALUES (5, 1, 1, 'movie-remux', ?,
		'Alpha.2020.2160p.UHD.BluRay.REMUX-GRP', 'dmm', 'Remux-2160p', 8550, 'queued', 'acquire:9', ?, ?, ?)`, hashA,
		e.pol.Revision, msOf(at), msOf(at))
	run := e.get("run:9")
	if run.PolicyRev != e.pol.Revision || run.QueuedPolicyRev != "olderrev0000" ||
		run.Reasoning == nil || run.Reasoning.PolicyRev != e.pol.Revision {
		t.Fatalf("run: policy %q queued %q reasoning %+v", run.PolicyRev, run.QueuedPolicyRev, run.Reasoning)
	}
	if !strings.Contains(run.Text(), "policy    "+e.pol.Revision+" (queued under olderrev0000)") {
		t.Errorf("text:\n%s", run.Text())
	}
	dl := e.get("dl:5")
	if dl.PolicyRev != e.pol.Revision || dl.Download == nil || dl.Download.PolicyRev != e.pol.Revision ||
		dl.QueuedPolicyRev != "olderrev0000" {
		t.Fatalf("download: policy %q queued %q", dl.PolicyRev, dl.QueuedPolicyRev)
	}
}
