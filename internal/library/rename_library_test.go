package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
)

// TestPreviewRenameLibraryListsOnlyThatLibrary: a title with links in the arr's library and Theatarr's copy; the
// copy's preview lists only its own link.
func TestPreviewRenameLibraryListsOnlyThatLibrary(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	target := filepath.Join(e.mount, "__all__", "x.mkv")
	own := filepath.Join(e.theatarrMovies, "Amelie (2001)", "old.mkv")
	arr := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
	for _, p := range []string{own, arr} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	meta := linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv",
		SceneName: "Movie.Release"})
	e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,created_at,updated_at)
		VALUES (5,?,?,1,'r','Remux-2160p',?,'active',0,0), (7,?,?,1,'r','Remux-2160p',?,'active',0,0)`,
		arr, target, meta, own, target, meta)
	e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (5,10), (7,11)`)
	p, err := e.lib.PreviewRenameLibrary(e.ctx, "movie-theatarr")
	if err != nil || len(p.Files) != 1 || p.Files[0].LinkID != 7 || p.Files[0].Version != "movie-theatarr" ||
		p.Files[0].ExistingPath != own || p.Files[0].NewPath == own {
		t.Fatalf("preview %+v %v", p, err)
	}
	if _, err := e.lib.PreviewRenameLibrary(e.ctx, "nope"); err == nil {
		t.Fatal("unknown library")
	}
}
