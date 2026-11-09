package tvrefresh

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestSceneMappingsRefresh(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	_, err := e.st.Writer().ExecContext(ctx, `INSERT INTO title_scene_mapping
		(title_id, source, name, season_number, scene_season_number, scene_origin, comment) VALUES
		(?, 'sonarr', 'IMPORTED.ALIAS', NULL, NULL, 'tvdb', ''),
		(?, 'sonarr', 'Season.Alias', 2, 1, 'mixed', 'Keep this')`, id, id)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Status string      `json:"status"`
		Data   tvdb.Series `json:"data"`
	}
	path := "/series/371980/extended"
	if err := json.Unmarshal(e.tvdb.fix[path], &record); err != nil {
		t.Fatal(err)
	}
	setNames := func(names []string, translations *tvdb.Translations) {
		t.Helper()
		record.Data.Aliases = nil
		for _, name := range names {
			record.Data.Aliases = append(record.Data.Aliases, tvdb.Alias{Name: name})
		}
		record.Data.NameTranslations = []string{"eng", "jpn"} // language codes, not titles
		record.Data.Translations = translations
		b, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		e.tvdb.fix[path] = b
	}
	other := e.add(t, catalog.Input{TmdbID: 1, Title: "Other Show"})
	if _, err := e.st.Writer().ExecContext(ctx, `INSERT INTO title_scene_mapping (title_id, source, name, scene_origin)
		VALUES (?, 'sonarr', 'Other Alias', 'tvdb')`, other); err != nil {
		t.Fatal(err)
	}
	setNames([]string{" Alias: Tale ", "Alias.Tale", "", "---", "3%", "3 Percent", "Imported Alias", "Season Alias",
		"Other Show", "Other.Alias"},
		&tvdb.Translations{NameTranslations: []tvdb.Translation{
			{Name: "Translated Name", Aliases: []string{"Nested Alias", "Alias Tale"}},
			{Language: "spa", Name: "Spanish Name", Aliases: []string{"Spanish Alias"}},
		}})
	if sum, err := e.svc.Sweep(ctx); err != nil || sum.Refreshed != 1 {
		t.Fatalf("sweep: %+v, %v", sum, err)
	}
	check := func(want []string) {
		t.Helper()
		rows, err := e.st.Reader().QueryContext(ctx, `SELECT name FROM title_scene_mapping
			WHERE title_id = ? AND source = ? ORDER BY name`, id, sceneSource)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("refresh names: %v, want %v", got, want)
		}
		if n := e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id = ?`, id); n != len(want)+2 {
			t.Errorf("%d mappings, want %d (including imports)", n, len(want)+2)
		}
		if n := e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id = ? AND source = ?
			AND season_number IS NULL AND scene_season_number IS NULL AND scene_origin = 'tvdb'`, id, sceneSource); n != len(want) {
			t.Errorf("%d all-season TVDB mappings, want %d", n, len(want))
		}
		if n := e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id = ? AND source = 'sonarr'
			AND name = 'Season.Alias' AND season_number = 2 AND scene_season_number = 1
			AND scene_origin = 'mixed' AND comment = 'Keep this'`, id); n != 1 {
			t.Error("imported season mapping changed")
		}
	}
	want := []string{"3%", "Alias: Tale", "Nested Alias", "Translated Name"}
	check(want)
	if _, err := e.svc.RefreshTitle(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	check(want) // repeated refreshes neither duplicate nor accumulate names
	setNames([]string{"New Alias"}, nil)
	if _, err := e.svc.RefreshTitle(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	want = []string{"New Alias"}
	check(want)
	if _, err := e.st.Writer().ExecContext(ctx, `CREATE TRIGGER fail_refresh BEFORE UPDATE ON series_meta
		BEGIN SELECT RAISE(ABORT, 'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	setNames([]string{"Uncommitted Alias"}, nil)
	if _, err := e.svc.RefreshTitle(ctx, id, true); err == nil {
		t.Fatal("broken store succeeded")
	}
	check(want) // aliases roll back with the rest of the series refresh
	if _, err := e.st.Writer().ExecContext(ctx, `DROP TRIGGER fail_refresh`); err != nil {
		t.Fatal(err)
	}
	e.tvdb.fix[path] = []byte(`{"status":"success","data":`)
	if _, err := e.svc.RefreshTitle(ctx, id, true); err == nil {
		t.Fatal("broken TVDB response succeeded")
	}
	check(want) // a failed fetch keeps the last successful mappings
	setNames(nil, nil)
	if _, err := e.svc.RefreshTitle(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	check(nil)
}

// Exercise decide through its store loaders here.
func TestDecideRefreshAlias(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	anime := true
	id := e.add(t, catalog.Input{TvdbID: 424536, Title: "Frieren: Beyond Journey's End", Anime: &anime,
		Versions: func(bool) ([]string, error) { return []string{"anime-tv"}, nil }})
	e.refresh(t, id)
	ti, err := decide.LoadTitle(ctx, e.st.Reader(), id)
	if err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = 'anime-tv'`,
		id).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(ctx, e.st.Reader(), &ti, versionID); err != nil {
		t.Fatal(err)
	}
	if len(ti.SceneMappings) == 0 || e.count(t, `SELECT COUNT(*) FROM title_scene_mapping
		WHERE title_id = ? AND source <> ?`, id, sceneSource) != 0 {
		t.Fatal("expected only refresh-written scene mappings")
	}
	pol, err := policy.Load(filepath.Join("..", "..", "decide", "testdata", "series"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := decide.New(pol, decide.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	v := decide.Version{ID: versionID, Key: "anime-tv", Policy: pol.Versions["anime-tv"]}
	for _, release := range []string{
		"[SubsPlease] Sousou no Frieren - 01 (1080p) [ABCD1234].mkv",
		"[Group] Frieren of the Funeral - 01 (1080p)",
	} {
		rel := decide.Release{Release: indexer.Release{Title: release, Indexer: "public", Size: 1 << 30, Seeders: 20}}
		d, err := engine.Decide(ctx, rel, ti, v, nil) // no indexer TVDB id
		if err != nil {
			t.Fatal(err)
		}
		if !d.Accepted || d.MatchType != decide.MatchAlias || !reflect.DeepEqual(d.Episodes, []string{"S01E01"}) {
			t.Errorf("%s: %+v", release, d)
		}
		without := ti
		without.SceneMappings = nil
		d, err = engine.Decide(ctx, rel, without, v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Accepted || len(d.Rejections) != 1 || d.Rejections[0].Reason != "UnknownSeries" {
			t.Errorf("without mappings: %+v", d)
		}
	}
}
