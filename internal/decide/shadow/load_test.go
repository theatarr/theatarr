package shadow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// A small history: import, grab (upgrade over the file), delete, grab (no file), and a grab for a movie we do not
// hold. The existing file before each grab is rebuilt from the events.
func TestLoadDB(t *testing.T) {
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
		`INSERT INTO title (id, kind, name, year, tmdb_id, imdb_id, created_at, updated_at) VALUES (1, 'movie', 'Dune: Part Two', 2024, 693134, 'tt15239678', 0, 0)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('radarr_4k_debrid', 'radarr', 'movie-remux', 0, 0)`,
		`INSERT INTO arr_history (instance, arr_id, event_type, date_at, arr_title_id, title_id, protocol, source_title, quality, languages, data) VALUES
		 ('radarr_4k_debrid', 1, 'downloadFolderImported', 1000, 5, 1, '', 'Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX', 'WEBDL-2160p', '["English"]',
		  '{"releaseGroup":"FLUX","size":"26843545600","indexerFlags":"G_Freeleech","importedPath":"/m/Dune Part Two (2024)/Dune Part Two (2024) [WEBDL-2160p]-FLUX.mkv"}'),
		 ('radarr_4k_debrid', 2, 'grabbed', 2000, 5, 1, 'torrent', 'Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR', 'Remux-2160p', '["English"]',
		  '{"customFormatScore":"2450","size":"64424509440","indexerFlags":"0","imdbId":"15239678","movieMatchType":"Id","releaseSource":"Rss","indexer":"TheRARBG (Prowlarr)"}'),
		 ('radarr_4k_debrid', 3, 'movieFileDeleted', 3000, 5, 1, '', '/m/x.mkv', 'WEBDL-2160p', '[]', '{"reason":"MissingFromDisk"}'),
		 ('radarr_4k_debrid', 4, 'grabbed', 4000, 5, 1, 'usenet', 'Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX', 'WEBDL-2160p', '["English"]',
		  '{"customFormatScore":"1700","size":"26843545600","indexerFlags":"G_Freeleech","releaseSource":"UserInvokedSearch"}'),
		 ('radarr_4k_debrid', 5, 'grabbed', 5000, 9, NULL, 'torrent', 'Other.2024.2160p.WEB-DL-FLUX', 'WEBDL-2160p', '[]', '{}')`,
	} {
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	grabs, err := LoadDB(ctx, st.Reader(), "radarr_4k_debrid", map[int][]string{693134: {"Dune 2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(grabs) != 2 {
		t.Fatalf("%d grabs, want 2 (the unheld movie is left out)", len(grabs))
	}
	g := grabs[0]
	if g.Existing == nil || g.Existing.Quality != "WEBDL-2160p" || g.Existing.ReleaseGroup != "FLUX" ||
		g.Existing.IndexerFlags != 1 || g.Existing.RelativePath != "Dune Part Two (2024) [WEBDL-2160p]-FLUX.mkv" {
		t.Errorf("existing before the first grab: %+v", g.Existing)
	}
	if g.GrabScore == nil || *g.GrabScore != 2450 || g.Release.IMDb != "tt15239678" || g.Release.Usenet ||
		g.MatchType != "Id" || g.Title.Aliases[0] != "Dune 2" || g.Release.Size != 64424509440 {
		t.Errorf("first grab: %+v", g)
	}
	if g2 := grabs[1]; g2.Existing != nil || !g2.Release.Usenet || *g2.Release.IndexerFlags != 1 {
		t.Errorf("second grab: existing %+v usenet %v", g2.Existing, g2.Release.Usenet)
	}
}

func TestLoadGrab(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, tmdb_id, imdb_id, created_at, updated_at) VALUES (1, 'movie', 'Dune: Part Two', 2024, 693134, 'tt15239678', 0, 0)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('radarr_4k_debrid', 'radarr', 'movie-remux', 0, 0)`,
		`INSERT INTO arr_history (instance, arr_id, event_type, date_at, arr_title_id, title_id, protocol, source_title, quality, languages, data) VALUES
		 ('radarr_4k_debrid', 1, 'downloadFolderImported', 1000, 5, 1, '', 'Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX', 'WEBDL-2160p', '["English"]', '{"releaseGroup":"FLUX"}'),
		 ('radarr_4k_debrid', 2, 'grabbed', 2000, 5, 1, 'torrent', 'Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR', 'Remux-2160p', '[]',
		  '{"customFormatScore":"2450","torrentInfoHash":"ABCDEF"}'),
		 ('radarr_4k_debrid', 3, 'downloadFolderImported', 2500, 5, 1, '', 'Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR', 'Remux-2160p', '[]', '{}')`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	titles, err := decide.LoadMovies(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	g, err := LoadGrab(ctx, st.Reader(), "radarr_4k_debrid", 2, titles)
	if err != nil {
		t.Fatal(err)
	}
	if g.HistoryID != 2 || g.Existing == nil || g.Existing.Quality != "WEBDL-2160p" || g.InfoHash != "ABCDEF" ||
		*g.GrabScore != 2450 {
		t.Errorf("grab %+v existing %+v", g, g.Existing)
	}
	if _, err := LoadGrab(ctx, st.Reader(), "radarr_4k_debrid", 3, titles); err == nil {
		t.Error("an import event is not a grab")
	}
}

// Series history: a two-episode pack grabbed (two rows a second apart) and imported into one file, then a single
// episode grabbed over that file. Grabs group per release; files are rebuilt per episode with their episode sets.
func TestLoadSeriesDB(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, runtime_minutes, created_at, updated_at)
			VALUES (1, 'series', 'The Show', 2020, 1001, 'standard', 45, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (2, 1, 'tv-2160p', 0, 0)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('sonarr_a', 'sonarr', 'tv-2160p', 0, 0)`,
		`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, quality_profile_id, series_type)
			VALUES ('sonarr_a', 7, 'series', 1, 2, 1, 7, 'standard')`,
		`INSERT INTO version_source (version_id, instance, arr_id) VALUES (2, 'sonarr_a', 7)`,
		`INSERT INTO episode (id, title_id, season, number, name, air_date, monitored, created_at, updated_at) VALUES
			(11, 1, 1, 1, 'A', '2020-04-01', 1, 0, 0), (12, 1, 1, 2, 'B', '2020-04-08', 1, 0, 0)`,
		`INSERT INTO arr_alias (instance, kind, arr_id, target_id, created_at) VALUES
			('sonarr_a', 'episode', 101, 11, 0), ('sonarr_a', 'episode', 102, 12, 0)`,
		`INSERT INTO arr_history (instance, arr_id, event_type, date_at, arr_title_id, arr_episode_id, title_id, download_id,
			protocol, source_title, quality, custom_format_score, languages, data) VALUES
		 ('sonarr_a', 1, 'grabbed', 1000, 7, 101, 1, 'H1', 'torrent', 'The.Show.S01.2160p.WEB-DL-GRP', 'WEBDL-2160p', 10, '[]',
		  '{"customFormatScore":"10","releaseSource":"Search","releaseType":"SeasonPack","tvdbId":"1001","indexerFlags":"Freeleech"}'),
		 ('sonarr_a', 2, 'grabbed', 1900, 7, 102, 1, 'H1', 'torrent', 'The.Show.S01.2160p.WEB-DL-GRP', 'WEBDL-2160p', 10, '[]',
		  '{"customFormatScore":"10","releaseSource":"Search","releaseType":"SeasonPack","tvdbId":"1001"}'),
		 ('sonarr_a', 3, 'downloadFolderImported', 5000, 7, 101, 1, 'H1', '', 'The.Show.S01.2160p.WEB-DL-GRP', 'WEBDL-2160p', NULL, '[]',
		  '{"fileId":"55","releaseType":"SeasonPack","importedPath":"/s/Season 01/x.mkv"}'),
		 ('sonarr_a', 4, 'downloadFolderImported', 5000, 7, 102, 1, 'H1', '', 'The.Show.S01.2160p.WEB-DL-GRP', 'WEBDL-2160p', NULL, '[]',
		  '{"fileId":"55","releaseType":"SeasonPack"}'),
		 ('sonarr_a', 5, 'grabbed', 9000, 7, 101, 1, 'H2', 'torrent', 'The.Show.S01E01.2160p.BluRay-GRP', 'Bluray-2160p', 0, '[]',
		  '{"releaseSource":"Rss"}')`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	grabs, err := LoadSeriesDB(ctx, st.Reader(), "sonarr_a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(grabs) != 2 {
		t.Fatalf("%d grabs, want 2", len(grabs))
	}
	g := grabs[0]
	if len(g.HistoryIDs) != 2 || len(g.Episodes) != 2 || g.Search == nil || g.Search.Kind != "season" ||
		g.Release.TVDB != 1001 || *g.Release.IndexerFlags != 1 || g.ArrProfileID != 7 {
		t.Errorf("pack grab: %+v search %+v", g, g.Search)
	}
	for _, e := range g.Title.Episodes {
		if e.File != nil {
			t.Errorf("pack grab: episode %d has a file before the import", e.ID)
		}
	}
	g = grabs[1]
	if g.Search != nil || len(g.Episodes) != 1 || g.Episodes[0] != 11 {
		t.Errorf("single grab: %+v", g)
	}
	f := g.Title.Episodes[0].File
	if f == nil || g.Title.Episodes[0].FileID != 55 || len(f.EpisodeIDs) != 2 || f.ReleaseType != "seasonPack" ||
		f.RelativePath != "x.mkv" {
		t.Fatalf("file before the single grab: %+v", f)
	}
}
