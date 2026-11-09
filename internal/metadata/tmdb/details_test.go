package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestDetails(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		q := r.URL.Query()
		if q.Get("api_key") != key {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/3/movie/603":
			if q.Get("append_to_response") != "external_ids,release_dates,alternative_titles,translations,images" || q.Get("include_image_language") != ImageLanguages {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"id": 603, "title": "The Matrix", "overview": "A hacker.", "tagline": "Believe.",
				"poster_path": "/p.jpg", "backdrop_path": "/b.jpg", "vote_average": 8.2, "vote_count": 26000,
				"production_companies": [{"id": 79, "name": "Village Roadshow Pictures"}],
				"release_dates": {"results": [{"iso_3166_1": "US", "release_dates": [
					{"certification": "PG", "type": 4}, {"certification": "R", "type": 3}]},
					{"iso_3166_1": "GB", "release_dates": [{"certification": "15", "type": 3}]}]},
				"images": {"logos": [{"file_path": "/l.png", "iso_639_1": "en"}]}}`)
		case "/3/tv/1396":
			if q.Get("append_to_response") != TVAppendDetails {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"id": 1396, "name": "Breaking Bad", "networks": [{"id": 174, "name": "AMC"}],
				"episode_run_time": [47], "next_episode_to_air": null, "status": "Ended",
				"external_ids": {"tvdb_id": 81189, "imdb_id": "tt0903747"},
				"content_ratings": {"results": [{"iso_3166_1": "US", "rating": "TV-MA"}]}}`)
		case "/3/find/81189":
			if q.Get("external_source") != "tvdb_id" {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"movie_results": [], "tv_results": [{"id": 1396}]}`)
		case "/3/find/1":
			io.WriteString(w, `{"movie_results": [], "tv_results": []}`)
		default:
			http.Error(w, `{}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv, sqlCache(t))
	ctx := context.Background()

	m, err := c.MovieDetails(ctx, 603)
	if err != nil {
		t.Fatal(err)
	}
	if m.Overview != "A hacker." || m.Tagline != "Believe." || m.PosterPath != "/p.jpg" || m.BackdropPath != "/b.jpg" ||
		m.VoteAverage != 8.2 || m.VoteCount != 26000 || m.ProductionCompanies[0].Name != "Village Roadshow Pictures" ||
		len(m.Images.Logos) != 1 || m.Certification("US") != "R" || m.Certification("GB") != "15" ||
		m.Certification("FR") != "" {
		t.Errorf("movie %+v", m)
	}
	// Details skip the response cache: a second call asks TMDb again.
	if _, err := c.MovieDetails(ctx, 603); err != nil {
		t.Fatal(err)
	}
	tv, err := c.TVDetails(ctx, 1396)
	if err != nil {
		t.Fatal(err)
	}
	if tv.Networks[0].Name != "AMC" || tv.EpisodeRunTime[0] != 47 || tv.NextEpisodeToAir != nil ||
		tv.ExternalIDs.TVDbID != 81189 || tv.Certification("US") != "TV-MA" || tv.Certification("DE") != "" {
		t.Errorf("tv %+v", tv)
	}
	if _, err := c.TVDetails(ctx, 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown TV id: %v", err)
	}
	if _, err := c.MovieDetails(ctx, 0); err == nil {
		t.Error("id 0 accepted")
	}
	// Find by TVDB id is cached like the other finds.
	for range 2 {
		if id, err := c.FindTVByTVDB(ctx, 81189); err != nil || id != 1396 {
			t.Errorf("find %d %v", id, err)
		}
	}
	if _, err := c.FindTVByTVDB(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("no match: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["/3/movie/603"] != 2 || calls["/3/find/81189"] != 1 {
		t.Errorf("calls %v", calls)
	}
}
