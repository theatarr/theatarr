package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/router"
)

func TestPrintOffers(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printOffers(&out, []router.OfferResult{
		{Version: "movie-web", Outcome: router.OfferPlanned, Release: "Adopted.Movie.2160p.WEB-DL"},
		{Version: "movie-extra", Outcome: router.OfferDeclined, Release: "Arr.Movie", Reasons: []string{"no_hash"}},
	})
	for _, want := range []string{"movie-web", "planned", "Adopted.Movie.2160p.WEB-DL", "movie-extra", "declined", "Arr.Movie", "(no_hash)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q lacks %q", out.String(), want)
		}
	}
}
