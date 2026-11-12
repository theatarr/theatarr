package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// instanceLink makes a Radarr/Sonarr file at p: a symlink to target (relative targets are under the zurg release
// root), or a regular file when target is "".
func (e *env) instanceLink(p, target string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if target == "" {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			e.t.Fatal(err)
		}
		return
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(e.mount, "__all__", target)
	}
	if err := os.Symlink(target, p); err != nil {
		e.t.Fatal(err)
	}
}

func TestAdoptImportedMovie(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	var formatsFor []string
	e.lib.formats = func(_ context.Context, key string, _ int64, _ string) []string {
		formatsFor = append(formatsFor, key)
		return nil
	}
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	inst := filepath.Join(e.outside, "radarr")
	good := filepath.Join(inst, "Amelie (2001)", "Amelie.mkv")
	plain := filepath.Join(inst, "Plain (2010)", "Plain.mkv")
	off := filepath.Join(inst, "Off (2010)", "Off.mkv")
	e.instanceLink(good, movieRelease+"/movie.mkv")
	e.instanceLink(plain, "")
	e.instanceLink(off, filepath.Join(e.outside, "o.mkv"))

	// CheckImportFile: adopt's look, nothing written.
	for p, want := range map[string]string{good: "", plain: AdoptNotLink, off: AdoptOffMount,
		filepath.Join(inst, "none.mkv"): AdoptMissing} {
		if reason, _, err := e.lib.CheckImportFile(e.ctx, p); err != nil || reason != want {
			t.Errorf("check %s: %q %v, want %q", p, reason, err, want)
		}
	}
	for p, want := range map[string]bool{inst: true, good: false, filepath.Join(e.dir, "nowhere"): false} {
		if ok, err := e.lib.DirExists(e.ctx, p); err != nil || ok != want {
			t.Errorf("dir %s: %v %v", p, ok, err)
		}
	}

	const mediaTags = `{"videoCodec":"x265","videoDynamicRangeType":"HDR10"}`
	file := func(p string) ImportFile {
		return ImportFile{TitleID: 1, Source: "radarr@radarr:7878 /movies/" + filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p),
			Path: p, RelativePath: filepath.Base(p), Quality: "Remux-2160p", ReleaseGroup: "GRP",
			SceneName: "Amelie.2001.2160p.REMUX-GRP", Languages: []string{"French"}, Revision: 1, Size: 1000,
			MediaInfo: mediaTags}
	}
	rep, err := e.lib.AdoptImported(e.ctx, "movie-remux", []ImportFile{file(good), file(plain), file(off)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Linked != 1 || rep.Files != 3 || rep.Skipped[AdoptNotLink] != 1 || rep.Skipped[AdoptOffMount] != 1 {
		t.Fatalf("report %+v", rep)
	}
	p := rep.Links[0].Path
	if !strings.HasPrefix(p, filepath.Join(e.movies, "Amelie The Movie (2001)")+"/") {
		t.Fatalf("named %s", p)
	}
	if tg, err := os.Readlink(p); err != nil || tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
		t.Fatalf("link -> %q %v", tg, err)
	}
	// The score is the library's own profile's (no custom formats here: 0), not the instance's 50.
	if got := e.one(`SELECT l.adopted_from || '|' || l.quality || '|' || l.score || '|' || json_extract(l.file_metadata, '$.Score')
		|| '|' || l.adopted_media || '|' || json_extract(l.file_metadata, '$.SceneName') || '|' ||
		json_extract(l.file_metadata, '$.Languages[0]')
		FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 10`); got !=
		`radarr@radarr:7878 /movies/Amelie (2001)/Amelie.mkv|Remux-2160p|0|0|`+mediaTags+`|Amelie.2001.2160p.REMUX-GRP|French` {
		t.Errorf("link row %s", got)
	}
	// The name carries the instance's media info, and a rename right after keeps it.
	if !strings.Contains(p, "[HDR10][x265]") {
		t.Errorf("name without media info: %s", p)
	}
	if pr, err := e.lib.PreviewRename(e.ctx, 1, nil); err != nil || len(pr.Files) != 0 || pr.Skipped != 0 {
		t.Errorf("rename after import: %+v %v", pr, err)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'link' AND mode = 'applied'
		AND detail = 'adopted from radarr@radarr:7878 /movies/Amelie (2001)/Amelie.mkv'`); got != "1" {
		t.Errorf("journal %s", got)
	}
	// {Custom Formats} are decided for the library itself, not for a followed one.
	if len(formatsFor) == 0 || formatsFor[0] != "movie-remux" {
		t.Errorf("formats decided for %q", formatsFor)
	}

	// Again: the link is in place, nothing new.
	before := strings.Join(tree(t, e.dir), "|")
	rep, err = e.lib.AdoptImported(e.ctx, "movie-remux", []ImportFile{file(good)})
	if err != nil || rep.Linked != 0 || rep.Existing != 1 {
		t.Fatalf("second run %+v %v", rep, err)
	}
	if after := strings.Join(tree(t, e.dir), "|"); before != after {
		t.Fatal("second run changed the tree")
	}
	if got := e.one(`SELECT COUNT(*) FROM link`); got != "1" {
		t.Errorf("links %s", got)
	}
}

func TestAdoptImportedSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.1080p.WEB-DL-GRP"
	e.release(rel, map[string]float64{"Show.S01E01E02.mkv": 5400, "Show.S01E01.mkv": 2700})
	inst := filepath.Join(e.outside, "sonarr", "Show", "Season 1")
	pack := filepath.Join(inst, "Show - S01E01-E02.mkv")
	single := filepath.Join(inst, "Show - S01E01.mkv")
	e.instanceLink(pack, rel+"/Show.S01E01E02.mkv")
	e.instanceLink(single, rel+"/Show.S01E01.mkv")
	file := func(p string, eps ...int64) ImportFile {
		return ImportFile{TitleID: 2, Source: "sonarr@sonarr:8989 " + p, Path: p, RelativePath: filepath.Base(p),
			Quality: "WEBDL-1080p", ReleaseGroup: "GRP", Revision: 1, Size: 1000, Episodes: eps}
	}
	rep, err := e.lib.AdoptImported(e.ctx, "tv", []ImportFile{file(pack, 202, 201)})
	if err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	want := filepath.Join(e.shows, "Show (2020)", "Season 01", "Show (2020) - S01E01-E02 - Pilot and Second Part [WEBDL-1080p]-GRP.mkv")
	if rep.Links[0].Path != want {
		t.Fatalf("named %s", rep.Links[0].Path)
	}
	if got := e.one(`SELECT GROUP_CONCAT(episode_id, ' ') FROM (SELECT le.episode_id FROM link_episode le
		JOIN link_version lv ON lv.link_id = le.link_id WHERE lv.version_id = 20 ORDER BY le.episode_id)`); got != "201 202" {
		t.Errorf("link episodes %s", got)
	}
	// A file for an episode the library already has: its own file stays.
	rep, err = e.lib.AdoptImported(e.ctx, "tv", []ImportFile{file(single, 201)})
	if err != nil || rep.Linked != 0 || rep.HasFile != 1 {
		t.Fatalf("has file: %+v %v", rep, err)
	}
	// No episodes: not linked.
	rep, err = e.lib.AdoptImported(e.ctx, "tv", []ImportFile{file(single)})
	if err != nil || rep.Skipped[AdoptNoEpisodes] != 1 {
		t.Fatalf("no episodes: %+v %v", rep, err)
	}
}

func TestAdoptImportedNeedsLiveLibrary(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeShadow)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	p := filepath.Join(e.outside, "radarr", "Amelie (2001)", "Amelie.mkv")
	e.instanceLink(p, movieRelease+"/movie.mkv")
	before := strings.Join(tree(t, e.dir), "|")
	_, err := e.lib.AdoptImported(e.ctx, "movie-remux", []ImportFile{{TitleID: 1, Source: "radarr@r:7878 " + p, Path: p,
		Quality: "Remux-2160p"}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Watch only: %v", err)
	}
	if after := strings.Join(tree(t, e.dir), "|"); before != after {
		t.Fatal("refused run changed the tree")
	}
	if got := e.one(`SELECT COUNT(*) FROM link`); got != "0" {
		t.Errorf("links %s", got)
	}
}

// TestAdoptedRenameKeepsMediaInfo: a link adopted from a followed library is named with the arr's media info; a rename
// right after keeps it (the link has no probe, only adopted_media).
func TestAdoptedRenameKeepsMediaInfo(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"), movieRelease+"/movie.mkv", "Remux-2160p")
	e.exec(`UPDATE version_file SET media_info = '{"videoCodec":"x265","videoDynamicRangeType":"HDR10"}' WHERE id = 1`)
	rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"})
	if err != nil || rep.Linked != 1 || !strings.Contains(rep.Links[0].Path, "[HDR10][x265]") {
		t.Fatalf("%+v %v", rep, err)
	}
	if pr, err := e.lib.PreviewRename(e.ctx, 1, nil); err != nil || len(pr.Files) != 0 || pr.Skipped != 0 {
		t.Errorf("rename after adopt: %+v %v", pr, err)
	}
}
