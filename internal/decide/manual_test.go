package decide

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestManualDecisionOverrides(t *testing.T) {
	e := testEngine(t)
	v := Version{Key: "movie-remux", Policy: e.Policy.Versions["movie-remux"]}
	if v.Policy == nil {
		t.Fatal("movie test version missing")
	}
	r := rel("Dune.Part.Two.2024.FRENCH.1080p.WEB-DL-OLD", "public", 20*gb)
	group := "CHOSEN"
	r.Manual = &ManualOverrides{Quality: &ManualQuality{Name: "Remux-2160p", Revision: profile.Revision{Version: 3, Real: 1, IsRepack: true}}, Languages: []string{"English"}, ReleaseGroup: &group}
	d, err := e.Decide(context.Background(), r, dune, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Quality != "Remux-2160p" || d.Revision.Version != 3 || d.Revision.Real != 1 || !d.Revision.IsRepack || d.ReleaseGroup != group || len(d.Languages) != 1 || d.Languages[0] != "English" {
		t.Fatalf("overrides %+v", d)
	}
	facts := AggregateMovieFile(ImportFile{Path: "Dune.Part.Two.2024.FRENCH.1080p.WEB-DL-OLD.mkv"})
	r.Manual.Apply(&facts)
	if facts.Quality != d.Quality || facts.Revision != 3 || !facts.IsRepack || facts.ReleaseGroup != group || len(facts.Languages) != 1 || facts.Languages[0] != "English" {
		t.Fatalf("facts %+v", facts)
	}
	x := ExistingFile{Quality: facts.Quality, Revision: facts.Revision, Real: facts.Real, IsRepack: facts.IsRepack,
		ReleaseGroup: facts.ReleaseGroup, Languages: facts.Languages}
	if err := e.ScoreFile(&dune, v, &x); err != nil || x.Score == nil {
		t.Fatalf("score %+v: %v", x, err)
	}
	if err := e.ScoreFile(&dune, Version{Policy: &policy.VersionPolicy{Kind: policy.KindMovie, Profile: "missing"}}, &x); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if q := (*ManualOverrides)(nil).quality(format.Radarr, profile.QualityModel{}); q.Quality.Name != "" {
		t.Fatal(q)
	}
}
