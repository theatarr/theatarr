package decide

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestChangedStandingStillAmbiguous(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	cover := map[int64]map[int]*indexer.SeaDexEntryMapping{1: {
		11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
		12: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
	}}
	mk := func(changed bool) context.Context {
		a := factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Size: 1, Preference: 2, IsBest: true}}})
		b := factsSnapshot(indexer.SeaDexSnapshot{AniList: 12, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Size: 1, Preference: 2, IsBest: true}}})
		if changed {
			b.Torrents[0].IsBest = false
			b = factsSnapshot(b)
			other := strings.Repeat("e", 40)
			a.Torrents = append(a.Torrents, factsSnapshot(indexer.SeaDexSnapshot{Torrents: []indexer.SeaDexStanding{{InfoHash: other, Size: 2, Preference: 2, IsBest: true}}}).Torrents[0])
			b.Torrents = append(b.Torrents, factsSnapshot(indexer.SeaDexSnapshot{Torrents: []indexer.SeaDexStanding{{InfoHash: other, Size: 2, Preference: 2, IsBest: true}}}).Torrents[0])
		}
		sc := &policy.SeaDexScoring{SizeWeight: 1}
		a = indexer.ConfigureSeaDex(a, sc)
		b = indexer.ConfigureSeaDex(b, sc)
		if changed && (a.Torrents[0].Preference != 2 || b.Torrents[0].Preference != 1) {
			t.Fatalf("bad fixture: ranks %d %d", a.Torrents[0].Preference, b.Torrents[0].Preference)
		}
		s := indexStanding(seaDexState{configured: true, titles: map[int64][]int{1: {11, 12}}, series: map[int64]bool{1: true}, cover: cover, snapshots: []indexer.SeaDexSnapshot{a, b}})
		return WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, s), 1)
	}
	f := &ExistingFile{InfoHash: h, seaDexEpisodes: [][2]int{{1, 1}}}
	old := resolveSeaDex(mk(false), f)
	if old.SeaDexPreference != 3 || old.SeaDexEntry != 0 {
		t.Fatalf("old=%+v", old)
	}
	now := resolveSeaDex(mk(true), &old)
	t.Logf("before rank=%d entry=%d; after both ranks decrease to 2,1: rank=%d best=%v alternatives=%d", old.SeaDexPreference, old.SeaDexEntry, now.SeaDexPreference, now.SeaDex, len(now.SeaDexAlternatives))
	original := factsSnapshot(indexer.SeaDexSnapshot{AniList: 13, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true}}})
	original = indexer.ConfigureSeaDex(original, nil)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pending, _ := indexer.ApplySeaDexObservation(&original, indexer.SeaDexSnapshot{AniList: 13}, at)
	retired, _ := indexer.ApplySeaDexObservation(&pending, indexer.SeaDexSnapshot{AniList: 13}, at.Add(12*time.Hour))
	if len(retired.Torrents) != 1 || retired.Torrents[0].Preference != 0 {
		t.Fatalf("retirement fixture: %+v", retired)
	}
	state := mk(true).Value(seaDexContext{}).(seaDexState)
	state.titles[1] = append(state.titles[1], 13)
	state.cover[1][13] = &indexer.SeaDexEntryMapping{Series: true, Episodes: map[[2]int]bool{{1, 1}: true}}
	state.snapshots = append(state.snapshots, pending)
	pendingFile := resolveSeaDex(WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, indexStanding(state)), 1), &old)
	if pendingFile.SeaDexPreference != 3 {
		t.Fatalf("grace lost rank: %d", pendingFile.SeaDexPreference)
	}
	state.snapshots[len(state.snapshots)-1] = retired
	afterRetire := resolveSeaDex(WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, indexStanding(state)), 1), &old)
	t.Logf("after entry 13 complete omissions 12h apart: tombstone rank=%d; other ranks 2 and 1; held rank=%d", retired.Torrents[0].Preference, afterRetire.SeaDexPreference)
	if now.SeaDexPreference != 2 || afterRetire.SeaDexPreference != 2 || len(now.SeaDexAlternatives) == 0 || len(afterRetire.SeaDexAlternatives) == 0 {
		t.Fatalf("retained rank 3 although no covering entry confirms it")
	}
}

func TestPartialEntryCoverage(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "tv")
	title := show()
	title.AnimeMap = animemap.New(title.TVDB, []*animemap.Entry{{Key: "al:11", Status: "auto", Episodes: []animemeta.SE{{1, 1}}}}, nil)
	ver, err := sonarr.ParseVersion(v.Policy.Sonarr)
	if err != nil {
		t.Fatal(err)
	}
	cour, _, mapped, _, err := newSeries(&title, ver).seadexCour("11", 1, title.AnimeMap)
	if err != nil || !mapped || len(cour) != 1 || cour[0].Number != 1 {
		t.Fatalf("bad cour fixture: %v %v %v", cour, mapped, err)
	}
	t.Logf("actual SeaDex decision entry covers only E%d", cour[0].Number)
	h := strings.Repeat("b", 40)
	alt := indexer.SeaDexStanding{InfoHash: h, Quality: q.Name, Preference: 3, Size: 20 * 1024 * 1024}
	setStandingFeatures(t, &alt, "The.Show.S01E01.mkv", "The.Show.S01E02.mkv")
	state := indexStanding(seaDexState{configured: true, titles: map[int64][]int{1: {11}}, series: map[int64]bool{1: true},
		cover:     map[int64]map[int]*indexer.SeaDexEntryMapping{1: {11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}}}},
		snapshots: []indexer.SeaDexSnapshot{{AniList: 11, Torrents: []indexer.SeaDexStanding{alt}}},
	})
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 1)
	held := &ExistingFile{Quality: q.Name, Revision: 1, InfoHash: strings.Repeat("c", 40), SeaDexEntry: 11, seaDexEpisodes: [][2]int{{1, 1}, {1, 2}}}
	got := resolveSeaDex(ctx, held)
	want, err := e.QualityCutoffNotMet(v, &got, &title)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("entry maps E1 only; held spans E1/E2; candidate lists both; alternatives=%d wanted=%v", len(got.SeaDexAlternatives), want)
	if want {
		t.Fatal("entry does not cover entire held file, yet file was curator-wanted")
	}
}

func TestAltCutReleaseMapping(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	title.Name = "Isekai Story Directors Cut"
	alt := indexer.SeaDexStanding{Entry: 1, InfoHash: strings.Repeat("d", 40), Quality: q.Name, Preference: 3, Size: 20 * 1024 * 1024}
	setStandingFeatures(t, &alt, "[Group] Generic - 02 (BD 1080p).mkv", "[Group] Generic - 03 (BD 1080p).mkv")
	held := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{1, 2}}, SeaDexAlternatives: []indexer.SeaDexStanding{alt}}
	wanted, err := e.QualityCutoffNotMet(v, held, &title)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range alt.FeatureNames() {
		mapped, err := ResolveFile(&title, v.Policy.Sonarr, FileInput{Path: name, Release: "[Group] Isekai Story Directors Cut S01 (BD 1080p)", OtherVideoFiles: true, Mapping: &EpisodeMap{How: AnimeMapFranchise, Entry: "al:1"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("import path=%s mapped=%+v", name, mapped)
		for _, id := range mapped.EpisodeIDs {
			if id == title.Episodes[1].ID {
				t.Fatal("fixture unexpectedly maps held E2")
			}
		}
	}
	t.Logf("cutoff wanted held E2=%v", wanted)
	if wanted {
		t.Fatal("cutoff ignored release-title alternate-cut mapping")
	}
}

var _ = format.SeaDexNone

func TestCutoffCacheProfileIdentity(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "tv")
	title := show()
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{1, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("f", 40), Quality: q.Name, Preference: 3, Size: 80 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "The.Show.S01E01.mkv")
	// Same policy pointer, distinct immutable engine/profile with a different size definition.
	loose := seriesEngine(t)
	p2 := loose.Profile(v.Policy.Profile, format.Sonarr)
	maxSize := float64(10)
	p2.Engine.Definitions.Set(profile.Definition{Quality: q, MaxSize: &maxSize})
	strict, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	same, err := loose.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	fresh := title
	fresh.cutoff = nil
	reset, err := loose.QualityCutoffNotMet(v, f, &fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("strict=%v loose with cached title=%v loose fresh title=%v", strict, same, reset)
	if same != reset {
		t.Fatal("profile absent from cutoff cache key")
	}
}

func TestCutoffCacheReloadedTitle(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{Entry: 2, InfoHash: strings.Repeat("a", 40), Quality: q.Name, Preference: 3, Size: 30 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "[Group] Isekai Monogatari - 01 (1080p).mkv", "[Group] Isekai Monogatari - 02 (1080p).mkv")
	first, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	title.AnimeMap = nil
	second, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	title.cutoff = nil
	fresh, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mapped=%v after removing mapping=%v recalculated=%v", first, second, fresh)
	if second != fresh {
		t.Fatal("mapping changed but cutoff cache not invalidated")
	}
}

func TestSuffixedEntry(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	title.AnimeMap.Entries[1].Key = "al:2#1"
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{Entry: 2, InfoHash: strings.Repeat("a", 40), Quality: q.Name, Preference: 3, Size: 30 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "[Group] Isekai Monogatari - 01 (1080p).mkv", "[Group] Isekai Monogatari - 02 (1080p).mkv")
	got, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := ResolveFile(&title, v.Policy.Sonarr, FileInput{Path: f.SeaDexAlternatives[0].FeatureNames()[0], OtherVideoFiles: true, Mapping: &EpisodeMap{How: AnimeMapFranchise, Entry: "al:2#1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("suffixed entry import mapping=%+v wanted S2E1=%v", mapped, got)
	if !got {
		t.Fatal("eligible mapped candidate missing because entry key has a # suffix")
	}
}

func TestConcurrentCutoff(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "tv")
	title := show()
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{1, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("f", 40), Quality: q.Name, Preference: 3, Size: 20 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "The.Show.S01E01.mkv")
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, _ = e.QualityCutoffNotMet(v, f, &title); done <- struct{}{} }()
	}
	close(start)
	<-done
	<-done
}

func TestAmbiguousEntryIsUnknown(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	duplicate := *title.AnimeMap.Entries[1]
	duplicate.Key = "al:2#1"
	title.AnimeMap.Entries = append(title.AnimeMap.Entries, &duplicate)
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{Entry: 2, InfoHash: strings.Repeat("a", 40), Quality: q.Name, Preference: 3, Size: 30 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "[Group] Isekai Monogatari - 01 (1080p).mkv", "[Group] Isekai Monogatari - 02 (1080p).mkv")
	got, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("ambiguous AniList mapping was curator-wanted")
	}
	ver, err := sonarr.ParseVersion(v.Policy.Sonarr)
	if err != nil {
		t.Fatal(err)
	}
	eps, key, mapped, _, err := newSeries(&title, ver).seadexCour("2", 2, title.AnimeMap)
	if err != nil || !mapped || len(eps) != 0 || key != "" {
		t.Fatalf("ambiguous cour: %v %s %v %v", eps, key, mapped, err)
	}
}
