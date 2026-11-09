package decide

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	am "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

// Cutoff and the curator search use the same synthetic season: a held S02 file's alternative is judged on the
// release a search for season 2 would return ("<Series> S02 ..."), not on the entry's lowest season. Here an
// alternate-cut map files that S02 release's files under other episodes, so the held S02E01 is not covered and the
// file is not curator-wanted (with S01 the files would map to S02E01 and the file would be wrongly wanted).
func TestCutoffUsesSearchedSeason(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	entry := *title.AnimeMap.Entries[1]
	entry.Episodes = []am.SE{{1, 12}, {2, 1}, {2, 2}}
	cut := &am.AltCutMap{Name: "Second season cut", Match: "S02", Entry: "al:2", Episodes: map[int][]am.SE{2: {{1, 2}}, 3: {{1, 3}}}}
	title.AnimeMap = animemap.New(title.TVDB, []*animemap.Entry{&entry}, []*am.AltCutMap{cut})
	alt := indexer.SeaDexStanding{Entry: 2, InfoHash: strings.Repeat("a", 40), Quality: q.Name, Preference: 3, Size: 30 * 1024 * 1024,
		RecordID: "abcdef012345678"}
	setStandingFeatures(t, &alt, "[Group] Generic - 02 (BD 1080p).mkv", "[Group] Generic - 03 (BD 1080p).mkv")
	held := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{alt}}
	wanted, err := e.QualityCutoffNotMet(v, held, &title)
	if err != nil {
		t.Fatal(err)
	}
	if wanted {
		t.Fatal("cutoff judged the S01 release; a search for the held S02 file returns an S02 release mapping elsewhere")
	}
}

// The format score minimum runs on the release a curator search would return: a profile minimum it cannot reach
// leaves the file not curator-wanted. A movie alternative whose facts list two features (a search for a movie drops
// it) is not wanted either.
func TestCuratorFormatAndShape(t *testing.T) {
	e := testEngine(t)
	p := e.Profile("remux-2160p", format.Radarr)
	p.Profile.CutoffFormatScore = 0
	vp := *e.Policy.Versions["movie-remux"]
	vp.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &vp}
	alt := func(files ...string) []indexer.SeaDexStanding {
		s := indexer.SeaDexStanding{InfoHash: strings.Repeat("a", 40), Quality: "Remux-2160p", Preference: 3, Size: 60 * gb,
			RecordID: "rec"}
		if len(files) > 0 {
			setStandingFeatures(t, &s, files...)
			s.Size = 60 * gb
		}
		return []indexer.SeaDexStanding{s}
	}
	f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, SeaDexAlternatives: alt("Dune.Part.Two.2024.mkv")}
	if wanted, err := e.QualityCutoffNotMet(v, f, &dune); err != nil || !wanted {
		t.Fatalf("control: wanted=%v %v", wanted, err)
	}
	p.Profile.MinFormatScore = 100000
	wanted, err := e.QualityCutoffNotMet(v, f, &dune)
	p.Profile.MinFormatScore = 0
	if err != nil || wanted {
		t.Fatalf("curator-wanted although the search's release is below the profile's minimum format score: %v %v", wanted, err)
	}
	f.SeaDexAlternatives = alt("Dune.Part.Two.2024.Disc1.mkv", "Dune.Part.Two.2024.Disc2.mkv")
	if wanted, err := e.QualityCutoffNotMet(v, f, &dune); err != nil || wanted {
		t.Fatalf("curator-wanted for a two-feature torrent a movie search drops: %v %v", wanted, err)
	}
}

// Local cache evidence read with the standing: a live miss on every provider of the debrid version (or uncached
// while the router adds cached torrents only) makes the curated torrent unavailable, so the file is not
// curator-wanted; an expired row is no evidence.
func TestCuratorCacheEvidence(t *testing.T) {
	st := linkedDB(t)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',123,0,0)`)
	s2 := strings.Repeat("2", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 20, Targets: []indexer.AniListTarget{{TVDB: 123, Season: 2}},
		Torrents: []indexer.SeaDexStanding{{InfoHash: s2, Quality: "Remux-2160p", IsBest: true, Preference: 3, Size: 60 * gb}}}))
	e := testEngine(t)
	e.Profile("remux-2160p", format.Radarr).Profile.CutoffFormatScore = 0
	vp := *e.Policy.Versions["movie-remux"]
	vp.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &vp}
	wanted := func() bool {
		t.Helper()
		ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
		if err != nil {
			t.Fatal(err)
		}
		ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
		f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: strings.Repeat("b", 40), seaDexEpisodes: [][2]int{{2, 1}}}
		if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
			t.Fatal(err)
		}
		got, err := e.QualityCutoffNotMet(v, f, &dune)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if !wanted() {
		t.Fatal("control: no cache evidence, the curated torrent is a reason to search")
	}
	far := int64(253402214400000)
	for _, tc := range []struct {
		state   string
		expires int64
		want    bool
	}{{"miss", far, false}, {"uncached", far, false}, {"cached", far, true}, {"miss", 1, true}} {
		seadexExec(t, st, `INSERT OR REPLACE INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
			VALUES (?, 'rd', ?, 'test', 0, ?)`, s2, tc.state, tc.expires)
		if got := wanted(); got != tc.want {
			t.Fatalf("provider_cache %s (expires %d): curator-wanted=%v, want %v", tc.state, tc.expires, got, tc.want)
		}
	}
}

// Completeness is judged over all the title's entries: while one mapped entry has no record, the held file's
// standing is unknown and it takes no curated alternatives (not curator-wanted), whatever its own entry.
func TestIncompleteTitleTakesNoAlternatives(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',123,0,0)`)
	s2 := strings.Repeat("2", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 20, Targets: []indexer.AniListTarget{{TVDB: 123, Season: 2}},
		Torrents: []indexer.SeaDexStanding{{InfoHash: s2, Quality: "Remux-2160p", IsBest: true, Preference: 3, Size: 60 * gb}}}))
	resolve := func() *ExistingFile {
		t.Helper()
		ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
		if err != nil {
			t.Fatal(err)
		}
		ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
		f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: strings.Repeat("b", 40), seaDexEpisodes: [][2]int{{2, 1}}}
		if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := resolve(); len(f.SeaDexAlternatives) != 1 {
		t.Fatalf("control: complete title lost its alternatives: %+v", f.SeaDexAlternatives)
	}
	seadexExec(t, st, `INSERT INTO title_external_id VALUES(1,'anilist','30')`) // mapped, never answered
	if f := resolve(); len(f.SeaDexAlternatives) != 0 {
		t.Fatalf("title with an unanswered mapped entry gave curated alternatives: %+v", f.SeaDexAlternatives)
	}
}

// The stored candidate release is the search's own: same title, flags and size as a search would build.
func TestCandidateReleaseMatchesSearch(t *testing.T) {
	t.Parallel()
	var s indexer.SeaDexStanding
	if err := json.Unmarshal([]byte(`{"quality":"Bluray-1080p","size":2000,"record_id":"rec1","info_hash":"`+strings.Repeat("A", 40)+
		`","preference":3,"is_best":true,"facts":{"id":"rec1","isBest":true,"tracker":"Nyaa","releaseGroup":"GRP","files":[{"name":"a.mkv","length":1000},{"name":"b.mkv","length":1000}]}}`), &s); err != nil {
		t.Fatal(err)
	}
	r, ok := indexer.SeaDexCandidateRelease(s, indexer.QuerySeries, "Show", 2, 2020)
	if !ok || r.Title != "Show S02 Bluray 1080p (rec1)" || r.Size != 2000 || indexer.SeaDexPreference(&r) != 3 ||
		SeaDexRank(&r) != format.SeaDexBest || r.InfoHash != strings.Repeat("a", 40) {
		t.Fatalf("candidate release %+v ok=%v", r, ok)
	}
	if _, ok := indexer.SeaDexCandidateRelease(s, indexer.QueryMovie, "Show", 0, 2020); ok {
		t.Fatal("a two-feature torrent is not a movie a search would return")
	}
}
