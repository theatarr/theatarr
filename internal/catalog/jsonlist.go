package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
)

// JSON lists (list_type json; docs/design/arrview.md, Lists): a public https address answering a JSON array of
// objects with imdb_id and/or tmdb_id (the StevenLu shape; title only names an entry in Preview). The list's
// media says what the ids are: movies, or series (a TMDb TV id, or a series' IMDb id). Read through the gateway
// (Fetch), never another HTTP client.

// MaxJSONListItems and MaxJSONListBytes bound a JSON list.
const (
	MaxJSONListItems = 1000
	MaxJSONListBytes = 5 << 20
)

// jsonListItem is one entry of a JSON list. tmdb_id may be a number or a string.
type jsonListItem struct {
	Title  string          `json:"title"`
	IMDbID string          `json:"imdb_id"`
	TMDbID json.RawMessage `json:"tmdb_id"`
}

func (it jsonListItem) tmdbID() int {
	v := strings.Trim(strings.TrimSpace(string(it.TMDbID)), `"`)
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// jsonItems reads a JSON list and resolves its entries to TMDb items of the list's media: by TMDb id, else by IMDb
// id through TMDb's find (cached). unresolved counts the entries with neither, or that TMDb does not know.
func (s *MovieIntake) jsonItems(ctx context.Context, cfg movieIntakeConfig) (items []tmdb.MovieSummary, unresolved int,
	err error) {
	if s.Fetch == nil {
		return nil, 0, errors.New("JSON lists cannot be read now")
	}
	body, err := s.Fetch(ctx, cfg.url)
	if err != nil {
		return nil, 0, err
	}
	var entries []jsonListItem
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&entries); err != nil {
		return nil, 0, errors.New("the address does not answer a JSON array of objects with imdb_id or tmdb_id")
	}
	if len(entries) > MaxJSONListItems {
		return nil, 0, fmt.Errorf("the list has %d entries, more than %d", len(entries), MaxJSONListItems)
	}
	media := "movie"
	if cfg.media == "series" {
		media = "tv"
	}
	seen := map[int]bool{}
	for _, e := range entries {
		id := e.tmdbID()
		if imdb := strings.TrimSpace(e.IMDbID); id == 0 && strings.HasPrefix(imdb, "tt") {
			find := s.TMDb.FindByIMDb
			if media == "tv" {
				find = s.TMDb.FindTVByIMDb
			}
			id, err = find(ctx, imdb)
			if errors.Is(err, tmdb.ErrNotFound) {
				id, err = 0, nil
			}
			if err != nil {
				return nil, 0, err
			}
		}
		if id <= 0 {
			unresolved++
			continue
		}
		if !seen[id] {
			seen[id] = true
			items = append(items, tmdb.MovieSummary{ID: id, MediaType: media, Title: e.Title, Name: e.Title})
		}
	}
	return items, unresolved, nil
}
