package catalog

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A parallel version takes its titles from the version it follows; an add without versions leaves it out.
func TestDefaultVersionsLeaveOutFollowers(t *testing.T) {
	t.Parallel()
	_, pol := setup(t)
	f := *pol.Versions["movie-remux"]
	f.Follows = "movie-remux"
	pol.Versions["movie-remux-theatarr"] = &f
	if got := strings.Join(DefaultVersions(pol, policy.KindMovie, false), " "); got != "movie-remux movie-web" {
		t.Fatalf("defaults %q", got)
	}
}
