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
)

func TestOwnEntryOutsideCoverage(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',100,0,0)`,
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:11','auto',100,'[[1,1],[1,2]]',0)`,
	} {
		seadexExec(t, st, q)
	}
	hash := strings.Repeat("a", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{
		{InfoHash: hash, Quality: "Bluray-2160p Remux", Preference: 1, Size: 10 * 1024 * 1024},
		{InfoHash: strings.Repeat("b", 40), Quality: "Bluray-2160p Remux", IsBest: true, Preference: 2, Size: 10 * 1024 * 1024},
	}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	f := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, InfoHash: hash, SeaDexEntry: 11, seaDexEpisodes: [][2]int{{1, 13}}}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
		t.Fatal(err)
	}
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	vp := *v.Policy
	vp.PreferSeaDex = true
	v.Policy = &vp
	p := e.Profile(vp.Profile, format.Sonarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: profile.MustQuality(format.Sonarr, "Bluray-2160p Remux"), MaxSize: &maxSize})
	title := show()
	title.Episodes = append(title.Episodes, Episode{ID: 113, Season: 1, Number: 13, Runtime: 45, Monitored: true})
	resolveFileAlternatives(f, &title)
	wanted, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.SeaDexAlternatives) > 0 || wanted {
		t.Fatalf("entry 11 mapped only E1-E2: E13 got %d alternatives and wanted=%v", len(f.SeaDexAlternatives), wanted)
	}
}

func TestAlternativeEpisodeSize(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	vp := *v.Policy
	vp.PreferSeaDex = true
	v.Policy = &vp
	p := e.Profile(vp.Profile, format.Sonarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	q := profile.MustQuality(format.Sonarr, "Bluray-2160p Remux")
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: q, MaxSize: &maxSize})
	title := show()
	hash := strings.Repeat("a", 40)
	f := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, seaDexEpisodes: [][2]int{{1, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: hash, Quality: q.Name, Preference: 3, Size: 100 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "The.Show.S01E01.2160p.BluRay.REMUX-GRP.mkv")
	r := rel("The.Show.S01E01.2160p.BluRay.REMUX-GRP", "public", 100*1024*1024)
	r.InfoHash = hash
	d, err := e.Decide(context.Background(), r, title, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reasons(d), "AboveMaximumSize") {
		t.Fatalf("control did not reject the 100 MiB episode: %v", d.Rejections)
	}
	resolveFileAlternatives(f, &title)
	got, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatalf("curator wanted for 100 MiB single episode even though decider rejects at 45 MiB; mapping covers whole season")
	}
}

func TestSeriesIncomingStandingStaleOnImport(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',100,0,0)`,
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:11','auto',100,'[[1,1],[1,2]]',0)`,
	} {
		seadexExec(t, st, q)
	}
	hash := strings.Repeat("a", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{
		{InfoHash: hash, Quality: "Bluray-2160p Remux", Preference: 1, Size: 100 * 1024 * 1024},
		{InfoHash: strings.Repeat("b", 40), Quality: "Bluray-2160p Remux", IsBest: true, Preference: 2, Size: 100 * 1024 * 1024},
	}}))
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	vp := *v.Policy
	vp.PreferSeaDex = true
	v.Policy = &vp
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &vp), 1)
	rel := indexer.Release{Title: "The.Show.S01E01.2160p.BluRay.REMUX-GRP", InfoHash: hash, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}}
	got := RefreshSeaDexRelease(ctx, rel)
	plain := RefreshSeaDexRelease(ctx, indexer.Release{Title: rel.Title, InfoHash: hash})
	f := LinkedFile(policy.KindSeries, vp.Sonarr, Release{Release: got}, "Bluray-2160p Remux")
	if err = ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
		t.Fatal(err)
	}
	held := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, SceneName: "The.Show.S01E01.2160p.BluRay.REMUX-GRP", InfoHash: strings.Repeat("c", 40), SeaDexPreference: 2}
	title := show()
	allowed, why, err := e.ImportUpgrade(&title, v, f, held)
	if err != nil {
		t.Fatal(err)
	}
	if rank := indexer.SeaDexPreference(&got); rank != 1 || f.SeaDexPreference != 1 || allowed {
		t.Fatalf("current rank=1 but refreshed stale rank=%d, refreshed unflagged rank=%d, import rank=%d; allowed=%v, reason=%s", rank, indexer.SeaDexPreference(&plain), f.SeaDexPreference, allowed, why)
	}
}

func TestKnownEpisodeRuntimesDespiteUnknownSeriesRuntime(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	v := tvVersion(e, "tv")
	vp := *v.Policy
	vp.PreferSeaDex = true
	v.Policy = &vp
	p := e.Profile(vp.Profile, format.Sonarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	q := profile.MustQuality(format.Sonarr, "Bluray-2160p Remux")
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: q, MaxSize: &maxSize})
	title := show()
	title.Runtime = 0
	hash := strings.Repeat("a", 40)
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{1, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: hash, Quality: q.Name, Preference: 3, Size: 100 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "The.Show.S01E01.mkv", "The.Show.S01E02.mkv", "The.Show.S01E03.mkv")
	r := rel("The.Show.S01.2160p.BluRay.REMUX-GRP", "public", 100*1024*1024)
	d, err := e.Decide(context.Background(), r, title, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reasons(d), "AboveMaximumSize") || strings.Contains(reasons(d), "UnknownRuntime") {
		t.Fatalf("control did not accept episode runtimes for size: %v", d.Rejections)
	}
	resolveFileAlternatives(f, &title)
	wanted, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	if !wanted {
		t.Fatal("known runtimes for all 3 45-minute episodes rejected solely because series runtime is zero")
	}
}

func setStandingFeatures(t *testing.T, standing *indexer.SeaDexStanding, names ...string) {
	t.Helper()
	files := []map[string]any{}
	for _, name := range names {
		files = append(files, map[string]any{"name": name, "length": 10000000})
	}
	data, err := json.Marshal(map[string]any{"facts": map[string]any{"files": files, "isBest": standing.IsBest}})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, standing); err != nil {
		t.Fatal(err)
	}
}

func resolveFileAlternatives(f *ExistingFile, title *Title) {
	state := indexStanding(seaDexState{
		snapshots: []indexer.SeaDexSnapshot{{AniList: 11, Torrents: f.SeaDexAlternatives}},
		titles:    map[int64][]int{title.ID: {11}}, series: map[int64]bool{title.ID: true},
		cover: map[int64]map[int]*indexer.SeaDexEntryMapping{title.ID: {11: {Series: true, Seasons: map[int]bool{1: true}}}},
	})
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), title.ID)
	*f = resolveSeaDex(ctx, f)
}

func TestUnresolvedIncomingHasNoStanding(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}}})})
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	incoming := indexer.Release{InfoHash: hash, Extras: map[string]string{"anilist": "99"}, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}}
	got := RefreshSeaDexRelease(ctx, incoming)
	if rank := indexer.SeaDexPreference(&got); rank != 0 {
		t.Fatalf("unresolved incoming rank=%d", rank)
	}
	held := LinkedFile(policy.KindMovie, "", Release{Release: incoming}, "Unknown")
	resolved := resolveSeaDex(ctx, held)
	if resolved.SeaDexPreference != 3 {
		t.Fatal("unresolved held protection lost")
	}
}

func TestOwnUnmappedEntryHasNoAlternatives(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}}})})
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	f := &ExistingFile{InfoHash: hash, SeaDexEntry: 11, seaDexEpisodes: [][2]int{{1, 1}}}
	got := resolveSeaDex(ctx, f)
	if got.SeaDexPreference == 0 {
		t.Fatal("own identity lost")
	}
	if len(got.SeaDexAlternatives) != 0 {
		t.Fatal("own unmapped entry supplied alternatives")
	}
}
