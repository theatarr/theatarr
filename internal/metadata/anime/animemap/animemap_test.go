package animemap_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestLoadAndBlocked(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	for _, q := range []string{
		`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('f', 'F', 1, 0, 0), ('g', 'G', 2, 0, 0)`,
		`INSERT INTO anime_entry (franchise, key, origin, title, body) VALUES
			('f', 'al:1', 'anilist', 'Show', '{"title": "Show", "title_en": "The Show", "names": ["Show", "Shou"]}'),
			('f', 'al:2', 'anilist', 'Show 2', '{"names": ["Show 2"]}')`,
		`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at) VALUES
			('f', 'al:1', 'auto', 77, '[[1,2],[1,1],[0,3]]', '', 0),
			('f', 'al:2', 'conflict', 77, '[[2,1],[2,2]]', 'close call', 0),
			('g', 'al:9', 'auto', 77, '[[2,2]]', '', 0),
			('g', 'al:8', 'auto', 88, '[[1,1]]', '', 0)`,
		`INSERT INTO anime_alt_cut (franchise, name, tvdb_series, episodes) VALUES ('f', 'DC', 77, '{"1": [[1,1],[1,2]]}')`,
	} {
		if _, err := w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := animemap.Load(ctx, st.Reader(), 77)
	if err != nil || s == nil {
		t.Fatal(s, err)
	}
	if len(s.Entries) != 3 || len(s.AltCuts) != 1 || len(s.AltCuts[0].Episodes[1]) != 2 {
		t.Fatalf("%+v", s)
	}
	e1 := s.Entries[0]
	if e1.Key != "al:1" || len(e1.Names) != 3 || e1.Episodes[0][0] != 0 || len(e1.Regular()) != 2 || e1.Regular()[0][1] != 1 {
		t.Errorf("entry %+v", e1)
	}
	if e, b := s.Blocked(2, 1); !b || e.Key != "al:2" {
		t.Error("S02E01 is only in a conflict")
	}
	if _, b := s.Blocked(2, 2); b {
		t.Error("S02E02 is also in an auto range (another franchise)")
	}
	if _, b := s.Blocked(5, 1); b {
		t.Error("an unmapped episode is not blocked")
	}
	if !s.Named("show", "x", strings.ToLower) || s.Named("other", "x", strings.ToLower) || !s.Named("other", "Show DC 01", strings.ToLower) {
		t.Error("named")
	}
	if s, err := animemap.Load(ctx, st.Reader(), 99); s != nil || err != nil {
		t.Errorf("unmapped series: %v %v", s, err)
	}
	for st, want := range map[string]bool{"auto": true, "arbitrated": true, "override": true, "heuristic": true, "conflict": false, "rejected": false} {
		if animemap.Importable(st) != want {
			t.Errorf("%s", st)
		}
	}
}
