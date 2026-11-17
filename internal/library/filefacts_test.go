package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// TestEditFileFacts: a file stored as WEBDL-1080p corrected to Bluray-1080p Proper meets the HD library's cutoff now
// (it leaves that library's Cutoff unmet), while the same link in the 4K library is judged with the 4K profile and
// stays below it. The formats are calculated again (none match: stored as an empty list), a re-probe afterwards
// leaves the corrected facts, and a link no longer active is refused.
func TestEditFileFacts(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.pol.Profiles["movie-hd"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "Bluray-1080p"}, {Name: "WEBDL-1080p"}},
		Cutoff: "Bluray-1080p"}
	e.pol.Versions["movie-hd"] = &policy.VersionPolicy{Kind: policy.KindMovie, Profile: "movie-hd", Library: e.movies}
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.engine = engine
	e.release("Rel", map[string]float64{"movie.mkv": 7200})
	e.exec(`INSERT INTO content (id, info_hash, first_seen_at, last_seen_at) VALUES (1, ?, 0, 0)`, hashA)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe, probed_at) VALUES (1, 1, 'Rel/movie.mkv', 5,
		'{"container":"matroska","video":{"codec":"hevc","bit_depth":8,"dovi_compat":-1}}', 1)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, below_quality_cutoff, created_at, updated_at)
		VALUES (11, 1, 'movie-hd', 'filled', 1, 0, 0)`)
	e.exec(`UPDATE version SET state = 'filled', below_quality_cutoff = 1 WHERE id = 10`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, score, file_metadata,
		state, created_at, updated_at) VALUES (1, ?, ?, 1, 1, 'Amelie.2001.1080p.WEB-DL-GRP', 'WEBDL-1080p', 5,
		'{"Formats":[{"Name":"Old","Score":5}],"Score":5,"Quality":"WEBDL-1080p","Revision":1,"SceneName":"Amelie.2001.1080p.WEB-DL-GRP","Languages":["English"],"ReleaseGroup":"GRP","Size":5}',
		'active', 0, 0)`, filepath.Join(e.movies, "Amelie (2001)", "movie.mkv"), filepath.Join(e.mount, "__all__", "Rel", "movie.mkv"))
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10), (1, 11)`)

	res, err := e.lib.EditFileFacts(e.ctx, 1, FileFactsEdit{Quality: "bluray-1080p", Revision: 2, Languages: []string{"French"},
		ReleaseGroup: "NEW"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Versions) != 2 || res.Versions[0] != "movie-remux" || res.Versions[1] != "movie-hd" || len(res.Scores) != 2 ||
		res.Before.Quality != "WEBDL-1080p" || res.After.Quality != "Bluray-1080p" {
		t.Fatalf("result %+v", res)
	}
	if got := e.one(`SELECT state || ' ' || below_quality_cutoff FROM version WHERE id = 11`); got != "cutoff_met 0" {
		t.Errorf("HD library: %s", got)
	}
	if got := e.one(`SELECT state || ' ' || below_quality_cutoff FROM version WHERE id = 10`); got != "filled 1" {
		t.Errorf("4K library: %s", got)
	}
	stored := e.one(`SELECT file_metadata FROM link WHERE id = 1`)
	if got := e.one(`SELECT quality || ' ' || score || ' ' || json_type(file_metadata, '$.Formats') || ' ' ||
		json_array_length(file_metadata, '$.Formats') || ' ' || json_extract(file_metadata, '$.Quality') || ' ' ||
		json_extract(file_metadata, '$.Revision') || ' ' || json_extract(file_metadata, '$.Languages') || ' ' ||
		json_extract(file_metadata, '$.ReleaseGroup') FROM link WHERE id = 1`); got != `Bluray-1080p 0 array 0 Bluray-1080p 2 ["French"] NEW` {
		t.Errorf("stored facts: %s", got)
	}

	// A re-probe writes only the probe.
	if err := e.lib.HandleReprobe(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT json_extract(probe, '$.v') FROM content_file WHERE id = 1`); got != "2" {
		t.Errorf("not re-probed: %s", got)
	}
	if got := e.one(`SELECT file_metadata FROM link WHERE id = 1`); got != stored {
		t.Errorf("re-probe changed the facts: %s", got)
	}

	e.exec(`UPDATE link SET state = 'dead' WHERE id = 1`)
	if _, err := e.lib.EditFileFacts(e.ctx, 1, FileFactsEdit{Quality: "WEBDL-1080p", Revision: 1}); !errors.Is(err, ErrRefused) {
		t.Errorf("dead link: %v", err)
	}
}

// TestEditFileFactsRenamePreview: an adopted link names its custom formats as adopt found them while it stores none;
// once its facts are corrected to match no format (stored as an empty list) the rename preview drops the old labels,
// and names the corrected quality with its revision.
func TestEditFileFactsRenamePreview(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.pol.Versions["movie-remux"].Naming.File = "{Movie CleanTitle} {[Custom Formats]}{[Quality Full]}"
	e.lib.formats = func(context.Context, string, int64, string) []string { return []string{"IMAX", "HDR"} }
	old := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "x.mkv"), old); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, file_metadata, state, adopted,
		adopted_from, created_at, updated_at) VALUES (1, ?, '/mnt/zurg/x.mkv', 1, 'Amelie.2001.1080p.BluRay-GRP',
		'Bluray-1080p', '{"Quality":"Bluray-1080p","Revision":1,"RelativePath":"a.mkv","SceneName":"Amelie.2001.1080p.BluRay-GRP"}', 'active', 1,
		'radarr /movies/Amelie (2001)/Amelie.2001.1080p.BluRay-GRP.mkv', 0, 0)`, old)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	name := func() string {
		t.Helper()
		p, err := e.lib.PreviewRename(e.ctx, 1, nil)
		if err != nil || len(p.Files) != 1 {
			t.Fatalf("preview %+v %v", p, err)
		}
		return filepath.Base(p.Files[0].NewPath)
	}
	if got := name(); !strings.Contains(got, "[IMAX HDR]") {
		t.Fatalf("adopted preview %s", got)
	}
	if _, err := e.lib.EditFileFacts(e.ctx, 1, FileFactsEdit{Quality: "WEBDL-1080p", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if got := name(); strings.Contains(got, "IMAX") || !strings.Contains(got, "[WEBDL-1080p Proper]") {
		t.Errorf("corrected preview %s", got)
	}
}
