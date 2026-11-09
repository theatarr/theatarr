package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestImportSetsMovieStateFromCutoff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, quality, want string
	}{{"below cutoff", "Amelie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP", "filled"}, {"at cutoff", movieRelease, "cutoff_met"}} {
		t.Run(tc.name, func(t *testing.T) {
			// The file's own quality (from its release and probe) is judged, not the download's.
			e := newEnv(t, true, policy.ModeActive)
			e.release(tc.quality, map[string]float64{"movie.mkv": 7000})
			if tc.want == "filled" {
				e.videoSize(tc.quality, 1920, 1080)
			}
			e.download(1, "movie-remux", 1, 10, tc.quality, tc.quality, "[]")
			if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
				t.Fatal(err)
			}
			if got := e.one(`SELECT state FROM version WHERE id = 10`); got != tc.want {
				t.Fatalf("version state=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestPartialEpisodeImportState(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'WEBDL-1080p' WHERE id = 1`)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil || len(plan.Links) != 2 {
		t.Fatalf("dry-run plan %+v, err %v", plan, err)
	}
	blocker := plan.Links[1].Path
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); err == nil {
		t.Fatal("partial import reported no error")
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "filled" {
		t.Fatalf("imported episode state=%s, want filled", got)
	}
	if got := e.one(`SELECT COALESCE((SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 202), 'wanted')`); got != "wanted" {
		t.Fatalf("failed episode state=%s, want wanted", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 20`); got != "filled" {
		t.Fatalf("series state=%s, want filled", got)
	}
	e.exec(`UPDATE episode_version_state SET state = 'cutoff_met', cutoff_met = 1 WHERE version_id = 20 AND episode_id = 201`)
	if _, err := e.lib.Import(e.ctx, 1, false); err == nil {
		t.Fatal("retry with blocked episode was not partial")
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "cutoff_met" {
		t.Fatalf("partial retry moved episode backwards to %s", got)
	}
	var linkID int64
	var path string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT l.id, l.path FROM link l JOIN link_episode le ON le.link_id = l.id
		WHERE le.episode_id = 201 AND l.state = 'active'`).Scan(&linkID, &path); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.shows, "Show (2020)", "Season 01", "duplicate.mkv")
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (99, ?, '/elsewhere/duplicate.mkv', 2, 'active', 0, 0)`, other)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (99, 20)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (99, 201)`)
	if _, err := e.lib.Unlink(e.ctx, "tv", path, linkID, "test"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "cutoff_met" {
		t.Fatalf("episode reset with another active file: %s", got)
	}
	if _, err := e.lib.Unlink(e.ctx, "tv", other, 99, "test"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "wanted" {
		t.Fatalf("episode state without files=%s, want wanted", got)
	}
}

func TestUnlinkResetsOnlyWhenNoFileRemains(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("import plan %+v, err %v", plan, err)
	}
	var linkID int64
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM link WHERE path = ?`, plan.Links[0].Path).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.movies, "Amelie The Movie (2001)", "other.mkv")
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (99, ?, '/elsewhere/other.mkv', 1, 'active', 0, 0)`, other)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (99, 10)`)
	if _, err := e.lib.Unlink(e.ctx, "movie-remux", plan.Links[0].Path, linkID, "test"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "cutoff_met" {
		t.Fatalf("state with another active file=%s, want cutoff_met", got)
	}
	if _, err := e.lib.Unlink(e.ctx, "movie-remux", other, 99, "test"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "wanted" {
		t.Fatalf("state without files=%s, want wanted", got)
	}
}

func TestShadowImportLeavesVersionStateAlone(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeShadow)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (20, 201, 1, 'wanted', 0)`)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || !plan.DryRun {
		t.Fatalf("shadow plan %+v, err %v", plan, err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "filled" {
		t.Fatalf("shadow version state=%s, want unchanged filled", got)
	}
	rel := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700})
	e.seriesDownload(2, 2, 20, strings.Repeat("b", 40), rel, "[201]", nil)
	plan, err = e.lib.Import(e.ctx, 2, false)
	if err != nil || !plan.DryRun {
		t.Fatalf("shadow series plan %+v, err %v", plan, err)
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "wanted" {
		t.Fatalf("shadow episode state=%s, want unchanged wanted", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 20`); got != "wanted" {
		t.Fatalf("shadow series state=%s, want unchanged wanted", got)
	}
}

func TestImportRequiresBuiltProfileBeforeWriting(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.lib.engine = nil
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err == nil || !strings.Contains(err.Error(), "decision engine is not configured") {
		t.Fatalf("missing profile error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(e.movies, "Amelie The Movie (2001)")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import wrote before checking its profile: %v", err)
	}
}
