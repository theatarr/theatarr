package animestore_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

func TestResolveMapping(t *testing.T) {
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
	d, _ := it.MappingDetail()
	want := []anime.SE{{2, 13}, {2, 14}, {2, 15}}
	c := anime.MappingCandidate{Source: animestore.ManualSource, SeriesID: d.TVDB, Episodes: want, Range: "S02E13-E15"}
	if err := s.ResolveMapping(ctx, it.ID, c, "by hand"); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		var status, winner, eps, reason string
		var series int
		if err := st.Reader().QueryRow(`SELECT status, winner, tvdb_series, episodes, reason FROM anime_mapping
			WHERE franchise = 'mushoku-tensei' AND entry_key = 'al:146065'`).Scan(&status, &winner, &series, &eps, &reason); err != nil {
			t.Fatal(err)
		}
		var got []anime.SE
		_ = json.Unmarshal([]byte(eps), &got)
		if status != anime.StatusOverride || winner != "manual" || series != d.TVDB || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %s %s %d %v (%s), want override manual %d %v", when, status, winner, series, got, reason, d.TVDB, want)
		}
	}
	check("after accepting")
	if n := count(t, st, `SELECT count(*) FROM job WHERE kind = ? AND dedupe_key = ?`, animemap.RelinkJobKind,
		animemap.RelinkDedupeKey(d.TVDB)); n != 1 {
		t.Errorf("relink jobs = %d, want 1", n)
	}
	if _, err := s.Save(ctx, res, "cache", false); err != nil {
		t.Fatal(err)
	}
	check("after a rebuild")
	if err := s.ResolveMapping(ctx, it.ID, c, ""); err != animestore.ErrNotOpen {
		t.Errorf("twice: %v", err)
	}

	// a held refresh has no mapping to set
	if _, err := s.Save(ctx, build(t, "re-zero"), "cache", false); err != nil {
		t.Fatal(err)
	}
	shrunk := build(t, "re-zero")
	for _, e := range shrunk.Entries {
		if e.Key == "al:21355" {
			e.TVDB, e.Mapping = nil, nil
		}
	}
	if _, err := s.Save(ctx, shrunk, "live", false); err != nil {
		t.Fatal(err)
	}
	items, _ = s.Items(ctx, "open", "re-zero")
	var held int64
	for _, x := range items {
		if x.Kind == "refresh_shrink" {
			held = x.ID
		}
	}
	if held == 0 {
		t.Fatalf("no refresh_shrink item: %+v", items)
	}
	if err := s.ResolveMapping(ctx, held, c, ""); !errors.Is(err, animestore.ErrNoCandidate) {
		t.Errorf("held refresh: %v, want ErrNoCandidate", err)
	}
}

func TestHeldMappingsApplyChoices(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := &animestore.Store{DB: st}
	if _, err := s.Save(ctx, build(t, "mushoku-tensei"), "cache", false); err != nil {
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
	d, _ := it.MappingDetail()
	c := anime.MappingCandidate{Source: animestore.ManualSource, SeriesID: d.TVDB, Episodes: []anime.SE{{2, 2}, {2, 3}}}
	if err := s.ResolveMapping(ctx, it.ID, c, ""); err != nil {
		t.Fatal(err)
	}
	// the next build proposes the raw conflict mapping again and is held because it unmaps another entry
	next := build(t, "mushoku-tensei")
	var drop *anime.Entry
	for _, e := range next.Entries {
		if e.Key != "al:146065" && e.TVDB != nil && (drop == nil || len(e.TVDB.Episodes) > len(drop.TVDB.Episodes)) {
			drop = e
		}
	}
	drop.TVDB, drop.Mapping = nil, nil
	if saved, err := s.Save(ctx, next, "live", false); err != nil || saved.Applied {
		t.Fatalf("not held: %+v %v", saved, err)
	}
	held, err := s.HeldMappings(ctx, "mushoku-tensei")
	if m := held["al:146065"]; err != nil || m.TVDB != d.TVDB || !reflect.DeepEqual(m.Episodes, c.Episodes) {
		t.Errorf("held mapping of al:146065 = %+v (%v), want the saved choice %v", m, err, c.Episodes)
	}
}

// A refresh proposing what review already decided is not held again; a new proposal for the entry is.
func TestDecidedProposalNotHeldAgain(t *testing.T) {
	ctx := context.Background()
	// open saves mushoku-tensei, finds the al:146065 conflict and links a file to the episode at picks of its series.
	open := func(t *testing.T, pick func(animestore.ItemDetail) anime.SE) (*animestore.Store, *store.Store, *animestore.Item, animestore.ItemDetail) {
		st := openStore(t)
		s := &animestore.Store{DB: st}
		if _, err := s.Save(ctx, build(t, "mushoku-tensei"), "cache", false); err != nil {
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
		d, _ := it.MappingDetail()
		w := st.Writer()
		mustExec(t, w, `INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (1, 'series', 'M', ?, 0, 0)`, d.TVDB)
		mustExec(t, w, `INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (1, 1, ?, ?, 0, 0)`, pick(d)[0], pick(d)[1])
		mustExec(t, w, `INSERT INTO link (id, path, target, title_id, created_at, updated_at) VALUES (1, '/l/a.mkv', '/m/a.mkv', 1, 0, 0)`)
		mustExec(t, w, `INSERT INTO link_episode (link_id, episode_id) VALUES (1, 1)`)
		return s, st, it, d
	}
	mapping := func(t *testing.T, st *store.Store) (status string, eps []anime.SE) {
		var e string
		if err := st.Reader().QueryRow(`SELECT status, episodes FROM anime_mapping
			WHERE franchise = 'mushoku-tensei' AND entry_key = 'al:146065'`).Scan(&status, &e); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(e), &eps)
		return status, eps
	}
	reopened := func(t *testing.T, st *store.Store) int {
		return count(t, st, `SELECT count(*) FROM anime_review WHERE franchise = 'mushoku-tensei' AND state = 'open'
			AND (entry_key = 'al:146065' OR kind LIKE 'refresh_%')`)
	}
	openID := func(t *testing.T, s *animestore.Store, kind string) int64 {
		items, _ := s.Items(ctx, "open", "mushoku-tensei")
		for _, it := range items {
			if it.Kind == kind {
				return it.ID
			}
		}
		t.Fatalf("no open %s item: %+v", kind, items)
		return 0
	}
	chosen := []anime.SE{{2, 2}, {2, 3}}
	onChosen := func(animestore.ItemDetail) anime.SE { return chosen[0] }
	onProposed := func(d animestore.ItemDetail) anime.SE { return d.Episodes[0] }

	t.Run("chosen mapping", func(t *testing.T) {
		s, st, it, d := open(t, onChosen)
		c := anime.MappingCandidate{Source: animestore.ManualSource, SeriesID: d.TVDB, Episodes: chosen}
		if err := s.ResolveMapping(ctx, it.ID, c, ""); err != nil {
			t.Fatal(err)
		}
		saved, err := s.Save(ctx, build(t, "mushoku-tensei"), "live", false)
		if err != nil || !saved.Applied || saved.NewReview != 0 {
			t.Fatalf("same proposal again: %+v %v", saved, err)
		}
		if n := reopened(t, st); n != 0 {
			t.Errorf("open items for the decided entry: %d", n)
		}
		if status, eps := mapping(t, st); status != anime.StatusOverride || !reflect.DeepEqual(eps, chosen) {
			t.Errorf("mapping %s %v, want override %v", status, eps, chosen)
		}

		// the same episodes proposed on another TVDB series are a new proposal: held
		other := build(t, "mushoku-tensei")
		for _, e := range other.Entries {
			if e.Key == "al:146065" {
				e.TVDB.SeriesID = d.TVDB + 1
			}
		}
		saved, err = s.Save(ctx, other, "live", false)
		if err != nil || saved.Applied || saved.NewReview == 0 {
			t.Fatalf("proposal on another series: %+v %v", saved, err)
		}
		if err := s.Resolve(ctx, openID(t, s, "refresh_moves_linked"), false, ""); err != nil {
			t.Fatal(err)
		}

		// a genuinely different proposal for the entry moves the linked episode: held
		next := build(t, "mushoku-tensei")
		for _, e := range next.Entries {
			if e.Key == "al:146065" {
				e.TVDB.Episodes = []anime.SE{{2, 20}, {2, 21}}
			}
		}
		saved, err = s.Save(ctx, next, "live", false)
		if err != nil || saved.Applied || saved.NewReview == 0 {
			t.Fatalf("new proposal: %+v %v", saved, err)
		}
		if n := count(t, st, `SELECT count(*) FROM anime_review WHERE entry_key = 'al:146065' AND kind = 'refresh_moves_linked'
			AND state = 'open'`); n != 1 {
			t.Errorf("refresh_moves_linked items: %d", n)
		}
		if status, eps := mapping(t, st); status != anime.StatusOverride || !reflect.DeepEqual(eps, chosen) {
			t.Errorf("held: mapping %s %v, want override %v", status, eps, chosen)
		}
	})
	t.Run("accepted", func(t *testing.T) {
		s, st, it, _ := open(t, onProposed)
		if err := s.Resolve(ctx, it.ID, true, ""); err != nil {
			t.Fatal(err)
		}
		saved, err := s.Save(ctx, build(t, "mushoku-tensei"), "live", false)
		if err != nil || !saved.Applied || saved.NewReview != 0 || reopened(t, st) != 0 {
			t.Fatalf("same proposal again: %+v %v", saved, err)
		}
		if status, _ := mapping(t, st); status != anime.StatusOverride {
			t.Errorf("status %s, want override", status)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		s, st, it, _ := open(t, onChosen)
		if err := s.Resolve(ctx, it.ID, false, ""); err != nil {
			t.Fatal(err)
		}
		saved, err := s.Save(ctx, build(t, "mushoku-tensei"), "live", false)
		if err != nil || !saved.Applied || saved.NewReview != 0 || reopened(t, st) != 0 {
			t.Fatalf("same proposal again: %+v %v", saved, err)
		}
		if status, _ := mapping(t, st); status != anime.StatusRejected {
			t.Errorf("status %s, want rejected", status)
		}
	})
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

// An accept in review, a build that turns a conflict importable, or an accepted held refresh that moves an importable
// mapping, queues one anime_relink job for the mapping's series; a reject, a first build and a rebuild that changes no
// status queue none.
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
	t.Run("accepted held refresh moves an importable mapping", func(t *testing.T) {
		st := openStore(t)
		s := &animestore.Store{DB: st}
		if _, err := s.Save(ctx, build(t, "re-zero"), "cache", false); err != nil {
			t.Fatal(err)
		}
		const key, series = "al:21355", 305089
		if got := count(t, st, `SELECT count(*) FROM anime_mapping WHERE franchise = 're-zero' AND entry_key = ?
			AND status IN ('auto', 'override', 'heuristic')`, key); got != 1 {
			t.Fatalf("%s is not importable", key)
		}
		w := st.Writer()
		mustExec(t, w, `INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (1, 'series', 'Re:Zero', ?, 0, 0)`, series)
		mustExec(t, w, `INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (1, 1, 1, 1, 0, 0)`)
		mustExec(t, w, `INSERT INTO link (id, path, target, title_id, created_at, updated_at) VALUES (1, '/l/a.mkv', '/m/a.mkv', 1, 0, 0)`)
		mustExec(t, w, `INSERT INTO link_episode (link_id, episode_id) VALUES (1, 1)`)
		moved := build(t, "re-zero")
		for _, e := range moved.Entries {
			if e.Key == key {
				for i := range e.TVDB.Episodes {
					e.TVDB.Episodes[i][1]++
				}
			}
		}
		if saved, err := s.Save(ctx, moved, "live", false); err != nil || saved.Applied {
			t.Fatalf("not held: %+v %v", saved, err)
		}
		if n := all(t, st); n != 0 {
			t.Fatalf("a held refresh queued %d relinks", n)
		}
		items, err := s.Items(ctx, "open", "re-zero")
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		for _, it := range items {
			if it.Kind == "refresh_moves_linked" && it.EntryKey == key {
				id = it.ID
			}
		}
		if id == 0 {
			t.Fatalf("no refresh_moves_linked item: %+v", items)
		}
		if err := s.Resolve(ctx, id, true, ""); err != nil {
			t.Fatal(err)
		}
		if n, m := jobsFor(t, st, series), all(t, st); n != 1 || m != 1 {
			t.Errorf("relinks %d of %d", n, m)
		}
	})
}
