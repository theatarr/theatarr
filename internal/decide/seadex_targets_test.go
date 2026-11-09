package decide

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Regression tests for the third SeaDex review (items B, D, E, H, K, L, P and the LOW items).

func seadexExec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.Writer().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func seadexSnapshotRow(t *testing.T, st *store.Store, s indexer.SeaDexSnapshot) {
	t.Helper()
	body, _ := json.Marshal(s)
	seadexExec(t, st, `INSERT OR REPLACE INTO metadata_cache(source,key,status,body,fetched_at,expires_at)
		VALUES('seadex-standing',?,200,?,0,253402214400000)`, s.AniList, body)
}

// configuredState is a prefer-on version's configured standing over the given snapshots (recorded with upstream
// facts) and mappings.
func configuredState(snapshots []indexer.SeaDexSnapshot, titles map[int64][]int, titleID int64) context.Context {
	for i := range snapshots {
		snapshots[i].Torrents = append([]indexer.SeaDexStanding(nil), snapshots[i].Torrents...)
		for j := range snapshots[i].Torrents {
			_ = json.Unmarshal([]byte(`{"facts":{}}`), &snapshots[i].Torrents[j])
		}
	}
	return legacyState(snapshots, titles, titleID)
}

// legacyState is configuredState over snapshots as stored (legacy ones without facts).
func legacyState(snapshots []indexer.SeaDexSnapshot, titles map[int64][]int, titleID int64) context.Context {
	state := indexStanding(seaDexState{snapshots: snapshots, titles: titles, stored: true, configured: true})
	return WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), titleID)
}

func bestFormatEngine(t *testing.T) *Engine {
	t.Helper()
	e := testEngine(t)
	f, err := format.ParseTrashFormat(format.Radarr, []byte(`{"trash_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Best","specifications":[{"name":"Best","implementation":"SeaDexSpecification","required":true,"fields":{"value":"best"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f.ID = 77
	p := e.Profile("remux-2160p", format.Radarr)
	p.Formats = []*format.Format{f}
	p.Profile.FormatItems = []format.FormatItem{{Format: 77, Score: 5000}}
	p.Profile.MinFormatScore = 0
	return e
}

// B: a prefer-off version's held file keeps the standing it was stored with whatever standing the operation pinned
// (shadow RSS, wanted, devices and live replay all load files under a pin before any version's policy applies).
func TestPreferOffHeldFileIgnoresPinnedStanding(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	hash := strings.Repeat("a", 40)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Dune: Part Two',0,0)`)
	seadexExec(t, st, `INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-remux',0,0)`)
	body, _ := json.Marshal(ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: hash})
	seadexExec(t, st, `INSERT INTO link(id,title_id,path,target,file_metadata,created_at,updated_at) VALUES(1,1,'/l','/t',?,0,0)`, string(body))
	seadexExec(t, st, `INSERT INTO link_version VALUES(1,1)`)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, IsBest: true, Preference: 2}}}))
	e := bestFormatEngine(t)
	r := rel("Dune.Part.Two.2024.2160p.BluRay.REMUX-GRP", "public", 60*gb)
	title := dune
	title.ID = 1
	plainFile, err := LoadExisting(context.Background(), st.Reader(), 1)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := e.Decide(context.Background(), r, title, Version{Key: "movie-remux"}, plainFile)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	held, err := LoadExisting(pinned, st.Reader(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if held.SeaDex != format.SeaDexNone || held.SeaDexPreference != 0 {
		t.Fatalf("an unconfigured load rewrote the held file: %+v", held)
	}
	d, err := e.Decide(pinned, r, title, Version{Key: "movie-remux"}, held)
	if err != nil {
		t.Fatal(err)
	}
	if d.Existing.Score != plain.Existing.Score || d.Accepted != plain.Accepted || reasons(d) != reasons(plain) {
		t.Fatalf("prefer-off decision depends on pinned standing: %+v vs %+v", d, plain)
	}
	// The same load for a prefer-on version takes the standing inside its decision.
	e.Policy.Versions["movie-remux"].PreferSeaDex = true
	on, err := e.Decide(pinned, r, title, Version{Key: "movie-remux"}, held)
	if err != nil {
		t.Fatal(err)
	}
	if on.Existing.Score != 5000 {
		t.Fatalf("prefer-on decision did not take the stored standing: %d", on.Existing.Score)
	}
}

// P: an unidentified native quality is held only for prefer-on versions.
func TestPreferOffUnknownQualityAsBefore(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	p := e.Profile("remux-2160p", format.Radarr)
	q, _ := profile.QualityByName(format.Radarr, "Unknown")
	p.Profile.Items = []profile.Item{{Quality: &q, Allowed: true}}
	p.Profile.MinFormatScore = 0
	r := rel("Dune.Part.Two.2024.Unknown", "public", 0)
	r.Kind, r.Flags = "seadex", []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	vp := *e.Policy.Versions["movie-remux"]
	d, err := e.Decide(context.Background(), r, dune, Version{Key: "movie-remux", Policy: &vp}, nil)
	if err != nil || !d.Accepted {
		t.Fatalf("prefer-off Unknown allowed by the profile was held: %+v %v", d, err)
	}
	vp.PreferSeaDex = true
	d, err = e.Decide(context.Background(), r, dune, Version{Key: "movie-remux", Policy: &vp}, nil)
	if err != nil || d.Accepted || reasons(d) != "SeaDexQualityPending" {
		t.Fatalf("prefer-on Unknown native quality not held: %+v %v", d, err)
	}
	// Not allowed by the profile: prefer-off rejects it permanently as any Unknown release.
	p.Profile.Items = nil
	vp.PreferSeaDex = false
	d, err = e.Decide(context.Background(), r, dune, Version{Key: "movie-remux", Policy: &vp}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rj := range d.Rejections {
		if rj.Reason == "SeaDexQualityPending" || rj.Temporary {
			t.Fatalf("prefer-off Unknown rejection became temporary: %+v", d.Rejections)
		}
	}
}

// E: a stored row that decodes but has malformed targets is skipped; the rest still load.
func TestMalformedTargetsRowSkipped(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	hash := strings.Repeat("a", 40)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','X',1,0,0)`)
	seadexExec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at)
		VALUES('seadex-standing','9',200,'{"anilist":9,"targets":["bad"],"torrents":[]}',0,253402214400000)`)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Targets: []indexer.AniListTarget{{TMDb: 1}},
		Torrents: []indexer.SeaDexStanding{{InfoHash: hash, IsBest: true, Preference: 2}}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	if err := SeaDexError(ctx); err != nil {
		t.Fatalf("a malformed row broke every standing load: %v", err)
	}
	x := &ExistingFile{InfoHash: hash}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{x}); err != nil || x.SeaDexEntry != 1 || x.SeaDex != format.SeaDexBest {
		t.Fatalf("valid rows lost with the malformed one: %+v %v", x, err)
	}
}

// D: curator alternatives come from the file's own entry; an unmatched series file takes only the entries covering
// its episodes, and a file without known episodes none.
func TestMultiEntrySeriesAlternativesKeepSeason(t *testing.T) {
	st := linkedDB(t)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',123,0,0)`)
	s1, s2, held := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("a", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 10, Targets: []indexer.AniListTarget{{TVDB: 123, Season: 1}},
		Torrents: []indexer.SeaDexStanding{{InfoHash: held, Quality: "Remux-2160p", IsBest: true, Preference: 2}}}))
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 20, Targets: []indexer.AniListTarget{{TVDB: 123, Season: 2}},
		Torrents: []indexer.SeaDexStanding{{InfoHash: s2, Quality: "Remux-2160p", IsBest: true, Preference: 3, Size: 60 * gb}}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	e := testEngine(t)
	e.Profile("remux-2160p", format.Radarr).Profile.CutoffFormatScore = 0
	vp := *e.Policy.Versions["movie-remux"]
	vp.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &vp}

	// The held file is its own (season 1) entry's best: season 2's curated torrent is no reason to search.
	own := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: held, seaDexEpisodes: [][2]int{{1, 1}}}
	// An ordinary season 1 file: only season 1's torrents (the held best) are alternatives.
	plainS1 := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: s1, seaDexEpisodes: [][2]int{{1, 2}}}
	// An ordinary season 2 file: season 2's curated torrent is.
	plainS2 := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: strings.Repeat("b", 40), seaDexEpisodes: [][2]int{{2, 1}}}
	// Episodes unknown: no curator reason at all.
	unknown := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: strings.Repeat("c", 40)}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{own, plainS1, plainS2, unknown}); err != nil {
		t.Fatal(err)
	}
	if own.SeaDexEntry != 10 || len(own.SeaDexAlternatives) != 1 || own.SeaDexAlternatives[0].InfoHash != held {
		t.Fatalf("matched file took another entry's alternatives: %+v", own)
	}
	if len(plainS1.SeaDexAlternatives) != 1 || plainS1.SeaDexAlternatives[0].InfoHash != held {
		t.Fatalf("season 1 file took season 2 alternatives: %+v", plainS1.SeaDexAlternatives)
	}
	if len(plainS2.SeaDexAlternatives) != 1 || plainS2.SeaDexAlternatives[0].InfoHash != s2 {
		t.Fatalf("season 2 file lost its entry's alternatives: %+v", plainS2.SeaDexAlternatives)
	}
	if len(unknown.SeaDexAlternatives) != 0 {
		t.Fatalf("file without episodes got curator alternatives: %+v", unknown.SeaDexAlternatives)
	}
	for _, c := range []struct {
		f    *ExistingFile
		want bool
	}{{own, false}, {plainS2, true}, {unknown, false}} {
		got, err := e.QualityCutoffNotMet(v, c.f, &dune)
		if err != nil || got != c.want {
			t.Fatalf("cutoff for %s = %v, want %v (%v)", c.f.InfoHash, got, c.want, err)
		}
	}
}

// D through the loader: LoadEpisodes gives each file its own episodes for the scope.
func TestLoadEpisodesScopesAlternatives(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',123,0,0)`)
	seadexExec(t, st, `INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(9,1,'tv',0,0)`)
	seadexExec(t, st, `INSERT INTO episode(id,title_id,season,number,name,created_at,updated_at) VALUES(101,1,1,1,'a',0,0),(201,1,2,1,'b',0,0)`)
	seadexExec(t, st, `INSERT INTO version_source VALUES (9, 'owner', 1)`)
	seadexExec(t, st, `INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, scene_name, added_at)
		VALUES (1, 9, 1, 'owner', 1, '/s1.mkv', 'Bluray-1080p', 's1', 1), (2, 9, 1, 'owner', 2, '/s2.mkv', 'Bluray-1080p', 's2', 1)`)
	seadexExec(t, st, `INSERT INTO version_file_episode VALUES (1, 101), (2, 201)`)
	s2 := strings.Repeat("2", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 20, Targets: []indexer.AniListTarget{{TVDB: 123, Season: 2}},
		Torrents: []indexer.SeaDexStanding{{InfoHash: s2, Quality: "Bluray-1080p Remux", IsBest: true, Preference: 3}}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	eps, err := LoadEpisodes(ctx, st.Reader(), 1, 9)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.File == nil {
			t.Fatalf("episode %d has no file", ep.ID)
		}
		got := len(ep.File.SeaDexAlternatives)
		if ep.Season == 1 && got != 0 || ep.Season == 2 && got != 1 {
			t.Fatalf("season %d file alternatives: %+v", ep.Season, ep.File.SeaDexAlternatives)
		}
	}
}

// L: a batch listed under two of the title's mapped entries keeps its agreeing rank and best flag, and a zero-rank
// tombstone under another entry is no conflict.
func TestBatchUnderTwoEntriesAndTombstone(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{
		{AniList: 10, Torrents: []indexer.SeaDexStanding{{InfoHash: h, IsBest: true, Preference: 2}}},
		{AniList: 20, Torrents: []indexer.SeaDexStanding{{InfoHash: h, IsBest: true, Preference: 2}}},
		{AniList: 30, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 0}}},
	}, map[int64][]int{1: {10, 20, 30}}, 1)
	r := RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: h, Title: "[G] Show S01-S02 Batch"})
	if indexer.SeaDexPreference(&r) != 2 || SeaDexRank(&r) != format.SeaDexBest {
		t.Fatalf("batch lost its standing: %+v", r.Flags)
	}
	x := resolveSeaDex(ctx, &ExistingFile{InfoHash: h})
	if x.SeaDexPreference != 2 || x.SeaDex != format.SeaDexBest || x.SeaDexEntry != 0 {
		t.Fatalf("held batch: %+v", x)
	}
}

// LOW: an exact hash listed only under entries outside the title's mappings is no evidence: nothing retires.
func TestOutOfScopeMatchDoesNotRetire(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{
		{AniList: 10},
		{AniList: 99, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 3}}},
	}, map[int64][]int{1: {10}}, 1)
	x := resolveSeaDex(ctx, &ExistingFile{InfoHash: h, SeaDexPreference: 2, SeaDex: format.SeaDexBest})
	if x.SeaDexPreference != 2 || x.SeaDex != format.SeaDexBest {
		t.Fatalf("out-of-scope listing retired held standing: %+v", x)
	}
}

// K and LOW: a hashless held file matched by record id keeps its standing while the record's old hash awaits
// retirement, and afterwards takes none (the new hash is never claimed as held).
func TestRecordEditKeepsStandingUntilConfirmed(t *testing.T) {
	t.Parallel()
	id := "6vdej49pr26gca0"
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	held := &ExistingFile{SceneName: "Show (" + id + ")", SeaDexPreference: 3, SeaDex: format.SeaDexBest}
	pending := configuredState([]indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{
		{InfoHash: a, RecordID: id, Preference: 3, MissingSince: 1000}, {InfoHash: b, RecordID: id, Preference: 1}}}}, nil, 1)
	if got := resolveSeaDex(pending, held); got.SeaDexPreference != 3 {
		t.Fatalf("pending record edit cleared held standing: %+v", got)
	}
	confirmed := configuredState([]indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{
		{InfoHash: a, RecordID: id, Preference: 0}, {InfoHash: b, RecordID: id, Preference: 3}}}}, nil, 1)
	if got := resolveSeaDex(confirmed, held); got.SeaDexPreference != 0 {
		t.Fatalf("hashless file took the edited record's new hash standing: %+v", got)
	}
}

// MarkSeaDex keeps a release's own entry; refresh keeps the entry's own rank.
func TestPropagationKeepsNativeEntry(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 40)
	a := indexer.Release{InfoHash: hash, Indexer: "seadex-a", Extras: map[string]string{"anilist": "1", "seadex_id": "111111111111111"}, Flags: []string{indexer.FlagSeaDex}}
	b := indexer.Release{InfoHash: hash, Indexer: "seadex-b", Extras: map[string]string{"anilist": "2", "seadex_id": "222222222222222"}, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}}
	other := indexer.Release{InfoHash: hash, Indexer: "nyaa"}
	rs := []indexer.Release{a, b, other}
	indexer.MarkSeaDex(rs)
	if rs[0].Extras["anilist"] != "1" || rs[1].Extras["anilist"] != "2" || rs[2].Extras["anilist"] != "" {
		t.Fatalf("provenance overwritten or guessed: %v %v %v", rs[0].Extras, rs[1].Extras, rs[2].Extras)
	}
	ctx := configuredState([]indexer.SeaDexSnapshot{
		{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}}},
		{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 3}}},
	}, map[int64][]int{1: {1}}, 1)
	got := RefreshSeaDexRelease(ctx, rs[0])
	if got.Extras["anilist"] != "1" || indexer.SeaDexPreference(&got) != 1 {
		t.Fatalf("entry 1 release took entry 2's identity or rank: %+v", got)
	}
}

// LOW (theatarr decide): the CLI's fresh answers, which it cannot store, override the stored rows for its decisions.
func TestOverlayUsesFreshAnswer(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	h := strings.Repeat("a", 40)
	seadexSnapshotRow(t, st, indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 0}}})
	on := &policy.VersionPolicy{PreferSeaDex: true}
	stored, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	stored = WithSeaDex(stored, []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, IsBest: true, Preference: 2}}})})
	if x := resolveSeaDex(WithSeaDexPolicy(stored, nil, on), &ExistingFile{InfoHash: h}); x.SeaDexPreference != 0 {
		t.Fatalf("a pinned operation took an unstored answer: %+v", x)
	}
	overlay := WithSeaDexOverlay(context.Background(), st.Reader(), []indexer.SeaDexSnapshot{
		factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, IsBest: true, Preference: 2}}}),
		{AniList: 2, Partial: true, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 3}}},
	})
	if x := resolveSeaDex(WithSeaDexPolicy(overlay, nil, on), &ExistingFile{InfoHash: h}); x.SeaDexPreference != 3 {
		t.Fatalf("the CLI ignored its fresh answer: %+v", x)
	}
}
