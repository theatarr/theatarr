package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// A saved (or explicit) entry not covering the file's verified episodes supplies no standing; the
// stored rank stays and the covering entry's alternatives are attached, held and incoming alike.
func TestHeldSavedEntryNeedsCoverage(t *testing.T) {
	t.Parallel()
	hash, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := splitCourCtx(hash, other)
	title := &Title{Episodes: []Episode{{ID: 1, Season: 1, Number: 1}}}
	for _, tc := range []struct {
		name string
		file ExistingFile
	}{
		{"no saved entry", ExistingFile{InfoHash: hash, SeaDexPreference: 1, SeaDex: format.SeaDexAlt}},
		{"saved non-covering entry", ExistingFile{InfoHash: hash, SeaDexEntry: 12, SeaDexPreference: 1, SeaDex: format.SeaDexAlt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.file
			ResolveSeaDexFileEpisodes(ctx, title, &f, []int64{1})
			if f.SeaDexPreference != 1 || f.SeaDex != format.SeaDexAlt || len(f.SeaDexAlternatives) != 1 || f.SeaDexAlternatives[0].InfoHash != other || f.SeaDexAlternatives[0].Entry != 11 {
				t.Fatalf("held file: rank=%d label=%v entry=%d alternatives=%+v; want saved rank 1 and entry 11's alternative", f.SeaDexPreference, f.SeaDex, f.SeaDexEntry, f.SeaDexAlternatives)
			}
		})
	}
	f := ExistingFile{InfoHash: hash, SeaDexEntry: 12}
	ResolveIncomingSeaDexFile(ctx, title, &f, []int64{1})
	if f.SeaDexPreference != 0 || len(f.SeaDexAlternatives) != 1 || f.SeaDexAlternatives[0].InfoHash != other {
		t.Fatalf("incoming file: rank=%d alternatives=%+v", f.SeaDexPreference, f.SeaDexAlternatives)
	}
}

// A series without any mapping takes no standing by a hash another title's entry lists.
func TestUnmappedSeriesCannotBorrowStanding(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	hash := strings.Repeat("a", 40)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES (1,'series','Mapped',0,0),(2,'series','Unmapped',0,0)`)
	seadexExec(t, st, `INSERT INTO title_external_id(title_id,source,external_id) VALUES (1,'anilist','11')`)
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, IsBest: true, Preference: 2}}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 2)
	if err := SeaDexError(ctx); err != nil {
		t.Fatal(err)
	}
	title := &Title{ID: 2, Kind: policy.KindSeries, Episodes: []Episode{{ID: 1, Season: 1, Number: 1}}}
	t.Run("incoming", func(t *testing.T) {
		f := ExistingFile{InfoHash: hash}
		ResolveIncomingSeaDexFile(ctx, title, &f, []int64{1})
		if f.SeaDexPreference != 0 || f.SeaDexEntry != 0 {
			t.Fatalf("unmapped series acquired another title's standing: rank=%d entry=%d", f.SeaDexPreference, f.SeaDexEntry)
		}
	})
	t.Run("held", func(t *testing.T) {
		f := ExistingFile{InfoHash: hash}
		ResolveSeaDexFileEpisodes(ctx, title, &f, []int64{1})
		if f.SeaDexPreference != 0 || f.SeaDexEntry != 0 {
			t.Fatalf("unmapped held series acquired another title's standing: rank=%d entry=%d", f.SeaDexPreference, f.SeaDexEntry)
		}
	})
}

// A held rank kept because a mapped entry has no record names no entry, so resolving the file
// again keeps it.
func TestKeptRankSurvivesSecondResolution(t *testing.T) {
	t.Parallel()
	hash, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	cover := map[int]*indexer.SeaDexEntryMapping{
		11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
		12: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
	}
	state := indexStanding(seaDexState{configured: true, titles: map[int64][]int{1: {11, 12}}, series: map[int64]bool{1: true},
		cover: map[int64]map[int]*indexer.SeaDexEntryMapping{1: cover}, snapshots: []indexer.SeaDexSnapshot{
			factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{
				{InfoHash: hash, Preference: 1}, {InfoHash: other, Preference: 3, IsBest: true},
			}}),
		}})
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 1)
	f := ExistingFile{InfoHash: hash, SeaDexPreference: 3, SeaDex: format.SeaDexBest, seaDexEpisodes: [][2]int{{1, 1}}}
	first := resolveSeaDex(ctx, &f)
	second := resolveSeaDex(ctx, &first)
	t.Logf("saved rank/entry=%d/%d; first=%d/%d; second=%d/%d", f.SeaDexPreference, f.SeaDexEntry, first.SeaDexPreference, first.SeaDexEntry, second.SeaDexPreference, second.SeaDexEntry)
	if first.SeaDexPreference != 3 || second.SeaDexPreference != 3 {
		t.Fatalf("unobserved entry no longer protects held rank: first=%+v second=%+v", first, second)
	}
}

// TestHeldSavedEntryNeedsCoverage through resolveSeaDex directly.
func TestHeldOwnEntryCoverageDirect(t *testing.T) {
	t.Parallel()
	hash, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := splitCourCtx(hash, other)
	f := &ExistingFile{InfoHash: hash, SeaDexEntry: 12, SeaDexPreference: 1, SeaDex: format.SeaDexAlt, seaDexEpisodes: [][2]int{{1, 1}}}
	got := resolveSeaDex(ctx, f)
	if got.SeaDexPreference != 1 {
		t.Errorf("non-covering saved entry promoted held rank: got %d, want protected rank 1", got.SeaDexPreference)
	}
	if len(got.SeaDexAlternatives) != 1 || got.SeaDexAlternatives[0].Entry != 11 {
		t.Errorf("missing covering alternatives: %+v", got.SeaDexAlternatives)
	}
}
