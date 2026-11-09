package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeasonEpisodeCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != key {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/3/find/tt0903747":
			io.WriteString(w, `{"movie_results": [], "tv_results": [{"id": 1396, "name": "Breaking Bad"}]}`)
		case "/3/find/tt0133093":
			io.WriteString(w, `{"movie_results": [{"id": 603}], "tv_results": []}`)
		case "/3/tv/1396":
			io.WriteString(w, `{"id": 1396, "seasons": [{"season_number": 0, "episode_count": 9},
				{"season_number": 1, "episode_count": 7}, {"season_number": 2, "episode_count": 13},
				{"season_number": 6, "episode_count": 0}]}`)
		default:
			http.Error(w, `{}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv, nil)
	ctx := context.Background()
	id, err := c.FindTVByIMDb(ctx, "tt0903747")
	if err != nil || id != 1396 {
		t.Fatalf("id %d err %v", id, err)
	}
	if _, err := c.FindTVByIMDb(ctx, "tt0133093"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a movie id: %v", err)
	}
	if _, err := c.FindTVByIMDb(ctx, "0903747"); err == nil {
		t.Error("an id without tt accepted")
	}
	m, err := c.SeasonEpisodeCounts(ctx, 1396)
	if err != nil || len(m) != 3 || m[0] != 9 || m[1] != 7 || m[2] != 13 {
		t.Errorf("counts %v err %v", m, err)
	}
}

func TestTVShowAndSeason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/77":
			io.WriteString(w, `{"id": 77, "name": "Show", "status": "Returning Series", "origin_country": ["GB"],
				"episode_run_time": [45], "seasons": [{"season_number": 1, "episode_count": 2}]}`)
		case "/3/tv/77/season/1":
			io.WriteString(w, `{"id": 9, "season_number": 1, "episodes": [{"id": 501, "name": "One", "air_date": "2024-01-02",
				"episode_number": 1, "season_number": 1, "runtime": 44}, {"id": 502, "name": "Two", "air_date": "",
				"episode_number": 2, "season_number": 1, "runtime": null}]}`)
		default:
			http.Error(w, `{}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv, nil)
	ctx := context.Background()
	s, err := c.TVShow(ctx, 77)
	if err != nil || s.Status != "Returning Series" || len(s.Seasons) != 1 || s.OriginCountry[0] != "GB" {
		t.Fatalf("show %+v err %v", s, err)
	}
	se, err := c.TVSeasonEpisodes(ctx, 77, 1)
	if err != nil || len(se.Episodes) != 2 || se.Episodes[0].ID != 501 || *se.Episodes[0].Runtime != 44 ||
		se.Episodes[1].Runtime != nil {
		t.Fatalf("season %+v err %v", se, err)
	}
	if _, err := c.TVSeasonEpisodes(ctx, 77, 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing season: %v", err)
	}
}
