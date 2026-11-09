package acquire

import "testing"

// A user-invoked search that names its movies or episodes searches them unmonitored too, as the arrs do (Radarr
// MoviesSearchService, Sonarr EpisodeSearch); a season search and the wanted loop still take monitored items only.
func TestNamedSearchTakesUnmonitored(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.exec(`UPDATE version SET monitored = 0 WHERE id IN (102, 201)`)
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 1003`)
	e.exec(`INSERT INTO arrview_command (id, view, name, status, queued_at) VALUES (2, 'radarr', 'MoviesSearch',
		'queued', 0), (3, 'sonarr', 'EpisodeSearch', 'queued', 0), (4, 'sonarr', 'SeasonSearch', 'queued', 0)`)
	if q, _, err := e.a.Submit(e.ctx, 2, Selection{Versions: []string{"movie"}, TitleIDs: []int64{2}}); err != nil || q != 1 {
		t.Fatalf("MoviesSearch for an unmonitored movie: %d queued, %v", q, err)
	}
	if q, _, err := e.a.Submit(e.ctx, 3, Selection{Versions: []string{"series"}, TitleIDs: []int64{10},
		EpisodeIDs: []int64{1003}}); err != nil || q != 1 {
		t.Fatalf("EpisodeSearch for an unmonitored episode: %d queued, %v", q, err)
	}
	e.drain()
	got := e.searches()
	if len(got) != 2 || got[0].Title != "Two" || got[1].Title != "Show" || !got[1].AnyMonitored {
		t.Fatalf("searches %+v", got)
	}
	// A season search of an unmonitored series searches nothing.
	e.exec(`UPDATE version SET monitored = 1 WHERE id = 201`)
	s := 1
	if _, _, err := e.a.Submit(e.ctx, 4, Selection{Versions: []string{"series"}, TitleIDs: []int64{10},
		Season: &s}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE command_id = 4 AND episodes LIKE '%1003%'`); n != 0 {
		t.Errorf("a season search took the unmonitored episode")
	}
}
