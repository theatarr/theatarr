package arrview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/acquire"
)

type failingSearcher struct {
	Searcher
	fail bool
}

func (f *failingSearcher) Submit(ctx context.Context, id int64, sel acquire.Selection) (int, int, error) {
	if f.fail {
		return 0, 0, errors.New("shutting down")
	}
	return f.Searcher.Submit(ctx, id, sel)
}

// A search command whose submit fails is stored as failed, so the same request sent again starts a new search
// instead of returning the stuck one.
func TestSearchCommandSubmitFailure(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	fs := &failingSearcher{Searcher: e.searcher, fail: true}
	e.searcher = fs
	e.start()
	body := map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 1}}
	e.call("POST", "/radarr/api/v3/command", body, 500, nil)
	var status, result string
	if err := e.st.Reader().QueryRow(`SELECT status, result FROM arrview_command`).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || result != "unsuccessful" {
		t.Fatalf("command after a failed submit: %s %s", status, result)
	}
	fs.fail = false
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", body, 201, &c)
	if c["status"] != "queued" || !strings.HasPrefix(c["message"].(string), "Searching 1 items") {
		t.Fatalf("repeat: %v %v", c["status"], c["message"])
	}
	e.drain()
	if got := e.searches(); strings.Join(got, "|") != "movie-web Alpha" {
		t.Fatalf("searches %v", got)
	}
}

// MoviesSearch naming an unmonitored movie searches it, as Radarr does for a manual command; the missing search of
// the same movie does not.
func TestSearchCommandUnmonitoredMovie(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "MissingMoviesSearch", "movieIds": []int{IDBase + 2}},
		201, &c)
	e.drain()
	if got := e.searches(); len(got) != 0 {
		t.Fatalf("missing search of an unmonitored movie: %v", got)
	}
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 2}},
		201, &c)
	e.drain()
	if got := e.searches(); strings.Join(got, "|") != "movie-web Beta" {
		t.Fatalf("searches %v (command %v: %v)", got, c["status"], c["message"])
	}
}
