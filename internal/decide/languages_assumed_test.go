package decide

import (
	"context"
	"testing"
)

// TestDecisionLanguagesAssumed: a name and indexer with no language get the title's original language, and the
// decision says it is assumed; a stated language, the indexer's or a manual one is not.
func TestDecisionLanguagesAssumed(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t)
	v := Version{Key: "movie-remux", Policy: e.Policy.Versions["movie-remux"]}
	se := seriesEngine(t)
	tv := tvVersion(se, "tv")
	indexer := rel("Dune.Part.Two.2024.1080p.WEB-DL-GRP", "public", 20*gb)
	indexer.Languages = []string{"German"}
	manual := rel("Dune.Part.Two.2024.1080p.WEB-DL-GRP", "public", 20*gb)
	manual.Manual = &ManualOverrides{Languages: []string{"English"}}
	for _, c := range []struct {
		name    string
		decide  func() (*Decision, error)
		want    string
		assumed bool
	}{
		{"movie, none stated", func() (*Decision, error) {
			return e.Decide(ctx, rel("Dune.Part.Two.2024.1080p.WEB-DL-GRP", "public", 20*gb), dune, v, nil)
		}, "English", true},
		{"movie, stated", func() (*Decision, error) {
			return e.Decide(ctx, rel("Dune.Part.Two.2024.FRENCH.1080p.WEB-DL-GRP", "public", 20*gb), dune, v, nil)
		}, "French", false},
		{"movie, from the indexer", func() (*Decision, error) { return e.Decide(ctx, indexer, dune, v, nil) }, "German", false},
		{"movie, manual", func() (*Decision, error) { return e.Decide(ctx, manual, dune, v, nil) }, "English", false},
		{"series, none stated", func() (*Decision, error) {
			return se.Decide(ctx, rel("The.Show.S01E02.2160p.WEB-DL-GRP", "public", 8*gb), show(), tv, nil)
		}, "English", true},
		{"series, stated", func() (*Decision, error) {
			return se.Decide(ctx, rel("The.Show.S01E02.German.2160p.WEB-DL-GRP", "public", 8*gb), show(), tv, nil)
		}, "German", false},
	} {
		d, err := c.decide()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(d.Languages) != 1 || d.Languages[0] != c.want || d.LanguagesAssumed != c.assumed {
			t.Errorf("%s: languages %v assumed %v, want %s %v", c.name, d.Languages, d.LanguagesAssumed, c.want, c.assumed)
		}
	}
}

// TestDecisionPreferSeaDex: the decision carries the version's prefer_seadex, so the release check can say the
// SeaDex ordering applies.
func TestDecisionPreferSeaDex(t *testing.T) {
	e := testEngine(t)
	for _, prefer := range []bool{false, true} {
		vp := *e.Policy.Versions["movie-remux"]
		vp.PreferSeaDex = prefer
		d, err := e.Decide(context.Background(), rel("Dune.Part.Two.2024.1080p.WEB-DL-GRP", "public", 20*gb), dune,
			Version{Key: "movie-remux", Policy: &vp}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.PreferSeaDex != prefer {
			t.Errorf("prefer_seadex %v: decision says %v", prefer, d.PreferSeaDex)
		}
	}
}
