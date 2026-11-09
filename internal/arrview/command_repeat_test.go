package arrview

import (
	"strings"
	"testing"
)

// A search command that has finished no longer dedupes a later identical command, whether or not a
// client read its status in between, and its row is stored with the final status.
func TestFinishedSearchCommandDoesNotDedupeRepeat(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	body := map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 1}}
	var c1, c2, c3 map[string]any
	e.call("POST", "/radarr/api/v3/command", body, 201, &c1)
	e.drain()
	if got := e.searches(); strings.Join(got, "|") != "movie-web Alpha" {
		t.Fatalf("first searches %v", got)
	}
	// Nobody read the first command's status: the repeat is still a new command.
	e.call("POST", "/radarr/api/v3/command", body, 201, &c2)
	e.drain()
	if c2["id"] == c1["id"] {
		t.Fatalf("repeat deduped onto finished command %v", c1["id"])
	}
	// The repeat ran its own search: the first one's download is in flight, so it starts nothing new.
	if c := e.command(c2["id"]); c["status"] != "completed" || !strings.Contains(c["message"].(string), "in flight") {
		t.Fatalf("second command %v %v", c["status"], c["message"])
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_command WHERE status IN ('queued', 'started')`); n != 0 {
		t.Errorf("%d finished commands still stored as queued", n)
	}
	e.call("POST", "/radarr/api/v3/command", body, 201, &c3)
	e.drain()
	if c3["id"] == c2["id"] {
		t.Errorf("third repeat deduped onto %v", c2["id"])
	}
}
