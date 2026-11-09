package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/metadata/anime/offlinecache"
)

// seedAnime saves the Mushoku Tensei build (one conflict: S2 Part 1) and a title for its TVDB series.
func (e *env) seedAnime() (conflictID int64, tvdb int) {
	e.t.Helper()
	root := filepath.Join("..", "metadata", "anime", "testdata", "offline")
	src, _, err := offlinecache.Sources(filepath.Join(root, "cache"))
	if err != nil {
		e.t.Fatal(err)
	}
	var fs []anime.Franchise
	b, _ := os.ReadFile(filepath.Join(root, "franchises.json"))
	_ = json.Unmarshal(b, &fs)
	for _, f := range fs {
		if f.Slug != "mushoku-tensei" {
			continue
		}
		res, err := anime.Build(context.Background(), src, f, anime.Options{Today: "2026-10-25"})
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := (&animestore.Store{DB: e.st}).Save(context.Background(), res, "cache", false); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.st.Reader().QueryRow(`SELECT id FROM anime_review WHERE entry_key = 'al:146065' AND kind = 'conflict'`).Scan(&conflictID); err != nil {
		e.t.Fatal(err)
	}
	e.st.Reader().QueryRow(`SELECT tvdb_series FROM anime_mapping WHERE entry_key = 'al:146065'`).Scan(&tvdb)
	e.exec(`INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES (9, 'series', 'Mushoku Tensei', ?, 1, 0, 0)`, tvdb)
	return conflictID, tvdb
}

func TestAnimeAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, _ := e.seedAnime()
	key := e.apiKey()
	var fl struct {
		Items []AnimeFranchise `json:"items"`
	}
	if code := e.getKey(APIBase+"/anime/franchises", key, &fl); code != 200 || len(fl.Items) != 1 || fl.Items[0].Statuses["conflict"] != 1 || fl.Items[0].OpenReview == 0 {
		t.Fatalf("franchises %d %+v", code, fl)
	}
	var d AnimeFranchiseDetail
	if code := e.getKey(APIBase+"/anime/franchises/mushoku-tensei", key, &d); code != 200 {
		t.Fatalf("franchise %d", code)
	}
	if len(d.Orders) != 3 || len(d.Orders[1].Items) == 0 || d.Orders[1].Items[0].Source == "" || len(d.Numbering) == 0 {
		t.Fatalf("detail: %d orders, numbering %d", len(d.Orders), len(d.Numbering))
	}
	if n := d.Numbering[0]; n.Entry == "" || n.EntryNumber == "" || n.Episode == "" {
		t.Errorf("numbering %+v", n)
	}
	if code := e.getKey(APIBase+"/anime/franchises/nope", key, nil); code != 404 {
		t.Errorf("unknown franchise %d", code)
	}
	var rl struct {
		Items []AnimeReviewItem `json:"items"`
	}
	if code := e.getKey(APIBase+"/anime/review", key, &rl); code != 200 || len(rl.Items) == 0 {
		t.Fatalf("review %d", code)
	}
	var item *AnimeReviewItem
	for i := range rl.Items {
		if rl.Items[i].ID == id {
			item = &rl.Items[i]
		}
	}
	if item == nil || item.Mapping == nil || len(item.Mapping.Candidates) < 2 || !strings.Contains(item.Snippet, "mappings:") {
		t.Fatalf("conflict item %+v", item)
	}
	if code := e.getKey(APIBase+"/anime/review?state=bogus", key, nil); code != 400 {
		t.Errorf("bad state %d", code)
	}
	var sn map[string]string
	if code := e.getKey(APIBase+"/anime/review/"+fmtInt(id)+"/snippet", key, &sn); code != 200 || !strings.Contains(sn["yaml"], "al:146065") {
		t.Errorf("snippet %d %v", code, sn)
	}
	post := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(auth.APIKeyHeader, key)
		return e.do(r)
	}
	pick := -1
	for i, c := range item.Mapping.Candidates {
		if c.Source != item.Mapping.Winner && c.Dropped == "" && len(c.Episodes) > 0 {
			pick = i
		}
	}
	if rec := post(APIBase+"/anime/review/"+fmtInt(id)+"/accept", map[string]any{"candidate": 99}); rec.Code != 400 {
		t.Errorf("candidate 99: %d", rec.Code)
	}
	if rec := post(APIBase+"/anime/review/"+fmtInt(id)+"/reject", map[string]any{"candidate": 0}); rec.Code != 400 {
		t.Errorf("reject with a candidate: %d", rec.Code)
	}
	if rec := post(APIBase+"/anime/review/"+fmtInt(id)+"/accept", map[string]any{"candidate": pick, "note": "checked"}); rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(APIBase+"/anime/review/"+fmtInt(id)+"/accept", nil); rec.Code != 409 {
		t.Errorf("accept twice: %d", rec.Code)
	}
	if rec := post(APIBase+"/anime/review/99999/reject", nil); rec.Code != 404 {
		t.Errorf("unknown item: %d", rec.Code)
	}
	var status, winner string
	e.st.Reader().QueryRow(`SELECT status, winner FROM anime_mapping WHERE entry_key = 'al:146065'`).Scan(&status, &winner)
	if status != "override" || winner != item.Mapping.Candidates[pick].Source {
		t.Errorf("after accept: %s %s", status, winner)
	}
	var pl struct {
		Items []AnimePlaylist `json:"items"`
	}
	if code := e.getKey(APIBase+"/anime/playlists", key, &pl); code != 200 || pl.Items == nil {
		t.Errorf("playlists %d", code)
	}
}

func fmtInt(n int64) string { return strconv.FormatInt(n, 10) }
