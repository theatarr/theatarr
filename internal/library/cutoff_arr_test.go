package library

import (
	"testing"
)

// An adopted arr file recorded cutoff_met is judged again under the current profile, at the first pass
// and after a settings change, so the cutoff sweep sees it once the profile's cutoff rises.
func TestRecomputeJudgesArrFilesAfterProfileChange(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.adoptedFile(900, 10, 1, "/arr/movie.mkv", "/arr/movie.mkv", "Remux-2160p", movieRelease)
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
