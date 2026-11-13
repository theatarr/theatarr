package library

import (
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// An imported arr file recorded cutoff_met is judged again under the current profile, at the first pass
// and after a settings change, so the cutoff sweep sees it once the profile's cutoff rises. A version an arr still
// manages keeps the arr's verdict.
func TestRecomputeJudgesArrFilesAfterProfileChange(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO version_file (id, instance, arr_file_id, version_id, title_id, path, relative_path, size_bytes,
		quality, quality_revision, release_group, scene_name, languages, media_info)
		VALUES (900, 'radarr_old', 900, 10, 1, '/arr/movie.mkv', 'movie.mkv', 1000, 'Remux-2160p', 1, 'GRP', ?,
		'["English"]', '{}')`, movieRelease)
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE id = 10`)
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "cutoff_met" {
		t.Fatalf("before the change: %s", got)
	}
	// The profile gains a format cutoff the file does not reach: a new policy revision.
	e.scoreFormat(100, 10000)
	e.pol.Revision = "raised"
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "filled" {
		t.Fatalf("arr file below the raised cutoff stays %s, want filled", got)
	}
}
