package web

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

func TestUpgradeEvidenceDecodesOnlySelectedDecisions(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	for _, q := range []string{
		`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		 ('old',1,'movie-remux','movie',2000,'[{"release":"superseded","accepted":true,"upgrade":true}]'),
		 ('new',1,'movie-remux','movie',6000,'[]'),
		 ('unrelated',1,'movie-web','movie',7000,'[{"release":"other version","accepted":true,"upgrade":true}]'),
		 ('early-rss',1,'movie-remux','rss',5500,'[{"release":"old RSS","accepted":true,"upgrade":true}]'),
		 ('late-rss',1,'movie-remux','rss',6500,'[{"release":"late RSS","accepted":true,"upgrade":true,"score":42}]')`,
	} {
		e.exec(q)
	}
	vs := []upgradeVersion{{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}}
	s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), vs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.evidence) != 1 {
		t.Fatalf("loaded evidence for unrelated versions: %+v", s.evidence)
	}
	for _, r := range s.evidence[titleKey{1, "movie-remux"}] {
		if r.at < 6000 && len(r.cands) != 0 {
			t.Errorf("decoded superseded candidates at %d: %+v", r.at, r.cands)
		}
	}
	u := s.explain(vs[0], 0)
	if u.Next == nil || u.Next.Release != "late RSS" || u.LastSearch == nil || u.LastSearch.UnixMilli() != 6000 {
		t.Fatalf("selected evidence: %+v", u)
	}
	// LastSearch is independent of the file-time floor, even when no blob needs decoding.
	e.exec(`UPDATE link SET created_at = 9000 WHERE id = 902`)
	s, err = e.web.loadUpgrades(context.Background(), e.st.Reader(), vs, false)
	if err != nil {
		t.Fatal(err)
	}
	u = s.explain(vs[0], 0)
	if u.Next != nil || u.LastSearch == nil || u.LastSearch.UnixMilli() != 6000 {
		t.Fatalf("last search before current file: %+v", u)
	}
	for _, recs := range s.evidence {
		for _, r := range recs {
			if len(r.cands) != 0 {
				t.Errorf("decoded evidence older than the file: %+v", r)
			}
		}
	}
}

func TestUpgradeEvidenceSelectsEachEpisodesCoveringHistory(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('season',10,'tv-2160p','s01',6000,'[]'),
		('ep2',10,'tv-2160p','s01e02',6500,'[]'),
		('ep1',10,'tv-2160p','s01e01',7000,'[{"release":"Episode 1","accepted":true,"upgrade":true,"score":50,"episodes":["S01E01"]}]')`)
	// Many newer decisions for another season cannot hide the covering searches. There is no history limit.
	e.exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 1200)
		INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates)
		SELECT 'other-season',10,'tv-2160p','s02',8000+x,'[{"release":"Other season","accepted":true,"upgrade":true}]' FROM n`)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('rss',10,'tv-2160p','rss',6600,'[{"release":"Episode 2 fallback","accepted":true,"upgrade":true,"score":60,"episodes":["S01E02"]}]')`)
	v := upgradeVersion{id: 10, titleID: 10, key: "tv-2160p", kind: "series", state: "filled"}
	s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, last int64
		release  string
	}{{101, 7000, "Episode 1"}, {102, 6500, "Episode 2 fallback"}} {
		u := s.explain(v, tc.id)
		if u.Next == nil || u.Next.Release != tc.release || u.LastSearch == nil || u.LastSearch.UnixMilli() != tc.last {
			t.Fatalf("episode %d: %+v", tc.id, u)
		}
	}
	for _, r := range s.evidence[titleKey{10, "tv-2160p"}] {
		if r.target == "s02" && len(r.cands) != 0 {
			t.Fatalf("decoded another season's candidates: %+v", r)
		}
	}
}

// A better release tried and failed after it was seen is not "Upgrade found"; another untried one still is, and the
// tried one stays the sighting Why? tells when nothing else is left. Nothing is written.
func TestUpgradeFoundClearsAfterAFailedTry(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',1,'movie-remux','movie',2500,'[{"release":"Best","info_hash":"AAAA","accepted":true,"upgrade":true,"score":9000},
		  {"release":"Second","info_hash":"bbbb","accepted":true,"upgrade":true,"score":5000}]')`)
	fail := func(hash string, at int64) {
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
			created_at, updated_at) VALUES (1, 1, 'movie-remux', ?, 'r', 'x', 'failed', 'miss on every provider', ?, ?)`, hash, at, at)
	}
	v := upgradeVersion{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}
	explain := func() *UpgradeInfo {
		t.Helper()
		s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.explain(v, 0)
	}
	// A failure before the search's sighting counts too (corrected in phase Z: the search's own route skipped the
	// known miss, so the search is no news): the other release is found.
	fail("aaaa", 1500)
	if u := explain(); u.State != "upgrade_available" || u.Next.Release != "Second" {
		t.Fatalf("older failure: %+v", u)
	}
	// Tried after the sighting (the hash in any case): the other release is found instead.
	fail("aaaa", 2600)
	if u := explain(); u.State != "upgrade_available" || u.Next.Release != "Second" {
		t.Fatalf("one tried: %+v", u)
	}
	// Both tried: no upgrade found, the best one is still said.
	fail("BBBB", 2700)
	if u := explain(); u.State != "below_cutoff" || u.Next == nil || u.Next.Release != "Best" {
		t.Fatalf("both tried: %+v", u)
	}
	// Another library's failure is not this library's.
	e.exec(`UPDATE download SET version_key = 'movie-web' WHERE info_hash = 'BBBB'`)
	if u := explain(); u.State != "upgrade_available" || u.Next.Release != "Second" {
		t.Fatalf("other library: %+v", u)
	}
}

// An RSS sighting does not revive a release that failed since the current file: the feed listing it again keeps it
// tried, whenever it was read. A listing published after the failure, or one now cached on a library provider after
// a cache miss, shows it again. A new covering search keeps it tried too (a search's failed release is tried whenever
// it failed: TestUpgradeFoundStaysTriedForASearchThatSawAFailedRelease).
func TestUpgradeFoundStaysTriedAfterAnRSSResighting(t *testing.T) {
	t.Parallel()
	const best = `"release":"Best","info_hash":"aaaa","accepted":true,"upgrade":true,"score":9000`
	var next *UpgradeNext
	state := func(t *testing.T, why string, later ...string) string {
		t.Helper()
		// An indexer whose releases go to RD only.
		e := newManageEnv(t, func(p *policy.Policy) { p.Indexers["rd-only"] = &policy.Indexer{Providers: []string{"rd"}} })
		e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
			('search',1,'movie-remux','movie',2500,'[{` + best + `}]')`)
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
			created_at, updated_at) VALUES (1, 1, 'movie-remux', 'aaaa', 'r', 'x', 'failed', ?, 2600, 2600)`, why)
		for i, q := range later {
			e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
				(?,1,'movie-remux',?,?,?)`, "later", map[bool]string{true: "rss", false: "movie"}[q[0] == 'R'],
				2700+int64(i)*100, q[1:])
		}
		v := upgradeVersion{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}
		s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
		if err != nil {
			t.Fatal(err)
		}
		u := s.explain(v, 0)
		next = u.Next
		return u.State
	}
	const miss, other = "miss on every provider", "provider status error"
	for _, c := range []struct {
		name, why string
		later     []string
		want      string
	}{
		{"tried", miss, nil, "below_cutoff"},
		{"plain RSS re-sighting", miss, []string{`R[{` + best + `}]`}, "below_cutoff"},
		{"RSS re-sighting with an older listing", miss, []string{`R[{` + best + `,"published":"1970-01-01T00:00:02.5Z"}]`}, "below_cutoff"},
		{"RSS listing published after the failure", miss, []string{`R[{` + best + `,"published":"1970-01-01T00:00:02.65Z"}]`}, "upgrade_available"},
		{"cached on a library provider after a miss", miss, []string{`R[{` + best + `,"cached_on":["rd"]}]`}, "upgrade_available"},
		{"cached elsewhere after a miss", miss, []string{`R[{` + best + `,"cached_on":["pm"]}]`}, "below_cutoff"},
		{"cached after another failure", other, []string{`R[{` + best + `,"cached_on":["rd"]}]`}, "below_cutoff"},
		{"a new covering search", miss, []string{`S[{` + best + `}]`}, "below_cutoff"},
		// Cache evidence counts only for a provider the release's indexer may go to.
		{"RD-only indexer cached on AD", miss, []string{`R[{` + best + `,"indexer":"rd-only","cached_on":["ad"]}]`}, "below_cutoff"},
		{"RD-only indexer cached on RD", miss, []string{`R[{` + best + `,"indexer":"rd-only","cached_on":["rd"]}]`}, "upgrade_available"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := state(t, c.why, c.later...); got != c.want {
				t.Errorf("%s, want %s", got, c.want)
			}
			// Why? is told the server's decision: tried, and when it failed.
			tried := c.want == "below_cutoff"
			if next == nil || next.Tried != tried || (next.TriedAt != nil) != tried || tried && next.TriedAt.UnixMilli() != 2600 {
				t.Errorf("next %+v, want tried %v at 2600", next, tried)
			}
		})
	}
}

// Only a real try clears Upgrade found, timed when it failed: a download never started (Pause was on) or ended by a
// removal is no try, as History and Why? say; one created before the sighting that failed after it is one; the
// failure's time is its History row's, so a later touch of the download does not move it.
func TestUpgradeFoundCountsOnlyARealTryWhenItFailed(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',1,'movie-remux','movie',2500,'[{"release":"Best","info_hash":"aaaa","accepted":true,"upgrade":true,"score":9000}]')`)
	fail := func(why string, created, failed int64) int64 {
		t.Helper()
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
			created_at, updated_at) VALUES (1, 1, 'movie-remux', 'aaaa', 'r', 'x', 'failed', ?, ?, ?)`, why, created, failed)
		var id int64
		if err := e.st.Reader().QueryRow(`SELECT MAX(id) FROM download`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		store.HistoryFailure(context.Background(), e.st.Writer(), id)
		return id
	}
	v := upgradeVersion{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}
	state := func() string {
		t.Helper()
		s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.explain(v, 0).State
	}
	fail("route ended: router: downloads are paused", 2600, 2600)
	fail("copy removed by admin", 2600, 2600)
	if got := state(); got != "upgrade_available" {
		t.Fatalf("not started and removal-ended: %s, want upgrade_available", got)
	}
	// Failed before the search's sighting: tried too (corrected in phase Z: a search's failed release is tried
	// whenever it failed), timed by its History row, so a later touch of the row (updated_at) does not move it.
	id := fail("miss on every provider", 2000, 2400)
	e.exec(`UPDATE download SET updated_at = 3000 WHERE id = ?`, id)
	s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
	if err != nil {
		t.Fatal(err)
	}
	if u := s.explain(v, 0); u.State != "below_cutoff" || u.Next == nil || u.Next.TriedAt == nil || u.Next.TriedAt.UnixMilli() != 2400 {
		t.Fatalf("failed before the sighting: %+v, want below_cutoff tried at 2400", u)
	}
	// Started before the sighting, failed after it: tried.
	fail("miss on every provider", 2400, 2600)
	if got := state(); got != "below_cutoff" {
		t.Fatalf("failed after the sighting: %s, want below_cutoff", got)
	}
}

// A search that lists a release which already failed for the title and library is no news: the search's own route
// ran right after it and skipped the known miss; a real retry writes a newer download row, whose result then decides.
// So for a search record a release with a failed try is tried, at that failure, whether it failed before or after the
// search and before or after the current file (Conclave: the release failed 56 s before the current file was
// imported). A release with no failure is found; a candidate without a hash is never tried; an RSS record keeps its
// own rule (TestUpgradeFoundStaysTriedAfterAnRSSResighting).
func TestUpgradeFoundStaysTriedForASearchThatSawAFailedRelease(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	// Alpha's current file is from 1000; a search at 2500 sees Best again, no download after it.
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',1,'movie-remux','movie',2500,'[{"release":"Best","info_hash":"aaaa","accepted":true,"upgrade":true,"score":9000}]')`)
	v := upgradeVersion{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}
	explain := func() *UpgradeInfo {
		t.Helper()
		s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.explain(v, 0)
	}
	if u := explain(); u.State != "upgrade_available" || u.Next == nil || u.Next.Tried {
		t.Fatalf("no failure: %+v, want upgrade_available", u)
	}
	// Failed before the current file was imported (and so before the search).
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		created_at, updated_at) VALUES (1, 1, 'movie-remux', 'AAAA', 'r', 'x', 'failed', 'miss on every provider', 900, 944)`)
	u := explain()
	if u.State != "below_cutoff" || u.Next == nil || !u.Next.Tried || u.Next.TriedAt == nil || u.Next.TriedAt.UnixMilli() != 944 {
		t.Fatalf("failed before the file: %+v (next %+v), want below_cutoff, tried at 944", u, u.Next)
	}

	// A candidate without a hash is never tried, even against a failure stored under an empty key.
	it := evItem{fileAt: 1000}
	recs := []evRecord{{at: 2500, search: true, source: "search", target: "movie",
		cands: []evCand{{Release: "NoHash", Accepted: true, Upgrade: true, Score: 9000}}}}
	if next, _ := judgeUpgrade(it, recs, map[string]failedTry{"": {at: 900, miss: true}}, nil); next == nil || next.Tried {
		t.Fatalf("no hash: %+v, want found", next)
	}
}

// seriesUpgrades explains The Show's tv-2160p through both file readers (the title page's and the list rows'): per
// episode whether an upgrade was found, and the version's Seen count.
func seriesUpgrades(t *testing.T, e *manageEnv) (map[int64]bool, int) {
	t.Helper()
	v := upgradeVersion{id: 10, titleID: 10, key: "tv-2160p", kind: "series", state: "filled"}
	s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
	if err != nil {
		t.Fatal(err)
	}
	vf, err := e.web.readVersionFiles(context.Background(), e.st.Reader(), []any{int64(10)})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.web.upgradesWith(context.Background(), e.st.Reader(), []upgradeVersion{v}, vf.items, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := s.episodeSeen(v)
	if got := rows.episodeSeen(v); len(got) != len(seen) || got[101] != seen[101] || got[102] != seen[102] {
		t.Fatalf("the two file readers differ: %v and %v", seen, got)
	}
	return seen, s.explain(v, 0).Seen
}

// T32: a sighting of the torrent an episode's file came from is no upgrade of that episode; another episode of the
// same pack still counts it.
func TestUpgradeSkipsTheHeldTorrent(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (77, 10, 10, 'tv-2160p', 'ffff', 'The.Show.S01.2160p.WEB-DL-FLUX', 'x', 'imported', 900, 900)`)
	e.exec(`UPDATE link SET download_id = 77 WHERE id = 900`)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',10,'tv-2160p','s01',6000,'[{"release":"The.Show.S01.2160p.WEB-DL-FLUX","info_hash":"FFFF","accepted":true,
		  "upgrade":true,"score":3375,"upgrade_reason":"better format score: 3375 over 3225","episodes":["S01E01","S01E02"]}]')`)
	seen, n := seriesUpgrades(t, e)
	if seen[101] || !seen[102] || n != 1 {
		t.Fatalf("held torrent: seen %v, count %d; want S01E02 only", seen, n)
	}
}

// An adopted episode file has no download: the hash its link stores (router.LinkStoredHashSQL, here the adopted
// source's) is the torrent it came from, and a sighting of it is no upgrade of it either.
func TestUpgradeSkipsTheAdoptedTorrent(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`UPDATE link SET download_id = NULL, adopted = 1, adopted_source = '{"hash":"ffff"}' WHERE id = 900`)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',10,'tv-2160p','s01',6000,'[{"release":"The.Show.S01.2160p.WEB-DL-FLUX","info_hash":"FFFF","accepted":true,
		  "upgrade":true,"score":3375,"upgrade_reason":"better format score: 3375 over 3225","episodes":["S01E01","S01E02"]}]')`)
	seen, n := seriesUpgrades(t, e)
	if seen[101] || !seen[102] || n != 1 {
		t.Fatalf("adopted torrent: seen %v, count %d; want S01E02 only", seen, n)
	}
}

// T32: an import that replaced the file at the same path keeps the link's created time; a sighting older than the
// download of the file now there judged the old file and does not count. A newer better release still does.
func TestUpgradeEvidenceFloorIsTheCurrentDownload(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	v := upgradeVersion{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}
	state := func() *UpgradeInfo {
		t.Helper()
		s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.explain(v, 0)
	}
	if u := state(); u.State != "upgrade_available" {
		t.Fatalf("seeded sighting: %+v", u)
	}
	// The file at Alpha's path now comes from a download made at 3000; the link still says 1000.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (78, 1, 1, 'movie-remux', 'cccc', 'Alpha.2020.2160p.WEB-DL-NEW', 'x', 'imported', 3000, 3000)`)
	e.exec(`UPDATE link SET download_id = 78 WHERE id = 902`)
	if u := state(); u.State != "below_cutoff" || u.Next != nil {
		t.Fatalf("sighting older than the current download: %+v", u)
	}
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',1,'movie-remux','movie',4000,'[{"release":"Alpha.2020.2160p.WEB-DL-NEW","info_hash":"CCCC","accepted":true,"upgrade":true,"score":9000},
		  {"release":"Alpha.2020.2160p.BluRay.REMUX-NEWER","info_hash":"dddd","accepted":true,"upgrade":true,"score":4000}]')`)
	if u := state(); u.State != "upgrade_available" || u.Next.Release != "Alpha.2020.2160p.BluRay.REMUX-NEWER" {
		t.Fatalf("newer better release: %+v", u)
	}
}

// T20: a pack that only fills episodes with no file is no upgrade of the held ones; a pack that upgrades and also
// fills still is.
func TestUpgradeIgnoresFillOnlyPacks(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',10,'tv-2160p','s01',6000,'[{"release":"The.Show.S01.720p.WEB-DL-FILL","info_hash":"aaaa","accepted":true,
		  "upgrade":true,"score":100,"upgrade_reason":"fills 1 episode(s) with no file; 2 episode(s) keep their file",
		  "episodes":["S01E01","S01E02","S01E03"]}]')`)
	if seen, n := seriesUpgrades(t, e); len(seen) != 0 || n != 0 {
		t.Fatalf("fill-only pack: seen %v, count %d", seen, n)
	}
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',10,'tv-2160p','s01',7000,'[{"release":"The.Show.S01.720p.WEB-DL-FILL","info_hash":"aaaa","accepted":true,
		  "upgrade":true,"score":100,"upgrade_reason":"fills 1 episode(s) with no file; 2 episode(s) keep their file",
		  "episodes":["S01E01","S01E02","S01E03"]},
		 {"release":"The.Show.S01.2160p.WEB-DL-UP","info_hash":"bbbb","accepted":true,"upgrade":true,"score":3000,
		  "upgrade_reason":"better quality: WEBDL-2160p over WEBDL-1080p; fills 1 episode(s) with no file",
		  "episodes":["S01E01","S01E02","S01E03"]}]')`)
	if seen, n := seriesUpgrades(t, e); !seen[101] || !seen[102] || n != 2 {
		t.Fatalf("upgrading pack that also fills: seen %v, count %d", seen, n)
	}
}

// A mixed pack (accepted because it upgrades some episodes) is no upgrade of an episode whose file it keeps; the
// episode it upgrades still counts it, and so does the season. The episode's stored Releases list still shows the
// pack, with the same counts.
func TestUpgradeSkipsAKeptEpisode(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,results,accepted,created_at,candidates) VALUES
		('search',10,'tv-2160p','s01',1,1,6000,'[{"release":"The.Show.S01.2160p.WEB-DL-MIXED","info_hash":"aaaa",
		  "accepted":true,"upgrade":true,"score":3000,
		  "upgrade_reason":"better quality: WEBDL-2160p over WEBDL-1080p; 1 episode(s) keep their file",
		  "episodes":["S01E01","S01E02","S01E03"],"kept":["S01E01"]}]')`)
	if seen, n := seriesUpgrades(t, e); seen[101] || !seen[102] || n != 1 {
		t.Fatalf("mixed pack: seen %v, count %d", seen, n)
	}
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Source == "none" || len(r.Candidates) != 1 || r.Candidates[0].Release != "The.Show.S01.2160p.WEB-DL-MIXED" ||
		r.Results != 1 || r.Accepted != 1 {
		t.Fatalf("kept episode's stored releases: source %q, %d rows, results %d, accepted %d", r.Source,
			len(r.Candidates), r.Results, r.Accepted)
	}
}

// A release on the library's blocklist (an Undo's, or any other source; by hash, else by its name's key) is no upgrade
// found and Why does not name it: the next one is. Another library's block does not count.
func TestUpgradeLeavesOutABlockedRelease(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('search',1,'movie-remux','movie',2500,'[{"release":"Best","info_hash":"AAAA","accepted":true,"upgrade":true,"score":9000},
		  {"release":"Second","accepted":true,"upgrade":true,"score":5000}]')`)
	v := upgradeVersion{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}
	explain := func() *UpgradeInfo {
		t.Helper()
		s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.explain(v, 0)
	}
	block := func(library, key string) {
		e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, reason, source, created_at)
			VALUES (?, ?, 1, 'the upgrade was undone by ann', 'undo:ann', 3000)`, library, key)
	}
	block("movie-web", "aaaa")
	if u := explain(); u.State != "upgrade_available" || u.Next.Release != "Best" {
		t.Fatalf("another library's block: %+v", u)
	}
	block("movie-remux", "aaaa")
	if u := explain(); u.State != "upgrade_available" || u.Next.Release != "Second" {
		t.Fatalf("one blocked: %+v", u)
	}
	block("movie-remux", router.BlocklistKey("", "Second"))
	if u := explain(); u.State != "below_cutoff" || u.Next != nil {
		t.Fatalf("both blocked: %+v", u)
	}
}
