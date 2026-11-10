package acquire

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/settings"
)

type fakeMeta struct{ queued []int64 }

func (f *fakeMeta) Enqueue(_ context.Context, id int64) error {
	f.queued = append(f.queued, id)
	return nil
}

// A series added in Theatarr searches only after its first metadata refresh: the search is held, the refresh queued, and
// ReleaseWaiting starts it with the episodes the refresh stored.
func TestSearchWaitsForMetadata(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	m := &fakeMeta{}
	e.a.SetMetadata(m)
	if _, err := e.st.Writer().Exec(`INSERT INTO series_meta (title_id, added_at) VALUES (10, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO arrview_command (id, view, name, status, queued_at)
		VALUES (2, 'sonarr', 'MissingEpisodeSearch', 'queued', 0), (3, 'sonarr', 'SeriesSearch', 'queued', 0)`); err != nil {
		t.Fatal(err)
	}
	sel := Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Only: OnlyMissing}
	queued, found, err := e.a.Submit(e.ctx, 2, sel)
	if err != nil || queued != 1 || found != 1 {
		t.Fatalf("submit %d %d %v", queued, found, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run`); n != 0 {
		t.Fatalf("%d runs before the refresh", n)
	}
	if len(m.queued) != 1 || m.queued[0] != 10 {
		t.Fatalf("refresh not queued: %v", m.queued)
	}
	// A search naming episodes is not held.
	if q, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"series"}, EpisodeIDs: []int64{1001}}); err != nil || q != 1 {
		t.Fatalf("episode search %d %v", q, err)
	}
	// A search held for a series the refresh then finds nothing for completes its command.
	five := 5
	if _, _, err := e.a.Submit(e.ctx, 3, Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Season: &five}); err != nil {
		t.Fatal(err)
	}
	// The refresh finished.
	if _, err := e.st.Writer().Exec(`UPDATE series_meta SET added_at = NULL, refreshed_at = 2`); err != nil {
		t.Fatal(err)
	}
	if err := e.a.ReleaseWaiting(e.ctx, 10); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE command_id = 2`); n != 3 {
		t.Errorf("%d runs of the held search, want 3 (s00e01, s01, s02e01)", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_search_wait`); n != 0 {
		t.Errorf("%d held searches left", n)
	}
	var status string
	if err := e.st.Reader().QueryRow(`SELECT status FROM arrview_command WHERE id = 3`).Scan(&status); err != nil ||
		status != "completed" {
		t.Errorf("command 3 %q %v", status, err)
	}
	// Once refreshed, searches run at once.
	if _, _, err := e.a.Submit(e.ctx, 0, sel); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_search_wait`); n != 0 || len(m.queued) != 2 {
		t.Errorf("held again: %d, %v", n, m.queued)
	}
}

func TestHeldSearchUsesRuntimeAfterReload(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.a.SetMetadata(&fakeMeta{})
	current := int64(1)
	e.a.runtimeCapture = func(ctx context.Context) context.Context { return settings.WithGeneration(ctx, current) }
	if _, err := e.st.Writer().Exec(`INSERT INTO series_meta (title_id, added_at) VALUES (10, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO arrview_command (id, view, name, status, queued_at)
		VALUES (20, 'sonarr', 'SeriesSearch', 'queued', 0)`); err != nil {
		t.Fatal(err)
	}
	sel := Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Only: OnlyMissing}
	if queued, _, err := e.a.Submit(e.ctx, 20, sel); err != nil || queued != 1 {
		t.Fatalf("hold search: queued=%d err=%v", queued, err)
	}
	var stored string
	if err := e.st.Reader().QueryRow(`SELECT selection FROM series_search_wait WHERE title_id = 10`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "generation") {
		t.Fatalf("held selection persisted a generation: %s", stored)
	}
	current = 2 // settings reload while metadata is pending
	if _, err := e.st.Writer().Exec(`UPDATE series_meta SET added_at = NULL, refreshed_at = 2 WHERE title_id = 10`); err != nil {
		t.Fatal(err)
	}
	if err := e.a.ReleaseWaiting(e.ctx, 10); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ?`, JobSearch).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "generation") {
		t.Fatalf("search job persisted a generation: %s", payload)
	}
	var observed []int64
	e.onSearch = func(ctx context.Context) error {
		observed = append(observed, RuntimeGeneration(ctx))
		return nil
	}
	ran, err := e.w.RunOne(e.ctx)
	if err != nil || !ran {
		t.Fatalf("run held search: ran=%t err=%v", ran, err)
	}
	if len(observed) == 0 {
		t.Fatal("held search did not execute after metadata release")
	}
	for _, generation := range observed {
		if generation != 2 {
			t.Fatalf("held search ran under generation %d after reload: %v", generation, observed)
		}
	}
}

// An episode the metadata source dropped is searched only when named.
func TestRemovedEpisodesNotSearched(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	if _, err := e.st.Writer().Exec(`UPDATE episode SET removed_at = 1 WHERE id = 1004`); err != nil {
		t.Fatal(err)
	}
	ts, err := e.a.targets(e.ctx, Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Only: OnlyMissing})
	if err != nil || len(ts) != 2 || ts[0].name != "s00e01" || ts[1].name != "s01" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	ts, err = e.a.targets(e.ctx, Selection{Versions: []string{"series"}, EpisodeIDs: []int64{1004}})
	if err != nil || len(ts) != 1 {
		t.Fatalf("named %+v %v", ts, err)
	}
}

// A library's anime setting takes a series by its anime flag or its series type (the title's): the arr views
// choose a series' libraries by the same rule (arrview TestSeriesTypeChoosesSearchableLibraries).
func TestTargetsAnimeLibraryBySeriesType(t *testing.T) {
	e := newEnv(t, true, nil)
	yes, no := true, false
	sel := Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Only: OnlyMissing}
	for _, tc := range []struct {
		stored  any
		flag    int
		library *bool
		want    bool
	}{
		{"anime", 0, &yes, true}, {"anime", 0, &no, false},
		{"standard", 0, &yes, false}, {"standard", 0, &no, true},
		{"standard", 1, &yes, true}, {nil, 1, &yes, true}, {nil, 0, &yes, false},
	} {
		if _, err := e.st.Writer().Exec(`UPDATE title SET series_type = ?, anime = ? WHERE id = 10`, tc.stored, tc.flag); err != nil {
			t.Fatal(err)
		}
		e.pol.Versions["series"].Anime = tc.library
		ts, err := e.a.targets(e.ctx, sel)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(ts) > 0; got != tc.want {
			t.Errorf("type %v, flag %d, anime library %v: searched=%v, want %v", tc.stored, tc.flag, *tc.library, got, tc.want)
		}
	}
}
