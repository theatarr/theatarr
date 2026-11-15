package arrview

import (
	"strconv"
	"testing"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// A bulk file delete checks every row before the first unlink, as a title delete does. Before, a
// selection of an open version's link followed by a gated version's link unlinked the first and answered 409.
func TestBulkDeleteFilesPreflightsEveryRow(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedFiles()
	active := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`)
	// movie-web may write; movie-remux is not listed under library.versions (the gate refuses it).
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-web": {Roots: []string{"/media/movies-web"}}}}
	e.lib.gate = library.NewGate(e.pol)
	rec := e.do("DELETE", "/radarr/api/v3/moviefile/bulk", e.key,
		map[string]any{"movieFileIds": []int{LinkFileBase + 7, LinkFileBase + 1}})
	if rec.Code != 409 {
		t.Errorf("bulk delete with a gated row: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.lib.unlinks) != 0 || e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`) != active {
		t.Errorf("a refused bulk delete unlinked %v", e.lib.unlinks)
	}
	if len(e.events.events) != 0 {
		t.Errorf("events %+v", e.events.events)
	}
}

// A link shared by two versions of a view is a row per version, each checked before anything is
// unlinked. Before, the rows collapsed to one per file id: the first version's check passed and the link went,
// although the other version may not write there.
func TestDeleteSharedLinkChecksEveryVersion(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedFiles()
	p := "/media/movies/Alpha (2020)/Alpha (2020) WEBDL-2160p.mkv"
	e.exec(`UPDATE link SET path = ? WHERE id = 7`, p)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 1)`)
	e.pol.Versions["movie-remux"].Mode = policy.ModeActive
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-remux": {Roots: []string{"/media/movies"}},
		"movie-web":   {Roots: []string{"/media/movies-web"}}}}
	e.lib.gate = library.NewGate(e.pol)
	rec := e.do("DELETE", "/radarr/api/v3/moviefile/"+strconv.Itoa(LinkFileBase+7), e.key, nil)
	if rec.Code != 409 || len(e.lib.unlinks) != 0 || len(e.events.events) != 0 {
		t.Errorf("shared link, one version refused: %d %s; unlinks %v", rec.Code, rec.Body.String(), e.lib.unlinks)
	}
}
