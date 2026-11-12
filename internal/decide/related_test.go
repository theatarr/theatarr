package decide

import (
	"context"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

var arrival = Title{ID: 8, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164", TMDb: 329865,
	Aliases: []string{"Story of Your Life"}, Runtime: 116, OriginalLanguage: "en"}

type relatedCase struct {
	name string
	rel  indexer.Release
	keep bool
}

func relatedCases() (film, tv []relatedCase) {
	r := func(title string) indexer.Release {
		return indexer.Release{Indexer: "public", Kind: "torznab", Title: title, Size: 8 * gb, Seeders: 10,
			Privacy: policy.PrivacyPublic}
	}
	withIMDb, withTVDB := r("Ankomst.2016.2160p.WEB-DL-GRP"), r("Fremde.Serie.S01E02.2160p.WEB-DL-GRP")
	withIMDb.IMDb, withTVDB.TVDB = "tt2543164", 1001
	film = []relatedCase{
		{"own name", r("Arrival.2016.2160p.WEB-DL.DDP5.1.H.265-GRP"), true},
		{"alias", r("Story.of.Your.Life.2016.2160p.WEB-DL.DDP5.1.H.265-GRP"), true},
		{"another title", r("Unrelated.Film.2019.2160p.WEB-DL-GRP"), false},
		{"foreign name with the IMDb id", withIMDb, true},
		{"unparsable", r("2160p"), false},
	}
	tv = []relatedCase{
		{"own name", r("The.Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP"), true},
		{"scene name", r("Show.Alt.Name.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP"), true},
		{"another title", r("Another.Show.S01E02.2160p.WEB-DL-GRP"), false},
		{"foreign name with the TVDB id", withTVDB, true},
		{"unparsable", r("2160p"), false},
	}
	return film, tv
}

func TestRelated(t *testing.T) {
	t.Parallel()
	ctx := WithParseMemo(context.Background())
	film, tv := relatedCases()
	series := show()
	for _, c := range film {
		if got := Related(ctx, c.rel, &arrival, ""); got != c.keep {
			t.Errorf("film %s: Related %v, want %v", c.name, got, c.keep)
		}
	}
	for _, sonarr := range []string{"", "v5"} {
		for _, c := range tv {
			if got := Related(ctx, c.rel, &series, sonarr); got != c.keep {
				t.Errorf("series (%q) %s: Related %v, want %v", sonarr, c.name, got, c.keep)
			}
		}
	}
}

// Every row Related drops is one the engine rejects as unable to parse, unknown film or unknown series.
func TestRelatedKeepsWhatDecideMaps(t *testing.T) {
	t.Parallel()
	ctx := WithSearch(context.Background())
	film, tv := relatedCases()
	unmapped := []string{"UnableToParse", "UnknownMovie", string(profile.ReasonUnknownSeries)}
	check := func(kind, name string, kept bool, d *Decision) {
		t.Helper()
		if kept {
			return
		}
		if !slices.ContainsFunc(d.Rejections, func(r Rejection) bool { return slices.Contains(unmapped, r.Reason) }) {
			t.Errorf("%s %s: dropped, but decided with rejections %q", kind, name, reasons(d))
		}
	}
	e := testEngine(t)
	for _, c := range film {
		d, err := e.Decide(ctx, Release{Release: c.rel}, arrival, Version{Key: "movie-remux"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		check("film", c.name, Related(ctx, c.rel, &arrival, ""), d)
	}
	se := seriesEngine(t)
	v := tvVersion(se, "tv")
	series := show()
	search := &SeriesSearch{Kind: profile.SearchSingleEpisode, Season: 1, Episode: 2, Episodes: []int64{102}}
	for _, c := range tv {
		d, err := se.DecideSearch(ctx, Release{Release: c.rel}, series, v, search)
		if err != nil {
			t.Fatal(err)
		}
		check("series", c.name, Related(ctx, c.rel, &series, v.Policy.Sonarr), d)
	}
}
