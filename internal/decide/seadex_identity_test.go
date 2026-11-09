package decide

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"strings"
	"testing"
)

func TestIncomingSeaDexIdentityComesFromMagnet(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{{RecordID: "6vdej49pr26gca0", InfoHash: a, Preference: 3}}}})
	r := RefreshSeaDexRelease(ctx, indexer.Release{Title: "Show (6vdej49pr26gca0)", Magnet: indexer.BareMagnet(b), Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}})
	if r.InfoHash != b || indexer.SeaDexPreference(&r) != 0 {
		t.Fatalf("wrong identity or standing: %+v", r)
	}
	r = RefreshSeaDexRelease(ctx, indexer.Release{Title: "Show (6vdej49pr26gca0)"})
	if r.InfoHash != "" {
		t.Fatal("title manufactured a transport hash")
	}
}

func TestConfiguredWinnerIsNotUpstreamBest(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 3}}}})
	r := RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: hash})
	for _, f := range r.Flags {
		if f == indexer.FlagSeaDexBest {
			t.Fatal("configured winner became upstream best")
		}
	}
}

func TestSeaDexAmbiguousMembership(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{
		{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}}},
		{AniList: 2, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 3}}},
	})
	x := resolveSeaDex(ctx, &ExistingFile{InfoHash: hash, SeaDexEntry: 2})
	if x.SeaDexEntry != 2 || x.SeaDexPreference != 3 {
		t.Fatalf("own entry ignored: %+v", x)
	}
	x = resolveSeaDex(ctx, &ExistingFile{InfoHash: hash})
	if x.SeaDexPreference != 0 {
		t.Fatal("ambiguous hash obtained standing")
	}
}

func BenchmarkSeaDexIndexedMiss(b *testing.B) {
	var snapshots []indexer.SeaDexSnapshot
	for i := 0; i < 3000; i++ {
		snapshots = append(snapshots, indexer.SeaDexSnapshot{AniList: i + 1, Torrents: []indexer.SeaDexStanding{{InfoHash: fmt.Sprintf("%040x", i+1), Preference: 3}}})
	}
	ctx := WithSeaDex(context.Background(), snapshots)
	f := &ExistingFile{InfoHash: strings.Repeat("f", 40)}
	b.ResetTimer()
	for b.Loop() {
		_ = resolveSeaDex(ctx, f)
	}
}

func TestCuratedCutoffRequiresHigherAllowedStanding(t *testing.T) {
	e := testEngine(t)
	e.Profile("remux-2160p", format.Radarr).Profile.CutoffFormatScore = 0
	v := Version{Key: "movie-remux"}
	cfg := *e.Policy.Versions[v.Key]
	cfg.PreferSeaDex = true
	v.Policy = &cfg
	for _, tc := range []struct {
		name          string
		current, rank int
		quality       string
		want          bool
	}{
		{"uncovered", 0, 0, "", false}, {"higher", 1, 3, "Remux-2160p", true},
		{"already best available", 2, 2, "Remux-2160p", false}, {"unknown", 0, 3, "Unknown", false},
		{"disallowed", 0, 3, "SDTV", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, SeaDexPreference: tc.current, SeaDexAlternatives: []indexer.SeaDexStanding{{Preference: tc.rank, Quality: tc.quality, Size: 60 * gb}}}
			got, err := e.QualityCutoffNotMet(v, f, &dune)
			if err != nil || got != tc.want {
				t.Fatalf("quality cutoff=%v %v", got, err)
			}
			got, err = e.FileCutoffNotMet(&dune, v, f)
			if err != nil || got != tc.want {
				t.Fatalf("full cutoff=%v %v", got, err)
			}
		})
	}
}

func TestUnknownNativeQualityHeldEvenWhenAllowed(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	p := e.Profile("remux-2160p", format.Radarr)
	q, _ := profile.QualityByName(format.Radarr, "Unknown")
	p.Profile.Items = []profile.Item{{Quality: &q, Allowed: true}}
	p.Profile.MinFormatScore = 0
	v := Version{Key: "movie-remux"}
	vp := *e.Policy.Versions[v.Key]
	vp.PreferSeaDex = true
	v.Policy = &vp
	r := rel("Dune.Part.Two.2024.Unknown", "public", 0)
	r.Kind = "seadex"
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	d, err := e.Decide(context.Background(), r, dune, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending := false
	for _, rj := range d.Rejections {
		pending = pending || rj.Reason == "SeaDexQualityPending"
	}
	if d.Accepted || !pending {
		t.Fatalf("unknown native quality accepted: %+v", d)
	}
}

func TestUnknownRuntimeDoesNotHideDiskRejection(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	title := dune
	title.Runtime = 0
	r := rel("Dune.Part.Two.2024.2160p.BluRay.REMUX-GRP", "public", 60*gb)
	f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, Size: 60 * gb}
	e.Policy.Versions["movie-remux"].PreferSeaDex = true
	d, err := e.Decide(context.Background(), r, title, Version{Key: "movie-remux"}, f)
	if err != nil {
		t.Fatal(err)
	}
	runtime, disk := false, false
	for _, rj := range d.Rejections {
		runtime = runtime || rj.Reason == "UnknownRuntime"
		disk = disk || strings.HasPrefix(rj.Reason, "Disk")
	}
	if !runtime || !disk {
		t.Fatalf("runtime hold hid disk decision: %+v", d.Rejections)
	}
}

func TestStoredStandingPinnedAndCorruptRowIsolated(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	hash := strings.Repeat("a", 40)
	save := func(rank int) {
		t.Helper()
		body, _ := json.Marshal(factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: rank}}}))
		if _, err := st.Writer().Exec(`INSERT OR REPLACE INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,0,253402214400000)`, body); err != nil {
			t.Fatal(err)
		}
	}
	save(3)
	if _, err := st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','bad',200,'{',0,253402214400000)`); err != nil {
		t.Fatal(err)
	}
	on := &policy.VersionPolicy{PreferSeaDex: true}
	// The pin reads once, when a prefer-on version first needs it, and the whole operation keeps that read.
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if err := SeaDexError(WithSeaDexPolicy(ctx, nil, on)); err != nil {
		t.Fatalf("corrupt row broke the load: %v", err)
	}
	save(0)
	old := resolveSeaDex(WithSeaDexPolicy(ctx, nil, on), &ExistingFile{InfoHash: hash, SeaDexPreference: 3})
	if old.SeaDexPreference == 0 {
		t.Fatal("operation mixed snapshots")
	}
	fresh, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	// A cached fresh answer does not override the operation's stored standing.
	fresh = WithSeaDex(fresh, []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 3}}})})
	retired := resolveSeaDex(WithSeaDexPolicy(fresh, nil, on), &ExistingFile{InfoHash: hash, SeaDexPreference: 3})
	if retired.SeaDexPreference != 0 {
		t.Fatal("legacy entryless file retained standing or cached answer replaced newer standing")
	}
	// A prefer-off version never takes standing, and an unconfigured load never rewrites held files.
	off := resolveSeaDex(WithSeaDexPolicy(fresh, nil, &policy.VersionPolicy{}), &ExistingFile{InfoHash: hash, SeaDexPreference: 3})
	held := &ExistingFile{InfoHash: hash, SeaDexPreference: 3}
	if err := ResolveSeaDexFiles(fresh, st.Reader(), []*ExistingFile{held}); err != nil || off.SeaDexPreference != 3 || held.SeaDexPreference != 3 {
		t.Fatalf("prefer-off or unconfigured load rewrote standing: %+v %+v %v", off, held, err)
	}
}

func TestEntrylessLegacyFileRetiresAgainstMappedEmptyEntry(t *testing.T) {
	t.Parallel()
	state := indexStanding(seaDexState{snapshots: []indexer.SeaDexSnapshot{{AniList: 1535}}, titles: map[int64][]int{1: {1535}}, stored: true})
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, state), 1)
	got := resolveSeaDex(ctx, &ExistingFile{InfoHash: strings.Repeat("a", 40), SeaDexPreference: 3, SeaDex: format.SeaDexBest})
	if got.SeaDexPreference != 0 || got.SeaDex != format.SeaDexNone {
		t.Fatalf("mapped legacy file kept retired standing: %+v", got)
	}
}

func TestPreferredAlternativeDoesNotGainBestFormatScore(t *testing.T) {
	t.Parallel()
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
	hash := strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 3}}}})
	r := rel("Dune.Part.Two.2024.2160p.BluRay.REMUX-GRP", "public", 60*gb)
	r.InfoHash = hash
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	for _, prefer := range []bool{false, true} {
		vp := *e.Policy.Versions["movie-remux"]
		vp.PreferSeaDex = prefer
		d, err := e.Decide(ctx, r, dune, Version{Key: "movie-remux", Policy: &vp}, nil)
		if err != nil || d.Score != 0 {
			t.Fatalf("prefer=%v score changed: %+v %v", prefer, d, err)
		}
	}
}
