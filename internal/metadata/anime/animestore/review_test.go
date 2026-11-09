package animestore_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/store"
)

func TestResolveCandidateAndSnippet(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := &animestore.Store{DB: st}
	res := build(t, "mushoku-tensei")
	if _, err := s.Save(ctx, res, "cache", false); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Items(ctx, "open", "mushoku-tensei")
	var it *animestore.Item
	for i := range items {
		if items[i].EntryKey == "al:146065" && items[i].Kind == "conflict" {
			it = &items[i]
		}
	}
	if it == nil {
		t.Fatal("no conflict")
	}
	d, ok := it.MappingDetail()
	if !ok || len(d.Candidates) < 2 {
		t.Fatalf("detail %+v", d)
	}
	snip, err := s.Snippet(ctx, it.ID)
	if err != nil || !strings.Contains(snip, "mappings:") || !strings.Contains(snip, `entry: "al:146065"`) {
		t.Fatalf("snippet %q %v", snip, err)
	}
	// the snippet is a valid overrides file
	if ov, err := anime.ParseOverrides([]byte(strings.ReplaceAll(snip, "  # -", "  -"))); err != nil || len(ov.Mappings) < 2 {
		t.Errorf("snippet does not parse: %v\n%s", err, snip)
	}
	pick := -1
	for i, c := range d.Candidates {
		if c.Source != d.Winner && c.Dropped == "" && len(c.Episodes) > 0 {
			pick = i
		}
	}
	if pick < 0 {
		t.Fatalf("no other candidate: %+v", d.Candidates)
	}
	if err := s.ResolveCandidate(ctx, it.ID, 99, ""); err == nil {
		t.Error("candidate 99 accepted")
	}
	if err := s.ResolveCandidate(ctx, it.ID, pick, "scudlee is right"); err != nil {
		t.Fatal(err)
	}
	want := d.Candidates[pick]
	check := func(when string) {
		var status, winner, eps string
		var series int
		if err := st.Reader().QueryRow(`SELECT status, winner, tvdb_series, episodes FROM anime_mapping
			WHERE franchise = 'mushoku-tensei' AND entry_key = 'al:146065'`).Scan(&status, &winner, &series, &eps); err != nil {
			t.Fatal(err)
		}
		var got []anime.SE
		_ = json.Unmarshal([]byte(eps), &got)
		if status != anime.StatusOverride || winner != want.Source || series != want.SeriesID || len(got) != len(want.Episodes) {
			t.Errorf("%s: %s %s %d %v, want the %s candidate %v", when, status, winner, series, got, want.Source, want.Episodes)
		}
	}
	check("after accepting")
	if _, err := s.Save(ctx, res, "cache", false); err != nil {
		t.Fatal(err)
	}
	check("after a rebuild")
	if err := s.ResolveCandidate(ctx, it.ID, pick, ""); err != animestore.ErrNotOpen {
		t.Errorf("twice: %v", err)
	}
}

func TestAltCutsSaved(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := &animestore.Store{DB: st}
	res := build(t, "re-zero")
	res.AltCuts = []*anime.AltCutMap{{Name: "Director's Cut", SeriesID: 305089, Episodes: map[int][]anime.SE{1: {{1, 1}}, 2: {{1, 2}, {1, 3}}}}}
	if _, err := s.Save(ctx, res, "cache", false); err != nil {
		t.Fatal(err)
	}
	var eps string
	if err := st.Reader().QueryRow(`SELECT episodes FROM anime_alt_cut WHERE franchise = 're-zero' AND tvdb_series = 305089`).
		Scan(&eps); err != nil || eps != `{"1":[[1,1]],"2":[[1,2],[1,3]]}` {
		t.Errorf("alt cut row %q %v", eps, err)
	}
}

// Migration 00018 goes down and up cleanly.
func TestMigration18(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 17); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	mustExec(t, w, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('x', 'X', 1, 0, 0)`)
	mustExec(t, w, `INSERT INTO anime_review (franchise, kind, summary, created_at) VALUES ('x', 'conflict', 's', 0)`)
	for range 2 {
		if _, err := p.UpTo(ctx, 18); err != nil {
			t.Fatal(err)
		}
		mustExec(t, w, `UPDATE anime_review SET choice = '{}'`)
		mustExec(t, w, `INSERT INTO anime_alt_cut (franchise, name, tvdb_series, episodes) VALUES ('x', 'DC', 1, '{}')`)
		mustExec(t, w, `INSERT INTO plex_playlist (franchise, order_kind, name, created_at, updated_at) VALUES ('x', 'watch', 'X (watch)', 0, 0)`)
		if _, err := w.Exec(`INSERT INTO plex_playlist (franchise, order_kind, name, created_at, updated_at) VALUES ('x', 'watch', 'again', 0, 0)`); err == nil {
			t.Error("two playlists for one franchise, order and route")
		}
		if _, err := p.DownTo(ctx, 17); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Exec(`SELECT choice FROM anime_review`); err == nil {
			t.Error("down: choice column still there")
		}
	}
}

// An accept in review, or a build that turns a conflict importable, queues one anime_relink job for the mapping's
// series; a reject, a first build and a rebuild that changes no status queue none.
func TestRelinkQueued(t *testing.T) {
	ctx := context.Background()
	const entry = "al:146065" // a conflict in the mushoku fixture
	setup := func(t *testing.T) (*store.Store, *animestore.Store, *animestore.Item, int) {
		st := openStore(t)
		s := &animestore.Store{DB: st}
		if _, err := s.Save(ctx, build(t, "mushoku-tensei"), "cache", false); err != nil {
			t.Fatal(err)
		}
		items, _ := s.Items(ctx, "open", "mushoku-tensei")
		var it *animestore.Item
		for i := range items {
			if items[i].EntryKey == entry && items[i].Kind == "conflict" {
				it = &items[i]
			}
		}
		if it == nil {
			t.Fatal("no conflict")
		}
		series := count(t, st, `SELECT tvdb_series FROM anime_mapping WHERE franchise = 'mushoku-tensei' AND entry_key = ?`, entry)
		if n := count(t, st, `SELECT count(*) FROM job WHERE kind = ?`, animemap.RelinkJobKind); n != 0 || series == 0 {
			t.Fatalf("first build queued %d relinks (series %d)", n, series)
		}
		return st, s, it, series
	}
	jobsFor := func(t *testing.T, st *store.Store, series int) int {
		return count(t, st, `SELECT count(*) FROM job WHERE kind = ? AND dedupe_key = ?`, animemap.RelinkJobKind,
			animemap.RelinkDedupeKey(series))
	}
	all := func(t *testing.T, st *store.Store) int {
		return count(t, st, `SELECT count(*) FROM job WHERE kind = ?`, animemap.RelinkJobKind)
	}

	t.Run("reject", func(t *testing.T) {
		st, s, it, _ := setup(t)
		if err := s.Resolve(ctx, it.ID, false, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Save(ctx, build(t, "mushoku-tensei"), "cache", false); err != nil {
			t.Fatal(err)
		}
		if n := all(t, st); n != 0 {
			t.Errorf("%d relinks", n)
		}
	})
	t.Run("accept", func(t *testing.T) {
		st, s, it, series := setup(t)
		if err := s.Resolve(ctx, it.ID, true, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Save(ctx, build(t, "mushoku-tensei"), "cache", false); err != nil {
			t.Fatal(err)
		}
		if n, m := jobsFor(t, st, series), all(t, st); n != 1 || m != 1 {
			t.Errorf("relinks %d of %d", n, m)
		}
	})
	t.Run("accept a candidate", func(t *testing.T) {
		st, s, it, _ := setup(t)
		d, _ := it.MappingDetail()
		pick := -1
		for i, c := range d.Candidates {
			if c.Dropped == "" && len(c.Episodes) > 0 && c.SeriesID != 0 {
				pick = i
			}
		}
		if err := s.ResolveCandidate(ctx, it.ID, pick, ""); err != nil {
			t.Fatal(err)
		}
		if n := jobsFor(t, st, d.Candidates[pick].SeriesID); n != 1 {
			t.Errorf("relinks %d", n)
		}
	})
	t.Run("build turns it importable", func(t *testing.T) {
		st, s, _, series := setup(t)
		res := build(t, "mushoku-tensei")
		for _, e := range res.Entries {
			if e.Key == entry {
				e.Mapping.Status = anime.StatusAuto
			}
		}
		if saved, err := s.Save(ctx, res, "cache", false); err != nil || !saved.Applied {
			t.Fatalf("save %+v %v", saved, err)
		}
		if n, m := jobsFor(t, st, series), all(t, st); n != 1 || m != 1 {
			t.Errorf("relinks %d of %d", n, m)
		}
	})
}
