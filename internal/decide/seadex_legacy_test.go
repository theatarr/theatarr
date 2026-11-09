package decide

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// factsSnapshot is s as recorded with upstream facts (not a legacy row): each listed torrent carries facts whose
// best flag is its own.
func factsSnapshot(s indexer.SeaDexSnapshot) indexer.SeaDexSnapshot {
	s.Torrents = append([]indexer.SeaDexStanding(nil), s.Torrents...)
	for i := range s.Torrents {
		facts := `{"facts":{}}`
		if s.Torrents[i].IsBest {
			facts = `{"facts":{"isBest":true}}`
		}
		_ = json.Unmarshal([]byte(facts), &s.Torrents[i])
	}
	return s
}

// The legacy restriction applies to the memberships of the release's own entry, not to every entry listing the
// hash: entry B's facts do not let entry A's legacy rank curate a release of entry A.
func TestLegacyCheckUsesResolvedEntry(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	var s []indexer.SeaDexSnapshot
	if err := json.Unmarshal([]byte(`[{"anilist":1,"torrents":[{"info_hash":"`+h+`","preference":2}]},{"anilist":2,"torrents":[{"info_hash":"`+h+`","preference":2,"is_best":true,"facts":{"isBest":true}}]}]`), &s); err != nil {
		t.Fatal(err)
	}
	ctx := WithSeaDexPolicy(WithSeaDex(context.Background(), s), nil, &policy.VersionPolicy{PreferSeaDex: true})
	got := RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: h, Extras: map[string]string{"anilist": "1"}, Flags: []string{indexer.FlagSeaDex}})
	if indexer.SeaDexPreference(&got) != 0 {
		t.Fatalf("legacy entry gained incoming rank via an unrelated entry with facts: %+v", got)
	}
}

// A legacy (facts-less) torrent is no curated alternative: an incoming release of it takes no standing, so a
// held file is not searched for it. It does not protect the held file either.
func TestLegacyAlternativeNotSearched(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	st := linkedDB(t)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','X',1,0,0)`)
	seadexSnapshotRow(t, st, indexer.SeaDexSnapshot{AniList: 1, Targets: []indexer.AniListTarget{{TMDb: 1}}, Torrents: []indexer.SeaDexStanding{
		{InfoHash: a, Preference: 1, Quality: "Remux-2160p"}, {InfoHash: b, IsBest: true, Preference: 2, Quality: "Remux-2160p"}}})
	e := testEngine(t)
	e.Profile("remux-2160p", format.Radarr).Profile.CutoffFormatScore = 0
	cfg := *e.Policy.Versions["movie-remux"]
	cfg.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &cfg}
	ctx, _ := WithStoredSeaDex(context.Background(), st.Reader())
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, e.Policy, &cfg), 1)
	f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: a}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
		t.Fatal(err)
	}
	if f.SeaDexPreference != 0 {
		t.Fatalf("legacy standing still protects the held file: %+v", f)
	}
	notMet, err := e.QualityCutoffNotMet(v, f)
	if err != nil || notMet {
		t.Fatalf("held file searched for a legacy alternative no release can match: %v %v (alternatives %+v)", notMet, err, f.SeaDexAlternatives)
	}
	r := RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: b, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest}})
	if indexer.SeaDexPreference(&r) != 0 {
		t.Fatalf("legacy membership curated an incoming release: %+v", r)
	}
}

// CLI (decide --search): a fresh empty answer laid over stored standing starts the retirement, as recording it would;
// a second omission at least SeaDexRetirementDelay after the first retires it.
func TestCLIOverlayConfirmsRetirement(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	h := strings.Repeat("a", 40)
	on := &policy.VersionPolicy{PreferSeaDex: true}
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true}}}))
	ctx := WithSeaDexPolicy(WithSeaDexOverlay(context.Background(), st.Reader(), []indexer.SeaDexSnapshot{{AniList: 1}}), nil, on)
	if got := resolveSeaDex(ctx, &ExistingFile{InfoHash: h, SeaDexEntry: 1, SeaDexPreference: 2}); got.SeaDexPreference == 0 {
		t.Fatalf("one fresh empty CLI answer retired held standing: %+v", got)
	}
	pending := factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true,
		MissingSince: time.Now().Add(-indexer.SeaDexRetirementDelay - time.Hour).UnixMilli()}}})
	seadexSnapshotRow(t, st, pending)
	ctx = WithSeaDexPolicy(WithSeaDexOverlay(context.Background(), st.Reader(), []indexer.SeaDexSnapshot{{AniList: 1}}), nil, on)
	if got := resolveSeaDex(ctx, &ExistingFile{InfoHash: h, SeaDexEntry: 1, SeaDexPreference: 2}); got.SeaDexPreference != 0 {
		t.Fatalf("a confirmed omission did not retire standing in the CLI overlay: %+v", got)
	}
}

// L: two entries mapped to the title rank one hash differently and neither is the file's own: its standing is
// unresolved, not the higher rank.
func TestConflictingMappedEntriesUnresolved(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{
		{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 1}}},
		{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 3, IsBest: true}}},
	}, map[int64][]int{1: {1, 2}}, 1)
	got := resolveSeaDex(ctx, &ExistingFile{InfoHash: h})
	if got.SeaDexPreference != 0 || got.SeaDex != format.SeaDexNone {
		t.Fatalf("conflicting ranks across mapped entries took the highest: %+v", got)
	}
	agree := configuredState([]indexer.SeaDexSnapshot{
		{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true}}},
		{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true}}},
	}, map[int64][]int{1: {1, 2}}, 1)
	if got := resolveSeaDex(agree, &ExistingFile{InfoHash: h}); got.SeaDexPreference != 2 {
		t.Fatalf("agreeing entries lost their standing: %+v", got)
	}
}

// The lazily loaded pin is shared by the operation: a caller that gave up does not fail every later decision.
func TestLazyPinIgnoresCallerCancel(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	ctx, _ := WithStoredSeaDex(context.Background(), st.Reader())
	on := &policy.VersionPolicy{PreferSeaDex: true}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	_ = SeaDexError(WithSeaDexPolicy(dead, nil, on))
	if err := SeaDexError(WithSeaDexPolicy(ctx, nil, on)); err != nil {
		t.Fatalf("a live caller inherits the cancelled first caller's error: %v", err)
	}
}

// A release whose reported hash and magnet name different torrents takes no standing from either.
func TestConflictingIdentityTakesNoStanding(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: b, Preference: 3, IsBest: true}}}},
		map[int64][]int{1: {1}}, 1)
	for _, rel := range []indexer.Release{
		{InfoHash: a, Magnet: indexer.BareMagnet(b), Flags: []string{indexer.FlagSeaDex}},
		{InfoHash: b, Magnet: indexer.BareMagnet(a), Flags: []string{indexer.FlagSeaDex}},
	} {
		got := RefreshSeaDexRelease(ctx, rel)
		if indexer.SeaDexPreference(&got) != 0 {
			t.Fatalf("conflicting release took standing: %+v", got)
		}
	}
	got := RefreshSeaDexRelease(ctx, indexer.Release{Magnet: indexer.BareMagnet(b)})
	if got.InfoHash != b || indexer.SeaDexPreference(&got) != 3 {
		t.Fatalf("hashless release not identified by its magnet: %+v", got)
	}
}
