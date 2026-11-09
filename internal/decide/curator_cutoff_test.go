package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestMatchedHashOutsideCoverage(t *testing.T) {
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
		{InfoHash: hash, Quality: "Remux-2160p", Preference: 1},
		{InfoHash: strings.Repeat("b", 40), Quality: "Remux-2160p", IsBest: true, Preference: 2},
	}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: hash, seaDexEpisodes: [][2]int{{1, 13}}}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
		t.Fatal(err)
	}
	if len(f.SeaDexAlternatives) > 0 {
		t.Fatalf("E13 received entry %d alternatives for E1-E2: %+v", f.SeaDexEntry, f.SeaDexAlternatives)
	}
	f.seaDexEpisodes = nil
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f}); err != nil {
		t.Fatal(err)
	}
	if len(f.SeaDexAlternatives) > 0 {
		t.Fatalf("unknown episodes received alternatives from a matching hash: %+v", f)
	}
}

func TestCuratorCutoffIgnoresSize(t *testing.T) {
	e := testEngine(t)
	p := e.Profile("remux-2160p", format.Radarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Radarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: profile.MustQuality(format.Radarr, "Remux-2160p"), MaxSize: &maxSize})
	vp := *e.Policy.Versions["movie-remux"]
	vp.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &vp}
	f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, SeaDexAlternatives: []indexer.SeaDexStanding{
		{InfoHash: strings.Repeat("a", 40), Quality: "Remux-2160p", Preference: 3, Size: 60 * gb},
	}}
	r := rel("Dune.Part.Two.2024.2160p.BluRay.REMUX-GRP", "public", 60*gb)
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	d, err := e.Decide(context.Background(), r, dune, v, f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reasons(d), "AboveMaximumSize") {
		t.Fatalf("control did not reject size: %v", d.Rejections)
	}
	wanted, err := e.FileCutoffNotMet(&dune, v, f)
	if err != nil {
		t.Fatal(err)
	}
	if wanted {
		t.Fatalf("curator-only wanted despite sole alternative's AboveMaximumSize rejection: %v", d.Rejections)
	}
}

func TestCuratorSizeReadiness(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	p := e.Profile("remux-2160p", format.Radarr)
	p.Profile.CutoffFormatScore = 0
	vp := *e.Policy.Versions["movie-remux"]
	vp.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &vp}
	for _, tc := range []struct {
		name    string
		runtime int
		size    int64
		want    bool
	}{
		{"eligible", 166, 60 * gb, true}, {"unknown runtime", 0, 60 * gb, false}, {"unknown size", 166, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title := dune
			title.Runtime = tc.runtime
			f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, SeaDexAlternatives: []indexer.SeaDexStanding{{Quality: "Remux-2160p", Preference: 3, Size: tc.size}}}
			full, err := e.FileCutoffNotMet(&title, v, f)
			if err != nil || full != tc.want {
				t.Fatalf("full=%v %v", full, err)
			}
			quality, err := e.QualityCutoffNotMet(v, f, &title)
			if err != nil || quality != tc.want {
				t.Fatalf("quality=%v %v", quality, err)
			}
		})
	}
}

func TestSeriesAlternativeSizeUsesEntryEpisodes(t *testing.T) {
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
	f := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, seaDexEpisodes: [][2]int{{1, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{{InfoHash: hash, Quality: "Bluray-2160p Remux", Preference: 3, Size: 100 * 1024 * 1024}}}
	setStandingFeatures(t, &f.SeaDexAlternatives[0], "The.Show.S01E01.mkv", "The.Show.S01E02.mkv", "The.Show.S01E03.mkv")
	// S01 has three 45-minute episodes: a 100 MiB pack fits, despite exceeding one episode's 45 MiB limit.
	resolveFileAlternatives(f, &title)
	wanted, err := e.QualityCutoffNotMet(v, f, &title)
	if err != nil || !wanted {
		t.Fatalf("eligible pack: %v %v", wanted, err)
	}
	f.SeaDexAlternatives[0].Size = 200 * 1024 * 1024
	wanted, err = e.QualityCutoffNotMet(v, f, &title)
	if err != nil || wanted {
		t.Fatalf("oversized pack: %v %v", wanted, err)
	}
}
