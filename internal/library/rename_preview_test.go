package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

func linkMetadata(t *testing.T, f decide.ExistingFile) string {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRenamePreviewSeriesMultiEpisodeAndMovie(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	movieOld := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
	seriesOld := filepath.Join(e.shows, "Show (2020)", "Season 01", "old.mkv")
	for _, p := range []string{movieOld, seriesOld} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(e.mount, "__all__", "x.mkv"), p); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,created_at,updated_at) VALUES (1,?,?,1,'r','Remux-2160p',?,'active',0,0)`, movieOld, "/mnt/zurg/x.mkv", linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv", SceneName: "Movie.Release"}))
	e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10)`)
	e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,created_at,updated_at) VALUES (2,?,?,2,'r','Bluray-2160p Remux',?,'active',0,0)`, seriesOld, "/mnt/zurg/x.mkv", linkMetadata(t, decide.ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, RelativePath: "pack.mkv", SceneName: "Show.S01E01-E02"}))
	e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (2,20)`)
	e.exec(`INSERT INTO link_episode (link_id,episode_id) VALUES (2,201),(2,202)`)
	m, err := e.lib.PreviewRename(e.ctx, 1, nil)
	if err != nil || len(m.Files) != 1 || m.Files[0].ExistingPath != movieOld {
		t.Fatalf("movie preview: %+v %v", m, err)
	}
	s, err := e.lib.PreviewRename(e.ctx, 2, nil)
	if err != nil || len(s.Files) != 1 || s.Files[0].ExistingPath != seriesOld || len(s.Files[0].EpisodeNums) != 2 {
		t.Fatalf("series preview: %+v %v", s, err)
	}
	filtered, err := e.lib.PreviewRename(e.ctx, 2, func() *int { n := 2; return &n }())
	if err != nil || len(filtered.Files) != 0 {
		t.Fatal("season filtering failed")
	}
}

func TestRenameApplyGateAndIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	old := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(e.mount, "__all__", "x.mkv")
	if err := os.Symlink(target, old); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,created_at,updated_at) VALUES (1,?,?,1,'r','Remux-2160p',?,'active',0,0)`, old, target, linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv", SceneName: "Movie.Release"}))
	e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10)`)
	p, err := e.lib.ApplyRename(e.ctx, 1, nil)
	if err != nil || len(p.Files) != 1 {
		t.Fatalf("apply: %+v %v", p, err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old link remains: %v", err)
	}
	newPath := p.Files[0].NewPath
	if got := e.one(`SELECT path FROM link WHERE id=1`); got != newPath {
		t.Fatalf("row path %q want %q", got, newPath)
	}
	if p, err = e.lib.ApplyRename(e.ctx, 1, nil); err != nil || len(p.Files) != 0 {
		t.Fatalf("second apply: %+v %v", p, err)
	}
	if got := e.one(`SELECT COUNT(*) FROM notify_rename`); got != "1" {
		t.Fatalf("apply must queue one rename: %s", got)
	}
	e2 := newEnv(t, false, policy.ModeActive)
	old2 := filepath.Join(e2.movies, "Amelie (2001)", "old.mkv")
	if err := os.MkdirAll(filepath.Dir(old2), 0o755); err != nil {
		t.Fatal(err)
	}
	target2 := filepath.Join(e2.mount, "__all__", "x.mkv")
	if err := os.Symlink(target2, old2); err != nil {
		t.Fatal(err)
	}
	e2.exec(`INSERT INTO link (id,path,target,title_id,file_metadata,state,created_at,updated_at) VALUES (1,?,?,1,?,'active',0,0)`, old2, target2, linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv"}))
	e2.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10)`)
	if _, err := e2.lib.ApplyRename(e2.ctx, 1, nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("closed gate: %v", err)
	}
	if _, err := os.Lstat(old2); err != nil {
		t.Fatalf("closed gate changed old link: %v", err)
	}
}

// TestRenameSkipsRemovedAndUnknownLibraries: a removed copy's links are neither previewed nor renamed, and a link of
// a library no longer in the settings is skipped (counted), not an error.
func TestRenameSkipsRemovedAndUnknownLibraries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	old := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
	gone := filepath.Join(e.movies, "Amelie (2001)", "gone.mkv")
	target := filepath.Join(e.mount, "__all__", "x.mkv")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{old, gone} {
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	meta := linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv", SceneName: "Movie.Release"})
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (99, 1, 'gone-lib', 'wanted', 0, 0)`)
	e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,created_at,updated_at) VALUES
		(1,?,?,1,'r','Remux-2160p',?,'active',0,0), (2,?,?,1,'r','Remux-2160p',?,'active',0,0)`, old, target, meta, gone, target, meta)
	e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10), (2,99)`)
	p, err := e.lib.PreviewRename(e.ctx, 1, nil)
	if err != nil || len(p.Files) != 1 || p.Files[0].LinkID != 1 || p.Skipped != 1 {
		t.Fatalf("preview: %+v %v", p, err)
	}
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'web', 0)`)
	if p, err = e.lib.PreviewRename(e.ctx, 1, nil); err != nil || len(p.Files) != 0 || p.Skipped != 1 {
		t.Fatalf("preview after removal: %+v %v", p, err)
	}
	if p, err = e.lib.ApplyRename(e.ctx, 1, nil); err != nil || len(p.Files) != 0 {
		t.Fatalf("apply after removal: %+v %v", p, err)
	}
	if _, err := os.Lstat(old); err != nil {
		t.Fatalf("a removed copy's link was renamed: %v", err)
	}
}

// A long series previews every link with its own probe, episodes (with the absolute number) and adopted formats; the
// golden file is the preview as the per-link reads made it.
func TestRenamePreviewLargeSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	const n = 300
	e.exec(`UPDATE title SET series_type = 'anime' WHERE id = 2`)
	e.pol.Versions["tv"].Naming.Anime = "{Series CleanTitleWithoutYear} - S{season:00}E{episode:00} - {absolute:000} - {Episode CleanTitle} {[Custom Formats]}{[Mediainfo VideoCodec]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{-Release Group}"
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (1, 'h', 'c', 'c', 0, 0)`)
	pj, _ := json.Marshal(probed("hevc", 1920, 1080, "", ""))
	probeJSON := string(pj)
	for i := range n {
		id, ep := int64(100+i), int64(1000+i)
		e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (?, 2, ?, ?, ?, 0, 0)`,
			ep, 2+i/25, 1+i%25, fmt.Sprintf("Episode %d", i))
		if i%3 != 2 {
			e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, number) VALUES (?, 2, 'absolute_tvdb', ?)`, ep, i+1)
		}
		old := filepath.Join(e.shows, "Show (2020)", fmt.Sprintf("old-%d.mkv", i))
		var cf, from any
		switch i % 3 {
		case 0:
			e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes, probe) VALUES (?, 1, ?, 1, ?)`, id, fmt.Sprintf("f%d.mkv", i), probeJSON)
			cf = id
		case 1:
			from = fmt.Sprintf("sonarr /tv/Show/Show.S01E%02d.1080p.WEB-DL-GRP.mkv", i)
		}
		e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,content_file_id,adopted_from,created_at,updated_at)
			VALUES (?,?,?,2,'r','WEBDL-1080p',?,'active',?,?,0,0)`, id, old, "/mnt/zurg/x.mkv",
			linkMetadata(t, decide.ExistingFile{Quality: "WEBDL-1080p", Revision: 1, RelativePath: "e.mkv", SceneName: "Show.S01E01.1080p-GRP"}), cf, from)
		e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (?,20)`, id)
		e.exec(`INSERT INTO link_episode (link_id,episode_id) VALUES (?,?)`, id, ep)
	}
	// The adopted links' formats share one memo per preview (a RenameFormats loads the title and library once).
	memos := map[string]int{}
	e.lib.formats = func(ctx context.Context, _ string, _ int64, _ string) []string {
		memos[fmt.Sprintf("%p", RenameMemo(ctx))]++
		return nil
	}
	start := time.Now()
	got, err := e.lib.PreviewRename(e.ctx, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preview of %d links: %v", n, time.Since(start))
	if len(memos) != 1 || memos[fmt.Sprintf("%p", map[int64]decide.Title(nil))] != 0 {
		t.Fatalf("rename memos %v", memos)
	}
	b, _ := json.MarshalIndent(got, "", " ")
	b = []byte(strings.ReplaceAll(string(b), e.dir, "DIR"))
	golden := filepath.Join("testdata", "rename_preview_large.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Fatalf("preview differs from %s:\n%s", golden, b)
	}
}
