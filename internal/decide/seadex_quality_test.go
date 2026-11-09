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

func TestSeaDexPreferenceRespectsActualQuality(t *testing.T) {
	t.Parallel()
	pol, err := policy.Load("testdata/series")
	if err != nil {
		t.Fatal(err)
	}
	pol.Dir = "../../examples/policy"
	pol.Formats = map[string]*policy.FormatRef{"Seadex": {File: "formats/seadex.json"}}
	for _, p := range pol.Profiles {
		p.Formats = nil
	}
	p := pol.Profiles["anime"]
	p.Qualities = []policy.QualityItem{{Name: "Bluray-1080p"}, {Name: "Bluray-720p"}}
	p.Formats = map[string]int{"Seadex": 5000}
	p.CutoffScore = 10000
	e, err := New(pol, Options{})
	if err != nil {
		t.Fatal(err)
	}
	title := show()
	v := tvVersion(e, "anime-tv")
	r := rel("The Show S01 Bluray 720p (arid1)", "public", 9*gb)
	r.Kind = "seadex"
	r.Kind = "seadex"
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest, indexer.FlagSeaDexPreferred}
	r.InfoHash = strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: r.InfoHash, Quality: "Bluray-720p", IsBest: true, Preference: 3}}})})
	d, err := e.Decide(ctx, r, title, v, nil)
	if err != nil || !d.Accepted || d.Quality != "Bluray-720p" || d.Score != 5000 {
		t.Fatalf("missing copy can use a genuinely allowed preferred 720p release: %+v %v", d, err)
	}
	plain := rel("The.Show.S01.720p.BluRay-OTHER", "public", 9*gb)
	other, err := e.Decide(ctx, plain, title, v, nil)
	if err != nil || !other.Accepted || Rank([]*Decision{other, d})[0] != d {
		t.Fatalf("SeaDex preference should win within the same quality: %+v %v", other, err)
	}
	for i := range title.Episodes {
		title.Episodes[i].FileID = title.Episodes[i].ID
		title.Episodes[i].File = &ExistingFile{Quality: "Bluray-1080p", Revision: 1,
			SceneName: "The.Show.1080p.BluRay-OTHER", Size: 3 * gb}
	}
	d, err = e.Decide(ctx, r, title, v, nil)
	if err != nil || d.Accepted || d.Upgrade || d.Quality != "Bluray-720p" || d.Score != 5000 {
		t.Fatalf("curator score must not disguise a downgrade from 1080p: %+v %v", d, err)
	}
	// An explicit user preference can choose that same curated encode, with its real quality retained.
	config := *v.Policy
	config.PreferSeaDex = true
	v.Policy = &config
	d, err = e.Decide(ctx, r, title, v, nil)
	if err != nil || !d.Accepted || !d.Upgrade || d.Quality != "Bluray-720p" || !strings.Contains(d.UpgradeReason, "SeaDex") {
		t.Fatalf("explicit curator preference: %+v %v", d, err)
	}
	missing := show()
	preferred, _ := e.Decide(ctx, r, missing, v, nil)
	high := plain
	high.Title = "The.Show.S01.1080p.BluRay-OTHER"
	higherResolution, err := e.Decide(ctx, high, missing, v, nil)
	if err != nil || !higherResolution.Accepted || Rank([]*Decision{higherResolution, preferred})[0] != preferred {
		t.Fatalf("curated release should rank first across resolutions: %+v %v", higherResolution, err)
	}
	for i := range title.Episodes {
		title.Episodes[i].File.Quality = "Bluray-720p"
		title.Episodes[i].File.SeaDexPreference = 3
	}
	plain.Title = "The.Show.S01.1080p.BluRay-OTHER"
	for _, legacy := range []bool{false, true} {
		if legacy {
			for i := range title.Episodes {
				title.Episodes[i].File.SeaDexPreference = 0
				title.Episodes[i].File.IndexerFlags = 92 // imported seadexerr flags on the arr's files
			}
		}
		other, err = e.Decide(ctx, plain, title, v, nil)
		if err != nil || other.Accepted != legacy {
			t.Fatalf("only verified standing protects a curated file (unverified flags=%v): %+v %v", legacy, other, err)
		}
	}
	cur := &ExistingFile{Quality: "Bluray-1080p", Revision: 1, Size: 3 * gb}
	nw := &ExistingFile{Quality: "Bluray-720p", Revision: 1, Size: 3 * gb, SeaDexPreference: 3}
	if ok, why, err := e.ImportUpgrade(&title, v, nw, cur); err != nil || !ok || !strings.Contains(why, "SeaDex") {
		t.Fatalf("import must honor the same preference: %v %s %v", ok, why, err)
	}
	if ok, _, err := e.ImportUpgrade(&title, v, cur, nw); err != nil || ok {
		t.Fatalf("import must preserve a preferred existing file: %v %v", ok, err)
	}
	config.Upgrades = policy.UpgradesOff
	if ok, _, err := e.ImportUpgrade(&title, v, nw, cur); err != nil || ok {
		t.Fatalf("preference must respect upgrades off: %v %v", ok, err)
	}
}

func TestPreferSeaDexUsesRealQualityAndRetainsGuards(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	q, _ := profile.QualityByName(format.Radarr, "Bluray-720p")
	minimum := 1.0
	p := e.Profile("remux-2160p", format.Radarr)
	for i := range p.Profile.Items {
		if p.Profile.Items[i].Quality != nil && p.Profile.Items[i].Quality.ID == q.ID {
			p.Profile.Items[i].Allowed = true
		}
	}
	e.Profile("remux-2160p", format.Radarr).Engine.Definitions.Set(profile.Definition{Quality: q, MinSize: &minimum})
	v := Version{Key: "movie-remux"}
	config := *e.Policy.Versions[v.Key]
	config.PreferSeaDex = true
	v.Policy = &config
	r := rel("Dune.Part.Two.2024.720p.BluRay-GRP", "public", 9*gb)
	r.Kind = "seadex"
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest, indexer.FlagSeaDexPreferred}
	r.InfoHash = strings.Repeat("a", 40)
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: r.InfoHash, Quality: "Bluray-720p", IsBest: true, Preference: 3}}})})
	cur := &ExistingFile{Quality: "Remux-2160p", Revision: 1, Size: 60 * gb}
	d, err := e.Decide(ctx, r, dune, v, cur)
	if err != nil || !d.Accepted || !d.Upgrade || d.Quality != "Bluray-720p" || !strings.Contains(d.UpgradeReason, "SeaDex") {
		t.Fatalf("movie preference and truthful quality: %+v %v", d, err)
	}
	// The same shared profile remains unchanged for a version without the preference.
	d, err = e.Decide(ctx, r, dune, Version{Key: v.Key}, cur)
	if err != nil || d.Accepted {
		t.Fatalf("preference leaked to a shared profile: %+v %v", d, err)
	}
	for _, c := range []struct {
		name string
		edit func(*Release)
	}{
		{"unknown", func(r *Release) { r.Title = "Dune.Part.Two.2024.Unknown" }},
		{"size", func(r *Release) { r.Size = 1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			bad := r
			c.edit(&bad)
			if d, err := e.Decide(ctx, bad, dune, v, nil); err != nil || d.Accepted {
				t.Fatalf("curator preference bypassed %s: %+v %v", c.name, d, err)
			} else if c.name == "unknown" && (len(d.Rejections) == 0 || d.Rejections[0].Reason != "SeaDexQualityPending" || !d.Rejections[0].Temporary) {
				t.Fatalf("unidentified quality must remain eligible after metadata arrives: %+v", d.Rejections)
			}
		})
	}
	e.Profile("remux-2160p", format.Radarr).Profile.UpgradeAllowed = false
	if d, err := e.Decide(ctx, r, dune, v, cur); err != nil || d.Accepted {
		t.Fatalf("curator preference bypassed disabled profile upgrades: %+v %v", d, err)
	}
}
