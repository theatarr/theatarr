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
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/retention"
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
	// dirty: exec wrote one of History's sources since History was last stored (flag).
	dirty bool
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
	e.j = New(Config{Store: st, Policy: pol, Now: func() time.Time { return testNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.seed()
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if historySourceRE.MatchString(q) {
		e.dirty = true
	}
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

// historySourceRE matches a statement that writes one of History's sources.
var historySourceRE = regexp.MustCompile(`(?i)(INSERT\s+(OR\s+\w+\s+)?INTO|UPDATE|DELETE\s+FROM)\s+(download|download_attempt|library_import|library_action|router_blocklist|journal_action)\b`)

// flag stores History's rows from the sources the test wrote by exec (as the migration would have) and works out their
// flags once.
func (e *env) flag() {
	e.t.Helper()
	ctx := context.Background()
	if e.dirty {
		// The rows are stored again from the sources; a row checked before keeps its stored flags, as an event's
		// rewrite of its row does, so later fixtures exercise the incremental path.
		e.dirty = false
		for _, q := range []string{`DROP TABLE IF EXISTS temp.kept`,
			`CREATE TEMP TABLE kept AS SELECT ref, flags, severity FROM history WHERE flags IS NOT NULL`,
			`DELETE FROM history`} {
			if _, err := e.st.Writer().ExecContext(ctx, q); err != nil {
				e.t.Fatal(err)
			}
		}
		if err := store.BackfillHistory(ctx, e.st.Writer()); err != nil {
			e.t.Fatal(err)
		}
		if _, err := e.st.Writer().ExecContext(ctx, `UPDATE history SET flags = k.flags, severity = k.severity
			FROM temp.kept k WHERE k.ref = history.ref`); err != nil {
			e.t.Fatal(err)
		}
	}
	if _, err := e.j.FillAll(ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) list(f Filter) *Page {
	e.t.Helper()
	e.flag()
	p, err := e.j.List(context.Background(), f)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) get(id string) *Entry {
	e.t.Helper()
	e.flag()
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
	if g.Kind != KindGrab || g.Source != "theatarr" || g.Title.Name != "Alpha" {
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
	if f.Kind != KindStall || f.Label != "Stalled" || !strings.HasPrefix(f.Summary, "Download stalled: not ready 10m0s") {
		t.Errorf("stall: %+v", f)
	}
	if b := e.get("bl:1"); b.Kind != KindBlocklist || b.Source != "router" || b.Release.Quality != "Remux-2160p" {
		t.Errorf("blocklist: %+v", b)
	}
	if rs := e.get("run:8"); rs.Kind != KindResearch || rs.Summary != "Search again after a stalled download: nothing grabbed · no accepted release among 4 results" {
		t.Errorf("research: %+v", rs)
	}
	if g.Label != "Failed" || !strings.HasPrefix(g.Summary, "Grabbed Alpha.2020.2160p.UHD.BluRay.REMUX-GRP on AD · failed: stalled: not ready 10m0s") {
		t.Errorf("grab summary: %s %q", g.Label, g.Summary)
	}
	// Nothing secret in any output, JSON or text. The failure is on the grab's line.
	page := e.list(Filter{})
	if len(page.Items) != 3 {
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

// Runs the search schedule marks asked (Search now) or new (a retry of a new release), and the old retry chains'
// (retry_new, fresh), are ordinary searches: they are not listed as re-searches after a failed download, and their own
// entries are searches.
func TestScheduledRunsAreNotFailureResearches(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	at := testNow.Add(-2 * time.Hour)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, research, message,
		created_at, updated_at) VALUES (9, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'asked', 'nothing found', ?, ?),
		(10, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'new', 'nothing found', ?, ?),
		(11, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'retry_new', 'nothing found', ?, ?),
		(12, 'wanted', 1, 1, 'movie-remux', 'movie', 'nothing', 'fresh', 'nothing found', ?, ?)`,
		msOf(at.Add(20*time.Minute)), msOf(at.Add(21*time.Minute)), msOf(at.Add(30*time.Minute)), msOf(at.Add(31*time.Minute)),
		msOf(at.Add(32*time.Minute)), msOf(at.Add(33*time.Minute)), msOf(at.Add(34*time.Minute)), msOf(at.Add(35*time.Minute)))
	var refs []string
	for _, ev := range e.get("dl:1").After {
		if ev.Kind == "research" {
			refs = append(refs, ev.Ref)
		}
	}
	if !slices.Equal(refs, []string{"run:8"}) {
		t.Errorf("re-searches after the failure: %v", refs)
	}
	for _, id := range []string{"run:9", "run:10", "run:11", "run:12"} {
		if g := e.get(id); g.Kind != KindSearch || g.Summary != "Wanted search: nothing grabbed · nothing found" {
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
	// Two grabs and their failures, which are on the grabs' lines.
	if p := e.list(Filter{Version: "tv-2160p", Since: base.Add(33 * time.Hour)}); len(p.Items) != 2 {
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
	// The flagged view pages too, and the badge counts the same entries.
	if p := e.list(Filter{Suspect: true, Limit: 2}); len(p.Items) != 2 || p.NextCursor == "" {
		t.Errorf("flagged page: %+v", p)
	}
	if n, err := e.j.FlaggedCount(context.Background()); err != nil || n != 3 {
		t.Errorf("flagged count %d %v", n, err)
	}
	// Flags are worked out once: a later change to the source does not change them.
	if _, err := e.st.Writer().Exec(`UPDATE download SET release_title = 'Alpha.2020.2160p.WEB-DL-GRP' WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	if f := hasFlag(e.get("dl:3"), CheckTitle); f == nil {
		t.Error("stored flag recomputed")
	}
	if _, err := e.j.List(context.Background(), Filter{Kinds: []string{"nope"}}); err == nil {
		t.Error("bad kind accepted")
	}
	if _, err := e.j.Get(context.Background(), "dl:999"); err != ErrNotFound {
		t.Errorf("missing: %v", err)
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

// A run of failures flags its latest failure only: each counted failure takes the flag from the earlier ones, also
// one that gets no flag itself (days 0, 1, 29 flag day 29; day 32 counts only 29 and 32, and day 29's flag goes).
// Each failure is checked as it comes, as live.
func TestRepeatedFailuresIncremental(t *testing.T) {
	e := newEnv(t)
	start := testNow.Add(-40 * 24 * time.Hour)
	fail := func(id int64, day int) {
		e.dlEvent(id, "failed", start.Add(time.Duration(day)*24*time.Hour), 1, 2, "movie-web",
			fmt.Sprintf("%040d", id), "Alpha.2020.2160p.WEB-DL-X", "WEBDL-2160p", 0, 0, nil, "dead")
		e.flag()
	}
	flagged := func() []string {
		var out []string
		for _, it := range e.list(Filter{Suspect: true, Limit: 50}).Items {
			out = append(out, it.ID)
		}
		return out
	}
	fail(1, 0)
	fail(2, 1)
	fail(3, 29)
	if got := flagged(); !slices.Equal(got, []string{"dl:3:fail"}) {
		t.Fatalf("after day 29: %v", got)
	}
	fail(4, 32)
	if got := flagged(); len(got) != 0 {
		t.Fatalf("after day 32: %v", got)
	}
}

// ids are a page's entry ids, in order.
func ids(p *Page) []string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

// The list shows one line per thing that happened (docs/journal.md "The list"): a download's import and link actions
// stand under its grab, an adoption's other files and the file it replaced under its first file, a hand unlink's action
// under its library action. Each still opens, is listed by its own kind or since bound when its line is not, and a
// flag on one shows on the line that stands for it.
func TestListFolds(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-48 * time.Hour)
	ms := func(d time.Duration) int64 { return msOf(at.Add(d)) }
	// Alpha movie-remux: download 5 on rd, imported an hour later over the old file.
	e.dlEvent(5, "imported", at, 1, 1, "movie-remux", hashA, "Alpha.2020.2160p.REMUX-NEW", "Remux-2160p", 9000, 60<<30,
		nil, "")
	e.exec(`UPDATE download SET provider = 'rd' WHERE id = 5`)
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (5, 'linked', '/m/Alpha', '{}', ?, ?)`, ms(time.Hour), ms(time.Hour))
	e.exec(`INSERT INTO library_action (id, at, kind, mode, version_key, download_id, path, detail) VALUES
		(1, ?, 'link', 'applied', 'movie-remux', 5, '/movies/Alpha (2020)/Alpha (2020) [Remux-2160p]-NEW.mkv',
			'linked Alpha.2020.2160p.REMUX-NEW.mkv'),
		(2, ?, 'replace', 'applied', 'movie-remux', 5, '/movies/Alpha (2020)/Alpha (2020) [Remux-2160p]-OLD.mkv',
			'moved aside to /trash/movie-remux/2026-10-24/Alpha (2020)/Alpha (2020) [Remux-2160p]-OLD.mkv')`,
		ms(time.Hour), ms(time.Hour))
	// Gamma S01E01-E02 adopted from Sonarr (one release, one day); the adoption replaced the old S01E01.
	e.exec(`INSERT INTO library_action (id, at, kind, mode, version_key, path, detail, release_title, quality, title_id,
		episodes) VALUES
		(3, ?, 'link', 'applied', 'tv-2160p', '/tv/Gamma/Season 01/Gamma - S01E01.mkv',
			'adopted from sonarr_4k_debrid /arr/tv/Gamma/S01E01.mkv', 'Gamma.S01.WEB-ARR', 'WEBDL-2160p', 3, '[101]'),
		(4, ?, 'replace', 'applied', 'tv-2160p', '/tv/Gamma/Season 01/Gamma - S01E01 old.mkv',
			'unlinked (deleted in Sonarr), moved aside to /trash/Gamma - S01E01 old.mkv', NULL, NULL, 3, '[101]'),
		(5, ?, 'link', 'applied', 'tv-2160p', '/tv/Gamma/Season 01/Gamma - S01E02.mkv',
			'adopted from sonarr_4k_debrid /arr/tv/Gamma/S01E02.mkv', 'Gamma.S01.WEB-ARR', 'WEBDL-2160p', 3, '[102]')`,
		ms(2*time.Hour), ms(2*time.Hour+5*time.Second), ms(2*time.Hour+10*time.Second))
	// Alpha movie-web: a file unlinked by hand, then the action that says who did it.
	e.exec(`INSERT INTO library_action (id, at, kind, mode, version_key, path, detail, title_id) VALUES
		(6, ?, 'replace', 'applied', 'movie-web', '/movies/Alpha (2020)/Alpha.WEB.mkv',
			'unlinked (unlinked by mgr), moved aside to /trash/movie-web/Alpha (2020)/Alpha.WEB.mkv', 1)`, ms(3*time.Hour))
	e.exec(`INSERT INTO journal_action (id, action, title_id, version_key, target, detail, by, created_at) VALUES
		(1, 'unlink_file', 1, 'movie-web', '/movies/Alpha (2020)/Alpha.WEB.mkv',
			'moved to /trash/movie-web/Alpha (2020)/Alpha.WEB.mkv', 'mgr', ?)`, ms(3*time.Hour+time.Second))
	e.flag()
	// The import is flagged (as a check would have found).
	if _, err := e.st.Writer().Exec(`UPDATE history SET flags = '[{"code":"file_runtime","severity":"warn","reason":"short"}]',
		severity = 'warn' WHERE ref = 'dl:5:import'`); err != nil {
		t.Fatal(err)
	}

	all := e.list(Filter{})
	if got := ids(all); !slices.Equal(got, []string{"lib:6", "lib:3", "dl:5"}) {
		t.Fatalf("lines: %v", got)
	}
	want := map[string][2]string{
		"lib:6": {"Removed", "Removed Alpha.WEB.mkv · by mgr · moved to the trash"},
		"lib:3": {"Adopted", "Adopted 2 files of Season 1 from Sonarr (sonarr_4k_debrid) · replaced Gamma - S01E01 old.mkv"},
		"dl:5":  {"Upgraded", "Grabbed Alpha.2020.2160p.REMUX-NEW on RD · imported, replaced Alpha (2020) [Remux-2160p]-OLD.mkv"},
	}
	for _, it := range all.Items {
		if w := want[it.ID]; it.Label != w[0] || it.Summary != w[1] {
			t.Errorf("%s: %s %q\nwant %s %q", it.ID, it.Label, it.Summary, w[0], w[1])
		}
	}
	if g := all.Items[2]; hasFlag(g, CheckFileRuntime) == nil || !g.Suspect || !slices.Contains(g.Paths,
		"/movies/Alpha (2020)/Alpha (2020) [Remux-2160p]-OLD.mkv") {
		t.Errorf("grab line flags %+v paths %v", g.Flags, g.Paths)
	}
	// What followed names every entry a line stands for; each opens, in plain words.
	refs := func(id string) []string {
		var out []string
		for _, ev := range e.get(id).After {
			out = append(out, ev.Ref)
		}
		return out
	}
	for id, kids := range map[string][]string{"dl:5": {"dl:5:import", "lib:1", "lib:2"}, "lib:3": {"lib:4", "lib:5"},
		"lib:6": {"act:1"}} {
		for _, k := range kids {
			if !slices.Contains(refs(id), k) {
				t.Errorf("%s: what followed %v lacks %s", id, refs(id), k)
			}
		}
	}
	for id, w := range map[string]string{
		"dl:5:import": "Imported Alpha.2020.2160p.REMUX-NEW",
		"lib:1":       "Linked Alpha (2020) [Remux-2160p]-NEW.mkv",
		"lib:2":       "Replaced Alpha (2020) [Remux-2160p]-OLD.mkv · moved to the trash",
		"lib:4":       "Removed Gamma - S01E01 old.mkv · deleted in Sonarr · moved to the trash",
		"lib:5":       "Adopted Gamma - S01E02.mkv from Sonarr (sonarr_4k_debrid)",
		"act:1":       "Removed Alpha.WEB.mkv · by mgr · moved to the trash",
	} {
		if g := e.get(id); g.Summary != w || strings.Contains(g.Summary, "/") {
			t.Errorf("%s: %q, want %q", id, g.Summary, w)
		}
	}
	// Filters: a kind whose parent is not listed shows its own entries; the parent's kind folds them again.
	for _, c := range []struct {
		f    Filter
		want []string
	}{
		{Filter{Kinds: []string{KindImport}}, []string{"dl:5:import"}},
		{Filter{Kinds: []string{KindAction}}, []string{"act:1"}},
		{Filter{Kinds: []string{KindLink}}, []string{"lib:6", "lib:3", "lib:2", "lib:1"}},
		{Filter{Kinds: []string{KindGrab, KindImport, KindLink, KindAction}}, []string{"lib:6", "lib:3", "dl:5"}},
		{Filter{Kinds: []string{KindUpgrade, KindImport}}, []string{"dl:5:import"}},
		// A since bound after the parent lists what came after it.
		{Filter{Since: at.Add(30 * time.Minute)}, []string{"lib:6", "lib:3", "lib:2", "lib:1", "dl:5:import"}},
		{Filter{Since: at.Add(2*time.Hour + 3*time.Second)}, []string{"lib:6", "lib:5", "lib:4"}},
		{Filter{Title: "Gamma"}, []string{"lib:3"}},
		{Filter{Version: "movie-web"}, []string{"lib:6"}},
		{Filter{Restricted: true, Within: []int64{1}}, []string{"lib:6", "dl:5"}},
		// The flagged view folds nothing: the flagged import is listed, and the badge counts it once.
		{Filter{Suspect: true}, []string{"dl:5:import"}},
	} {
		if got := ids(e.list(c.f)); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.f, got, c.want)
		}
	}
	if n, err := e.j.FlaggedCount(context.Background()); err != nil || n != 1 {
		t.Errorf("flagged count %d %v", n, err)
	}
	// Pages of one: every line once, none split.
	var paged []string
	f := Filter{Limit: 1}
	for {
		p := e.list(f)
		paged = append(paged, ids(p)...)
		if p.NextCursor == "" {
			break
		}
		f.Cursor = p.NextCursor
	}
	if !slices.Equal(paged, ids(all)) {
		t.Errorf("paged %v", paged)
	}
}

// Summaries name files, not paths: a path in free text is cut to its file name, a trash path said as the trash.
func TestPlainDetail(t *testing.T) {
	for in, want := range map[string]string{
		"moved aside to /trash/a b (2020)/x [1].mkv":                         "moved to the trash",
		"moved aside to /trash/x.mkv (renamed during the upgrade)":           "moved to the trash (renamed during the upgrade)",
		"moved to /trash/tv/S01E08 - x.mkv when its library was removed":     "moved to the trash when its library was removed",
		`"/media/movies/A (2020)/A.mkv" is not a symlink`:                    `"A.mkv" is not a symlink`,
		"leftover symlink moved aside to /t/x.mkv: no download holds it":     "leftover symlink moved to the trash: no download holds it",
		"root /media/tv: permission denied; /media/tv/B/x.mkv is under none": "root tv: permission denied; x.mkv is under none",
		"stalled: not ready 10m0s after the add":                             "stalled: not ready 10m0s after the add",
	} {
		if got := plainDetail(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// Review follow-ups to the list: every folded action is in its grab's What followed (applied or not); a grab reads
// Upgraded only once it replaced a file, Failed when it failed; an import refused with its failed download is no
// import held for review; a pack's grab line names its episodes (the episode window keeps it); plain words for a
// missing file's hash, a search after every release failed, and a removal's remover.
func TestListFollowUps(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-72 * time.Hour)
	ms := func(d time.Duration) int64 { return msOf(at.Add(d)) }
	// Gamma: an imported pack (S01E01-E02), then an upgrade attempt still downloading and one that failed.
	e.dlEvent(1, "imported", at, 3, 3, "tv-2160p", hashA, "Gamma.S01.2160p-A", "WEBDL-2160p", 100, 1<<30,
		[]int64{101, 102}, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (1, 'linked', '/m/G', '{}', ?, ?)`, ms(time.Hour), ms(time.Hour))
	e.dlEvent(2, "downloading", at.Add(2*time.Hour), 3, 3, "tv-2160p", hashB, "Gamma.S01.2160p-B", "WEBDL-2160p", 200,
		1<<30, []int64{101, 102}, "")
	e.dlEvent(3, "failed", at.Add(3*time.Hour), 3, 3, "tv-2160p", hashC, "Gamma.S01.2160p-C", "WEBDL-2160p", 200,
		1<<30, []int64{101, 102}, "library: rejected by the sample guard")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
		VALUES (3, 'rejected', '/m/G', 'sample guard: the file runs 4 min', '{}', ?, ?)`, ms(3*time.Hour), ms(3*time.Hour))
	// Alpha movie-remux: a download whose import ran dry (library paused).
	e.dlEvent(4, "ready", at.Add(4*time.Hour), 1, 1, "movie-remux", "dddddddddddddddddddddddddddddddddddddddd",
		"Alpha.2020.REMUX-D", "Remux-2160p", 9000, 1<<30, nil, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (4, 'dry_run', '/m/A', '{}', ?, ?)`, ms(5*time.Hour), ms(5*time.Hour))
	e.exec(`INSERT INTO library_action (id, at, kind, mode, version_key, download_id, path, detail) VALUES
		(1, ?, 'link', 'dry_run', 'movie-remux', 4, '/movies/Alpha/Alpha.D.mkv', 'the library is in dry-run mode'),
		(2, ?, 'dead', 'applied', 'movie-web', NULL, '/movies/Alpha/Alpha.W.mkv',
			'missing 7c977171240e2dfc7c977171240e2dfc7c977171'),
		(3, ?, 'replace', 'applied', 'movie-web', NULL, '/movies/Alpha/Alpha.X.mkv',
			'unlinked (copy removed by key:auto), moved aside to /trash/Alpha.X.mkv')`,
		ms(5*time.Hour), ms(6*time.Hour), ms(7*time.Hour))
	e.exec(`UPDATE library_action SET title_id = 1 WHERE id IN (2, 3)`)
	e.exec(`INSERT INTO journal_action (id, action, title_id, version_key, target, detail, by, created_at) VALUES
		(1, 'remove_version', 1, 'movie-web', 'Alpha', 'files deleted', 'key:auto', ?)`, ms(7*time.Hour-time.Second))
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, research, message,
		created_at, updated_at) VALUES (5, 'wanted', 3, 3, 'tv-2160p', 'season', 'nothing', 'exhausted',
		'no accepted release left', ?, ?)`, ms(8*time.Hour), ms(8*time.Hour))

	byID := map[string]*Entry{}
	for _, it := range e.list(Filter{}).Items {
		byID[it.ID] = it
	}
	for id, w := range map[string][2]string{
		"dl:1":  {"Grabbed", "Grabbed Gamma.S01.2160p-A · imported"},
		"dl:2":  {"Grabbed", "Grabbed Gamma.S01.2160p-B · downloading"},
		"dl:3":  {"Failed", "Grabbed Gamma.S01.2160p-C · failed: library: rejected by the sample guard"},
		"dl:4":  {"Grabbed", "Grabbed Alpha.2020.REMUX-D · dry run, nothing linked"},
		"lib:2": {"Missing", "Missing Alpha.W.mkv"},
		"lib:3": {"Removed", "Removed Alpha.X.mkv · by key:auto · moved to the trash"},
		"act:1": {"Removed", "Removed from the library · by key:auto · files moved to the trash"},
		"run:5": {"Search after a failure", "Search again after every release tried failed (season): nothing grabbed · no accepted release left"},
	} {
		if g := byID[id]; g == nil || g.Label != w[0] || g.Summary != w[1] {
			t.Errorf("%s: %+v\nwant %s %q", id, g, w[0], w[1])
		}
	}
	if g := byID["dl:1"]; g == nil || !slices.Contains(g.Release.Episodes, "S01E02") {
		t.Errorf("pack's episodes: %+v", g)
	}
	var refs []string
	for _, ev := range e.get("dl:4").After {
		refs = append(refs, ev.Ref)
	}
	if !slices.Contains(refs, "lib:1") {
		t.Errorf("dry run's link action not in what followed: %v", refs)
	}
	if im := e.get("dl:3:import"); im.Label != "Import failed" || strings.Contains(im.Summary, "held") {
		t.Errorf("refused import: %s %q", im.Label, im.Summary)
	}
	for _, ev := range e.get("dl:3").After {
		if ev.Ref == "dl:3:import" && strings.Contains(ev.Text, "held") {
			t.Errorf("refused import in what followed: %q", ev.Text)
		}
	}
}

// An adoption group whose first file was replaced since still lists as one line after retention has run past the
// window: the first action, which History's group row names, is kept with that row.
func TestListAdoptionAfterPrune(t *testing.T) {
	e := newEnv(t)
	at := testNow.Add(-400 * 24 * time.Hour)
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES
		(1, '/tv/Gamma/S01E01.mkv', '/t/1', 3, 'unlinked', ?, ?), (2, '/tv/Gamma/S01E02.mkv', '/t/2', 3, 'active', ?, ?)`,
		msOf(at), msOf(at), msOf(at), msOf(at))
	e.exec(`INSERT INTO library_action (id, at, kind, mode, version_key, link_id, path, detail, release_title, quality,
		title_id, episodes) VALUES
		(1, ?, 'link', 'applied', 'tv-2160p', 1, '/tv/Gamma/S01E01.mkv', 'adopted from sonarr_4k /a/1.mkv', 'G.S01', 'WEBDL-2160p', 3, '[101]'),
		(2, ?, 'link', 'applied', 'tv-2160p', 2, '/tv/Gamma/S01E02.mkv', 'adopted from sonarr_4k /a/2.mkv', 'G.S01', 'WEBDL-2160p', 3, '[102]')`,
		msOf(at), msOf(at.Add(time.Second)))
	if got := ids(e.list(Filter{})); !slices.Equal(got, []string{"lib:1"}) {
		t.Fatalf("before: %v", got)
	}
	if _, err := retention.Prune(context.Background(), e.st, testNow, retention.Defaults); err != nil {
		t.Fatal(err)
	}
	if got := ids(e.list(Filter{})); !slices.Equal(got, []string{"lib:1"}) {
		t.Errorf("after the prune: %v", got)
	}
}

// A hold fixed by a later import that linked: the grab's line shows the import's own flags, not the held row's.
func TestGrabFlagsAfterResolvedHold(t *testing.T) {
	e := newEnv(t)
	e.dlEvent(5, "imported", testNow.Add(-time.Hour), 1, 1, "movie-remux", hashA, "Alpha.2020.REMUX-A", "Remux-2160p",
		9000, 60<<30, nil, "")
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at)
		VALUES (5, 'linked', '/m/A', '{}', ?, ?)`, msOf(testNow), msOf(testNow))
	e.flag()
	if _, err := e.st.Writer().Exec(`INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, flags,
		severity) VALUES ('dl:5:held', ?, 'failed', 'import', 1, 'movie-remux', 5,
		'[{"code":"library_guard","severity":"info","reason":"held"}]', '')`, msOf(testNow.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE history SET flags = '[]', severity = '' WHERE ref = 'dl:5:import'`); err != nil {
		t.Fatal(err)
	}
	if g := e.get("dl:5"); hasFlag(g, CheckGuard) != nil {
		t.Errorf("grab line flags %+v", g.Flags)
	}
}
