package decide

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func linkedDB(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLoadExistingLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := linkedDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (7, 'movie', 'Dune: Part Two', 0, 0)`)
	exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 7, 'movie-remux', 0, 0)`)
	exec(`INSERT INTO version_source VALUES (1, 'owner', 1)`)
	exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, scene_name,
		added_at) VALUES (1, 1, 7, 'owner', 1, '/arr.mkv', 'WEBDL-2160p', 'arr scene', 1),
		(2, 1, 7, 'other', 2, '/other.mkv', 'Remux-2160p', 'other scene', 999)`)
	arr, err := LoadExisting(ctx, st.Reader(), 1)
	if err != nil || !reflect.DeepEqual(arr, &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "arr scene", Languages: []string{}, Added: time.UnixMilli(1).UTC()}) {
		t.Fatalf("arr-only owner precedence: %+v, %v", arr, err)
	}
	low := rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb)
	f := LinkedFile(policy.KindMovie, low, "WEBDL-2160p")
	f.Formats, f.Score = []FormatScore{{Name: "saved format", Score: 99}}, new(99)
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO link (id, title_id, path, target, release_title, quality, file_metadata, created_at, updated_at)
		VALUES (1, 7, '/renamed.mkv', '/mount/file.mkv', ?, 'WEBDL-2160p', ?, 5, 5)`, low.Title, string(body))
	exec(`INSERT INTO link_version VALUES (1, 1)`)
	got, err := LoadExisting(ctx, st.Reader(), 1)
	if err != nil || got == nil || got.SceneName != low.Title || got.Size != low.Size || got.ReleaseGroup != "FLUX" ||
		got.Score == nil || *got.Score != 99 || !reflect.DeepEqual(got.Formats, f.Formats) {
		t.Fatalf("linked metadata: %+v, %v", got, err)
	}
	e := testEngine(t)
	high := rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb)
	d, err := e.Decide(ctx, high, dune, Version{Key: "movie-remux"}, got)
	if err != nil || !d.Upgrade || d.Existing == nil || d.Existing.Quality != f.Quality || d.Existing.Score != 1700 ||
		d.UpgradeReason != "better quality: Remux-2160p over WEBDL-2160p" {
		t.Fatalf("upgrade from link: %+v, %v", d, err)
	}
	// An active link wins even over a newer arr file; dead and unlinked rows never hide an arr file.
	for _, state := range []string{"dead", "unlinked"} {
		exec(`UPDATE link SET state = ? WHERE id = 1`, state)
		got, err = LoadExisting(ctx, st.Reader(), 1)
		if err != nil || !reflect.DeepEqual(got, arr) {
			t.Fatalf("%s link changed arr loading: %+v, %v", state, got, err)
		}
	}
	exec(`DELETE FROM version_file`)
	exec(`UPDATE link SET state = 'active' WHERE id = 1`)
	got, err = LoadExisting(ctx, st.Reader(), 1)
	if err != nil || got == nil || got.SceneName != low.Title {
		t.Fatalf("theatarr-only file: %+v, %v", got, err)
	}
	// A better existing link must prevent the worse release from looking like a fill.
	body, _ = json.Marshal(LinkedFile(policy.KindMovie, high, "Remux-2160p"))
	exec(`UPDATE link SET file_metadata = ? WHERE id = 1`, string(body))
	got, err = LoadExisting(ctx, st.Reader(), 1)
	if err != nil {
		t.Fatal(err)
	}
	d, err = e.Decide(ctx, low, dune, Version{Key: "movie-remux"}, got)
	if err != nil || d.Accepted || d.Existing == nil || d.Existing.Quality != "Remux-2160p" || reasons(d) != "DiskHigherPreference" {
		t.Fatalf("downgrade from link: %+v, %v", d, err)
	}
}

func TestLoadEpisodeLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := linkedDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'series', 'The Show', 0, 0)`)
	exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (9, 1, 'tv', 0, 0), (10, 1, 'other', 0, 0)`)
	for _, ep := range show().Episodes {
		exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (?, 1, ?, ?, ?, 0, 0)`,
			ep.ID, ep.Season, ep.Number, ep.Title)
	}
	exec(`INSERT INTO version_source VALUES (9, 'owner', 1)`)
	exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, scene_name,
		added_at) VALUES (1, 9, 1, 'owner', 1, '/arr.mkv', 'WEBDL-2160p', 'owner', 1),
		(2, 9, 1, 'other', 2, '/other.mkv', 'Bluray-2160p Remux', 'other', 999)`)
	exec(`INSERT INTO version_file_episode VALUES (1, 101), (1, 103), (2, 101)`)
	before, err := LoadEpisodes(ctx, st.Reader(), 1, 9)
	if err != nil || before[1].FileID != 1 || before[3].FileID != 1 {
		t.Fatalf("arr-only episodes: %+v, %v", before, err)
	}
	f := LinkedFile(policy.KindSeries, rel("The.Show.S01E01E02.2160p.WEB-DL-GRP", "public", 16*gb), "WEBDL-2160p")
	body, _ := json.Marshal(f)
	exec(`INSERT INTO link (id, title_id, path, target, file_metadata, created_at, updated_at)
		VALUES (1, 1, '/renamed.mkv', '/mount/file.mkv', ?, 5, 5)`, string(body))
	exec(`INSERT INTO link_version VALUES (1, 9)`)
	exec(`INSERT INTO link_episode VALUES (1, 101), (1, 102)`)
	got, err := LoadEpisodes(ctx, st.Reader(), 1, 9)
	if err != nil {
		t.Fatal(err)
	}
	if got[1].FileID != -1 || got[2].File != got[1].File || len(got[1].File.EpisodeIDs) != 2 ||
		got[1].File.ReleaseType != "multiEpisode" || !reflect.DeepEqual(got[3], before[3]) {
		t.Fatalf("mixed episode files: %+v", got)
	}
	title := show()
	for i := range title.Episodes {
		title.Episodes[i].File, title.Episodes[i].FileID = got[i].File, got[i].FileID
	}
	e := seriesEngine(t)
	d, err := e.Decide(ctx, rel("The.Show.S01E01E02.2160p.BluRay.REMUX.HDR-GRP", "public", 40*gb), title, tvVersion(e, "tv"), nil)
	if err != nil || !d.Upgrade || d.Existing == nil || d.Existing.Quality != "WEBDL-2160p" || d.Missing != 0 || d.UpgradeReason == "" {
		t.Fatalf("episode upgrade from link: %+v, %v", d, err)
	}
	d, err = e.Decide(ctx, rel("The.Show.S01E01.2160p.BluRay.REMUX.HDR-GRP", "public", 20*gb), title, tvVersion(e, "tv"), nil)
	if err != nil || reasons(d) != "ExistingFileHasMoreEpisodes" {
		t.Fatalf("multi-episode link split: %+v, %v", d, err)
	}
	other, err := LoadEpisodes(ctx, st.Reader(), 1, 10)
	if err != nil || other[1].File != nil || other[2].File != nil {
		t.Fatalf("link leaked to another version: %+v, %v", other, err)
	}
	exec(`UPDATE link SET state = 'dead'`)
	got, err = LoadEpisodes(ctx, st.Reader(), 1, 9)
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("inactive links changed shadow files: %+v, %v", got, err)
	}
}

func TestLoadBackfilledLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := linkedDB(t)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie', 0, 0)`,
		`INSERT INTO link (id, title_id, path, target, file_metadata, created_at, updated_at) VALUES (1, 1,
		'/renamed.mkv', '/mount/file.mkv', '{"SceneName":"Movie.2020.REPACK.1080p.WEB-DL-GRP", "Quality":"WEBDL-1080p",
		"Size":1000, "Score":50, "Formats":[{"name":"French","score":50}], "Languages":["French"], "flags":["internal"]}', 0, 0)`,
		`INSERT INTO link_version VALUES (1, 1)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	f, err := LoadExisting(ctx, st.Reader(), 1)
	if err != nil || f == nil || f.Quality != "WEBDL-1080p" || f.Revision != 2 || f.ReleaseGroup != "GRP" ||
		f.Size != 1000 || f.Score == nil || *f.Score != 50 || !reflect.DeepEqual(f.Languages, []string{"French"}) ||
		f.IndexerFlags == 0 || len(f.Formats) != 1 || f.Formats[0].Name != "French" {
		t.Fatalf("legacy metadata: %+v, %v", f, err)
	}
}
