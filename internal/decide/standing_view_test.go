package decide

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// splitCourCtx is a series title with two split-cour entries of season 1: 11 covers E01-E12, 12 covers E13-E24.
// Entry 12 lists hash, entry 11 lists only other.
func splitCourCtx(hash, other string) context.Context {
	cover := map[int]*indexer.SeaDexEntryMapping{11: {Series: true, Episodes: map[[2]int]bool{}}, 12: {Series: true, Episodes: map[[2]int]bool{}}}
	for n := 1; n <= 24; n++ {
		entry := 11
		if n > 12 {
			entry = 12
		}
		cover[entry].Episodes[[2]int{1, n}] = true
	}
	state := indexStanding(seaDexState{configured: true, titles: map[int64][]int{1: {11, 12}}, series: map[int64]bool{1: true},
		cover: map[int64]map[int]*indexer.SeaDexEntryMapping{1: cover}, snapshots: []indexer.SeaDexSnapshot{
			indexer.ConfigureSeaDex(factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: other, IsBest: true, Preference: 2}}}), nil),
			indexer.ConfigureSeaDex(factsSnapshot(indexer.SeaDexSnapshot{AniList: 12, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, IsBest: true, Preference: 2}}}), nil),
		}})
	return WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 1)
}

// A held series file of cour 1 whose hash is listed only by cour 2's entry gains no standing from that hash;
// its stored standing stays (the listing is outside its scope: no evidence either way).
func TestHeldFileGainsNoStandingFromNonCoveringEntry(t *testing.T) {
	t.Parallel()
	hash, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := splitCourCtx(hash, other)
	got := resolveSeaDex(ctx, &ExistingFile{InfoHash: hash, seaDexEpisodes: [][2]int{{1, 1}}})
	if got.SeaDexPreference != 0 || got.SeaDex != format.SeaDexNone || got.SeaDexEntry != 0 {
		t.Fatalf("cour 1 file took cour 2's standing from its hash: %+v", got)
	}
	stored := resolveSeaDex(ctx, &ExistingFile{InfoHash: hash, SeaDexPreference: 1, SeaDex: format.SeaDexAlt, seaDexEpisodes: [][2]int{{1, 1}}})
	if stored.SeaDexPreference != 1 || stored.SeaDex != format.SeaDexAlt {
		t.Fatalf("stored standing of an out-of-scope listing changed: %+v", stored)
	}
	// Covered episodes and unknown episodes behave as before: standing from the covering entry, or the hash alone.
	for name, eps := range map[string][][2]int{"covered": {{1, 13}}, "unknown": nil} {
		if got := resolveSeaDex(ctx, &ExistingFile{InfoHash: hash, seaDexEpisodes: eps}); got.SeaDexPreference != 3 || got.SeaDexEntry != 12 {
			t.Fatalf("%s episodes: %+v", name, got)
		}
	}
}

// The same file still gets the alternatives of the entry covering it, as a hashless file does.
func TestNonCoveringListingKeepsCoveringAlternatives(t *testing.T) {
	t.Parallel()
	hash, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := splitCourCtx(hash, other)
	for name, f := range map[string]*ExistingFile{
		"hash listed elsewhere": {InfoHash: hash, seaDexEpisodes: [][2]int{{1, 1}}},
		"hashless":              {seaDexEpisodes: [][2]int{{1, 1}}},
		"unlisted hash":         {InfoHash: strings.Repeat("c", 40), seaDexEpisodes: [][2]int{{1, 1}}},
	} {
		got := resolveSeaDex(ctx, f)
		if len(got.SeaDexAlternatives) != 1 || got.SeaDexAlternatives[0].InfoHash != other || got.SeaDexAlternatives[0].Entry != 11 {
			t.Fatalf("%s: alternatives %+v, want entry 11's", name, got.SeaDexAlternatives)
		}
	}
}

// A held rank below every current rank of disagreeing mapped entries is kept (disagreement never promotes),
// and the file can still be searched up to the matched entries' curated torrents.
func TestHeldRankBelowAllCurrentRanksKeepsAlternatives(t *testing.T) {
	t.Parallel()
	h, better := strings.Repeat("a", 40), strings.Repeat("b", 40)
	state := indexStanding(seaDexState{configured: true, titles: map[int64][]int{1: {1, 2}},
		cover: map[int64]map[int]*indexer.SeaDexEntryMapping{1: {1: {}, 2: {}}}, snapshots: []indexer.SeaDexSnapshot{
			factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true}}}),
			factsSnapshot(indexer.SeaDexSnapshot{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 1}, {InfoHash: better, Preference: 3, IsBest: true}}}),
		}})
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 1)
	got := resolveSeaDex(ctx, &ExistingFile{InfoHash: h, SeaDexPreference: 0, SeaDex: format.SeaDexNone})
	if got.SeaDexPreference != 0 || got.SeaDex != format.SeaDexNone {
		t.Fatalf("disagreement promoted the held rank: %+v", got)
	}
	found := false
	for _, a := range got.SeaDexAlternatives {
		found = found || a.InfoHash == better && a.Entry == 2
	}
	if !found {
		t.Fatalf("kept rank lost the matched entries' alternatives: %+v", got.SeaDexAlternatives)
	}
}

// A prefer-off version's view is empty and reads nothing: on a database that cannot be read it has no error, while a
// prefer-on version of the same operation reports one.
func TestPreferOffViewReadsNothing(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	db := st.Reader()
	ctx, _ := WithStoredSeaDex(context.Background(), db)
	st.Close()
	off := WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{})
	if err := SeaDexError(off); err != nil {
		t.Fatalf("prefer-off view read the database: %v", err)
	}
	if v := seaDexView(off); !v.off || v.configured || len(v.snapshots) != 0 {
		t.Fatalf("prefer-off view is not empty: %+v", v)
	}
	if err := SeaDexError(WithSeaDexPolicy(off, nil, &policy.VersionPolicy{PreferSeaDex: true})); err == nil {
		t.Fatal("prefer-on view of a closed database has no error")
	}
}

// One operation sees one state of the records however often its views are built and however many goroutines build
// them; a write after the pin is seen only by the next operation.
func TestViewConsistentAcrossReloadsAndGoroutines(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: a, Preference: 1}}}))
	on := &policy.VersionPolicy{PreferSeaDex: true}
	other := &policy.VersionPolicy{PreferSeaDex: true}
	pinned, _ := WithStoredSeaDex(context.Background(), st.Reader())
	first := seaDexView(WithSeaDexPolicy(pinned, nil, on))
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: b, Preference: 1}}}))
	var wg sync.WaitGroup
	views := make([]seaDexState, 16)
	for i := range views {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vp := on
			if i%2 == 1 {
				vp = other
			}
			views[i] = seaDexView(WithSeaDexPolicy(pinned, nil, vp))
		}()
	}
	wg.Wait()
	for i, v := range views {
		if v.err != nil || len(v.snapshots) != 1 || len(v.hashes[b]) != 0 {
			t.Fatalf("view %d of the pinned operation saw a later write: %+v", i, v.snapshots)
		}
		if i%2 == 0 && &v.snapshots[0] != &first.snapshots[0] {
			t.Fatalf("view %d of the same version policy was rebuilt", i)
		}
	}
	next, _ := WithStoredSeaDex(context.Background(), st.Reader())
	if v := seaDexView(WithSeaDexPolicy(next, nil, on)); len(v.snapshots) != 2 || len(v.hashes[b]) != 1 {
		t.Fatalf("a new operation missed the write: %+v", v.snapshots)
	}
}

// Value copies of a loaded title share its cutoff cache, and a cutoff pass writes no field of the title: copying the
// title while other goroutines run cutoff passes is not a race (go test -race).
func TestTitleCopiesDuringCutoffPass(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "tv")
	title := show()
	title.cutoff = &cutoffCache{} // as LoadSeriesVersion leaves it
	alt := indexer.SeaDexStanding{InfoHash: strings.Repeat("f", 40), Quality: q.Name, Preference: 3, Size: 80 * 1024 * 1024}
	setStandingFeatures(t, &alt, "The.Show.S01E01.mkv")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copied := title
			f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{1, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{alt}}
			if _, err := e.QualityCutoffNotMet(v, f, &copied); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(title.cutoff.m) == 0 {
		t.Fatal("value copies did not share the loaded title's cache")
	}
}
