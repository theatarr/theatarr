package animestore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/metadata/anime/offlinecache"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const fixture = "../testdata/offline"

func openStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func build(t *testing.T, slug string) *anime.Result {
	t.Helper()
	src, _, err := offlinecache.Sources(filepath.Join(fixture, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	var fs []anime.Franchise
	b, err := os.ReadFile(filepath.Join(fixture, "franchises.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Slug != slug {
			continue
		}
		ov, err := anime.LoadOverrides(filepath.Join(fixture, "overrides"), slug)
		if err != nil {
			t.Fatal(err)
		}
		res, err := anime.Build(context.Background(), src, f, anime.Options{Today: "2026-10-25", Overrides: ov})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	t.Fatalf("no franchise %s", slug)
	return nil
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveAndReview(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := &animestore.Store{DB: st, Now: func() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) }}
	res := build(t, "mushoku-tensei")
	saved, err := s.Save(ctx, res, "cache", false)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Applied || len(saved.Held) > 0 {
		t.Fatalf("first save: %+v", saved)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_entry WHERE franchise = 'mushoku-tensei'`); n != len(res.Entries) {
		t.Errorf("entries %d, want %d", n, len(res.Entries))
	}
	if n := count(t, st, `SELECT count(*) FROM anime_order WHERE franchise = 'mushoku-tensei' AND kind = 'watch'`); n != len(res.WatchOrder) {
		t.Errorf("watch rows %d, want %d", n, len(res.WatchOrder))
	}
	if n := count(t, st, `SELECT count(*) FROM anime_stage WHERE franchise = 'mushoku-tensei'`); n != 5 {
		t.Errorf("stages %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_node WHERE franchise = 'mushoku-tensei' AND kind = 'line'`); n == 0 {
		t.Error("no line nodes")
	}
	// Mushoku S2 Part 1: ScudLee and anibridge disagree within the margin (episode 0) -> conflict
	var status string
	if err := st.Reader().QueryRow(`SELECT status FROM anime_mapping WHERE franchise = 'mushoku-tensei' AND entry_key = 'al:146065'`).
		Scan(&status); err != nil || status != anime.StatusConflict {
		t.Fatalf("al:146065 status %q (%v)", status, err)
	}
	items, err := s.Items(ctx, "open", "mushoku-tensei")
	if err != nil {
		t.Fatal(err)
	}
	var conflict *animestore.Item
	for i := range items {
		if items[i].EntryKey == "al:146065" && items[i].Kind == "conflict" {
			conflict = &items[i]
		}
	}
	if conflict == nil || saved.NewReview != len(items) {
		t.Fatalf("review items %+v (new %d)", items, saved.NewReview)
	}
	// the same build again: nothing new, items stay open
	saved, err = s.Save(ctx, res, "cache", false)
	if err != nil || !saved.Applied || saved.NewReview != 0 {
		t.Fatalf("second save: %+v %v", saved, err)
	}
	if err := s.Resolve(ctx, conflict.ID, true, "anibridge is right: ep 0 is S00E02"); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, conflict.ID, true, ""); err != animestore.ErrNotOpen {
		t.Errorf("resolving twice: %v", err)
	}
	// the decision sticks across rebuilds
	if _, err := s.Save(ctx, res, "cache", false); err != nil {
		t.Fatal(err)
	}
	st.Reader().QueryRow(`SELECT status FROM anime_mapping WHERE franchise = 'mushoku-tensei' AND entry_key = 'al:146065'`).Scan(&status)
	if status != anime.StatusOverride {
		t.Errorf("after accept and rebuild: %q", status)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_review WHERE entry_key = 'al:146065' AND state = 'open'`); n != 0 {
		t.Errorf("accepted conflict reopened: %d", n)
	}
	back, err := s.Result(ctx, "mushoku-tensei")
	if err != nil || len(back.Entries) != len(res.Entries) {
		t.Fatalf("stored result: %v", err)
	}
}

func TestRefreshHeld(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := &animestore.Store{DB: st}
	res := build(t, "re-zero")
	if saved, err := s.Save(ctx, res, "cache", false); err != nil || !saved.Applied {
		t.Fatalf("%+v %v", saved, err)
	}
	before := count(t, st, `SELECT count(*) FROM anime_mapping WHERE franchise = 're-zero'`)

	// a refresh that loses Re:Zero season 1's mapping drops more than 10% of TVDB 305089's episodes
	shrunk := build(t, "re-zero")
	for _, e := range shrunk.Entries {
		if e.Key == "al:21355" {
			e.TVDB, e.Mapping = nil, nil
		}
	}
	saved, err := s.Save(ctx, shrunk, "live", false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Applied || len(saved.Held) != 1 {
		t.Fatalf("shrinking refresh: %+v", saved)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_mapping WHERE franchise = 're-zero' AND entry_key = 'al:21355'`); n != 1 {
		t.Error("held refresh was applied")
	}
	items, _ := s.Items(ctx, "open", "re-zero")
	var id int64
	for _, it := range items {
		if it.Kind == "refresh_shrink" {
			id = it.ID
		}
	}
	if id == 0 {
		t.Fatalf("no refresh_shrink item: %+v", items)
	}
	if err := s.Resolve(ctx, id, true, "S1 really is gone"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_mapping WHERE franchise = 're-zero'`); n != before-1 {
		t.Errorf("after accepting: %d mappings, want %d", n, before-1)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_franchise WHERE slug = 're-zero' AND pending_result IS NULL AND source = 'live'`); n != 1 {
		t.Error("pending build not applied")
	}

	// a refresh that moves a mapping with a linked file is held too
	if _, err := s.Save(ctx, res, "cache", true); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	mustExec(t, w, `INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (1, 'series', 'Re:Zero', 305089, 0, 0)`)
	mustExec(t, w, `INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (1, 1, 0, 26, 0, 0)`)
	mustExec(t, w, `INSERT INTO link (id, path, target, title_id, created_at, updated_at) VALUES (1, '/l/a.mkv', '/m/a.mkv', 1, 0, 0)`)
	mustExec(t, w, `INSERT INTO link_episode (link_id, episode_id) VALUES (1, 1)`)
	moved := build(t, "re-zero")
	for _, e := range moved.Entries {
		if e.Key == "al:100049#1" { // Memory Snow, S00E26
			e.TVDB.Episodes = []anime.SE{{0, 40}}
		}
	}
	saved, err = s.Save(ctx, moved, "live", false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Applied || len(saved.Held) != 1 {
		t.Fatalf("moving a linked mapping: %+v", saved)
	}
	items, _ = s.Items(ctx, "open", "re-zero")
	found := false
	for _, it := range items {
		if it.Kind == "refresh_moves_linked" && it.EntryKey == "al:100049#1" {
			found = true
			if err := s.Resolve(ctx, it.ID, false, "keep"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found {
		t.Fatalf("no refresh_moves_linked item: %+v", items)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_franchise WHERE slug = 're-zero' AND pending_result IS NULL`); n != 1 {
		t.Error("rejected refresh still pending")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSonarrTVDB(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	w := st.Writer()
	mustExec(t, w, `INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (1, 'series', 'Show', 77, 0, 0)`)
	mustExec(t, w, `INSERT INTO title_external_id (title_id, source, external_id) VALUES (1, 'anilist', '21355')`)
	mustExec(t, w, `INSERT INTO episode (id, title_id, season, number, name, air_date, runtime_minutes, created_at, updated_at)
		VALUES (1, 1, 1, 1, 'One', '2016-04-04', 50, 0, 0), (2, 1, 0, 1, 'Special', NULL, NULL, 0, 0)`)
	mustExec(t, w, `INSERT INTO episode_number (episode_id, title_id, scheme, number, source) VALUES (1, 1, 'absolute_tvdb', 1, 'x')`)
	mustExec(t, w, `INSERT INTO arr_alias (instance, kind, arr_id, target_id, created_at) VALUES ('sonarr_anime_debrid', 'episode', 9, 1, 0)`)
	src := animestore.SonarrTVDB{DB: st, Instance: "sonarr_anime_debrid"}
	sh, err := src.Show(ctx, 77)
	if err != nil {
		t.Fatal(err)
	}
	if *sh.Title != "Show" || len(sh.Episodes) != 2 || !sh.HasEpisodes || string(sh.AniListIDs) != "[21355]" {
		t.Fatalf("show %+v", sh)
	}
	e := sh.Episodes[1]
	if e.SeasonNumber != 1 || *e.AbsoluteEpisodeNumber != 1 || *e.AirDate != "2016-04-04" || *e.Runtime != 50 {
		t.Errorf("episode %+v", e)
	}
	if _, err := src.Show(ctx, 78); err == nil {
		t.Error("unknown series")
	}
	eps, err := src.SonarrEpisodes(ctx, []int{77, 78})
	if err != nil || len(eps) != 1 || !eps[77][anime.SE{1, 1}] || eps[77][anime.SE{0, 1}] {
		t.Errorf("sonarr episodes %v %v", eps, err)
	}
}

// Migration 00017 goes down and up cleanly.
func TestMigration17(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "theatarr.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sub, err := fs.Sub(os.DirFS("../../../store"), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(database.DialectSQLite3, st.Writer(), sub, goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(store.MigrationTable), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	mustExec(t, w, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES ('tmdb', 'k', 200, x'00', 0, 0)`)
	for range 2 {
		if _, err := p.UpTo(ctx, 17); err != nil {
			t.Fatal(err)
		}
		mustExec(t, w, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('x', 'X', 1, 0, 0)`)
		mustExec(t, w, `INSERT INTO anime_review (franchise, kind, summary, created_at) VALUES ('x', 'conflict', 's', 0)`)
		if _, err := w.Exec(`INSERT INTO anime_review (franchise, kind, summary, created_at) VALUES ('x', 'conflict', 's', 0)`); err == nil {
			t.Error("two open items for the same entry and kind")
		}
		if _, err := w.Exec(`INSERT INTO anime_mapping (franchise, entry_key, status, updated_at) VALUES ('x', 'k', 'bogus', 0)`); err == nil {
			t.Error("status CHECK missing")
		}
		var etag string
		if err := w.QueryRow(`SELECT etag FROM metadata_cache WHERE key = 'k'`).Scan(&etag); err != nil || etag != "" {
			t.Errorf("etag column: %q %v", etag, err)
		}
		mustExec(t, w, `DELETE FROM anime_franchise`)
		if n := count(t, st, `SELECT count(*) FROM anime_review`); n != 0 {
			t.Error("review rows survive their franchise")
		}
		if _, err := p.DownTo(ctx, 16); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Exec(`SELECT etag FROM metadata_cache`); err == nil {
			t.Error("down: etag column still there")
		}
	}
}
