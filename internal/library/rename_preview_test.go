package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
