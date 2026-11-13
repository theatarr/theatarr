package decide

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
)

func reviewCutoffEngine(t *testing.T, key string) (*Engine, Version, profile.Quality) {
	t.Helper()
	e := seriesEngine(t)
	v := tvVersion(e, key)
	vp := *v.Policy
	vp.PreferSeaDex = true
	v.Policy = &vp
	p := e.Profile(vp.Profile, format.Sonarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	quality := "Bluray-2160p Remux"
	if key == "anime-tv" {
		quality = "Bluray-1080p"
	}
	q := profile.MustQuality(format.Sonarr, quality)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: q, MaxSize: &maxSize})
	return e, v, q
}

func TestCandidateMustContainHeldEpisode(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "tv")
	title := show()
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{1, 3}}, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Quality: q.Name, Preference: 3, Size: 80 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "The.Show.S01E01.mkv", "The.Show.S01E02.mkv")
	resolveFileAlternatives(f, &title)
	if len(f.SeaDexAlternatives) != 1 {
		t.Fatalf("fixture alternatives=%d", len(f.SeaDexAlternatives))
	}
	got, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("E03 marked curator-wanted by a torrent containing only E01 and E02")
	}
}

func TestUnresolvedHeldDisagreementKeepsProtection(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("c", 40)
	state := indexStanding(seaDexState{
		snapshots: []indexer.SeaDexSnapshot{
			indexer.ConfigureSeaDex(factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 2, IsBest: true}}}), nil),
			indexer.ConfigureSeaDex(factsSnapshot(indexer.SeaDexSnapshot{AniList: 12, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}, {InfoHash: strings.Repeat("e", 40), Preference: 2, IsBest: true}}}), nil),
		},
		configured: true,
		titles:     map[int64][]int{1: {11, 12}},
		series:     map[int64]bool{1: true},
		cover: map[int64]map[int]*indexer.SeaDexEntryMapping{1: {
			11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
			12: {Series: true, Episodes: map[[2]int]bool{{1, 2}: true}},
		}},
	})
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 1)
	held := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, InfoHash: hash, SeaDexPreference: 3, SeaDex: format.SeaDexBest, seaDexEpisodes: [][2]int{{1, 13}}}
	got := resolveSeaDex(ctx, held)
	e, v, _ := reviewCutoffEngine(t, "tv")
	title := show()
	incoming := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, InfoHash: strings.Repeat("d", 40)}
	oldAllowed, oldWhy, oldErr := e.ImportUpgrade(&title, v, incoming, held)
	newAllowed, newWhy, newErr := e.ImportUpgrade(&title, v, incoming, &got)
	if oldErr != nil || newErr != nil {
		t.Fatalf("%v %v", oldErr, newErr)
	}
	if got.SeaDexPreference != 3 || got.SeaDex != format.SeaDexBest {
		t.Fatalf("no matching entry covers held E13; rank 3 became %d; ordinary higher-quality import: before allowed=%v (%s), after allowed=%v (%s)", got.SeaDexPreference, oldAllowed, oldWhy, newAllowed, newWhy)
	}
}

func TestPackParsedOnce(t *testing.T) {
	e, v, q := reviewCutoffEngine(t, "tv")
	title := show()
	title.Episodes = nil
	var names []string
	for i := 1; i <= 200; i++ {
		title.Episodes = append(title.Episodes, Episode{ID: int64(i), Season: 1, Number: i, Runtime: 45})
		names = append(names, fmt.Sprintf("The.Show.S01E%02d.mkv", i))
	}
	f := &ExistingFile{Quality: q.Name, Revision: 1, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Quality: q.Name, Preference: 3, Size: 200 * 10000000}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], names...)
	calls := 0
	resolve := func(ti *Title, in FileInput) (FileEpisodes, error) {
		calls++
		return ResolveFile(ti, in)
	}
	p := e.Profile(v.Policy.Profile, format.Sonarr)
	start := time.Now()
	for i := 1; i <= 200; i++ {
		candidate := cutoffAlternative(&title, v.Policy, p, f.SeaDexAlternatives[0], q, resolve)
		if !candidate.eligible {
			t.Fatal("candidate not eligible")
		}
		f.seaDexEpisodes = [][2]int{{1, i}}
		if wanted, err := e.QualityCutoffNotMet(v, f, &title); err != nil || !wanted {
			t.Fatalf("wanted=%v err=%v", wanted, err)
		}
	}
	if calls != 200 {
		t.Fatalf("parsed %d features; want 200", calls)
	}
	t.Logf("200x200 cutoff pass: %v", time.Since(start))
}
func TestCandidateEntryMapsLocalNumbers(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	for i := range title.Episodes {
		if title.Episodes[i].Season == 1 {
			title.Episodes[i].Runtime = 60
		} else {
			title.Episodes[i].Runtime = 20
		}
	}
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{Entry: 2, InfoHash: strings.Repeat("b", 40), Quality: q.Name, Preference: 3, Size: 80 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "[Group] Isekai Monogatari - 01 (1080p).mkv", "[Group] Isekai Monogatari - 02 (1080p).mkv")
	if wanted, err := e.QualityCutoffNotMet(v, f, &title); err != nil || wanted {
		t.Fatalf("80 MiB exceeds S2 episodes' 40 MiB limit: wanted=%v err=%v", wanted, err)
	}
}

func TestCandidateEntryEligibleLocalNumbers(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	title := animeMapped("auto")
	f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{Entry: 2, InfoHash: strings.Repeat("b", 40), Quality: q.Name, Preference: 3, Size: 30 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "[Group] Isekai Monogatari - 01 (1080p).mkv", "[Group] Isekai Monogatari - 02 (1080p).mkv")
	if wanted, err := e.QualityCutoffNotMet(v, f, &title); err != nil || !wanted {
		t.Fatalf("eligible local-numbered S2 candidate: wanted=%v err=%v", wanted, err)
	}
}
