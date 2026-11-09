package decide

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

func TestMovieRuntimeWaitsInsteadOfUsingMedian(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	q, _ := profile.QualityByName(format.Radarr, "Remux-2160p")
	minimum := 136.8
	e.Profile("remux-2160p", format.Radarr).Engine.Definitions.Set(profile.Definition{Quality: q, MinSize: &minimum})
	title := dune
	title.Name, title.Year, title.Runtime = "Scooby-Doo", 2002, 0
	r := rel("Scooby-Doo.2002.2160p.BluRay.REMUX-GRP", "public", 14685320000)
	// Prefer-off: Radarr's 110-minute assumption, as before standing existed.
	d, err := e.Decide(context.Background(), r, title, Version{Key: "movie-remux"}, nil)
	if err != nil || len(d.Rejections) != 1 || d.Rejections[0].Reason != "BelowMinimumSize" {
		t.Fatalf("prefer-off unknown runtime must keep the 110-minute check: %+v %v", d, err)
	}
	e.Policy.Versions["movie-remux"].PreferSeaDex = true
	d, err = e.Decide(context.Background(), r, title, Version{Key: "movie-remux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rejections) != 1 || d.Rejections[0].Reason != "UnknownRuntime" || !d.Rejections[0].Temporary {
		t.Fatalf("unknown runtime should wait, not fail a guessed size: %+v", d.Rejections)
	}
	title.Runtime = 87
	d, err = e.Decide(context.Background(), r, title, Version{Key: "movie-remux"}, nil)
	if err != nil || !d.Accepted {
		t.Fatalf("real runtime should accept the release: %+v %v", d, err)
	}
}
