package arrview

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestDeleteTitleFiles(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"delete files", "keep files", "gate closed", "outside root", "regular file", "no trash", "no library", "arr files only"} {
		t.Run(mode, func(t *testing.T) {
			e := newDevEnv(t)
			e.seedFiles()
			dir := t.TempDir()
			web, remux := filepath.Join(dir, "web"), filepath.Join(dir, "remux")
			trash, target := filepath.Join(dir, "trash"), filepath.Join(dir, "mount.mkv")
			for _, root := range []string{web, remux} {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte("mount content"), 0o644); err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(web, "alpha.mkv"), filepath.Join(remux, "alpha.mkv")}
			if mode == "outside root" {
				paths[1] = filepath.Join(dir, "outside.mkv")
			}
			for i, path := range paths {
				if mode == "regular file" && i == 1 {
					if err := os.WriteFile(path, []byte("regular file"), 0o644); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			e.exec(`UPDATE link SET path = ?, target = ? WHERE id = 7`, paths[0], target)
			e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
				VALUES (8, ?, ?, 1, 'active', 0, 0)`, paths[1], target)
			e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (8, 1)`)
			arrPath := filepath.Join(remux, "arr.mkv")
			if err := os.WriteFile(arrPath, []byte("arr file"), 0o644); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE version_file SET path = ? WHERE id = 1`, arrPath)
			e.pol.Versions["movie-remux"].Mode = policy.ModeActive
			e.pol.Versions["movie-remux"].Library = remux
			e.pol.Versions["movie-web"].Library = web
			e.pol.Library = &policy.Library{Enabled: mode != "gate closed", Trash: policy.LibraryTrash{Dir: trash},
				Versions: map[string]*policy.LibraryVersion{
					"movie-remux": {Roots: []string{remux}}, "movie-web": {Roots: []string{web}},
				}}
			if mode == "no trash" {
				e.pol.Library.Trash.Dir = ""
			}
			e.svc.library = library.New(library.Options{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.now }})
			if mode == "no library" {
				e.svc.library = nil
			}
			if mode == "arr files only" {
				e.exec(`DELETE FROM link_version WHERE link_id IN (7, 8)`)
				e.exec(`DELETE FROM link WHERE id IN (7, 8)`)
			}
			want := 409
			if mode == "delete files" || mode == "keep files" || mode == "arr files only" {
				want = 200
			}
			deleteFiles := mode != "keep files"
			e.call("DELETE", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1)+"?deleteFiles="+strconv.FormatBool(deleteFiles), nil, want, nil)
			for _, path := range paths {
				_, err := os.Lstat(path)
				if mode == "delete files" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("link still on disk: %s, %v", path, err)
					}
				} else if err != nil {
					t.Fatalf("file changed: %s, %v", path, err)
				}
			}
			moved, removed := int64(0), int64(0)
			if mode == "delete files" {
				moved = 2
				rows, err := e.st.Reader().Query(`SELECT trash_path FROM link_trash ORDER BY link_id`)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				for rows.Next() {
					var path string
					if err := rows.Scan(&path); err != nil {
						t.Fatal(err)
					}
					if got, err := os.Readlink(path); err != nil || got != target {
						t.Fatalf("trash link %s: %q, %v", path, got, err)
					}
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
			}
			if want == 200 {
				removed = 2
				e.call("GET", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 404, nil)
			} else {
				e.call("GET", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, nil)
				if e.one(`SELECT SUM(monitored) FROM version WHERE title_id = 1`) != 2 || len(e.events.events) != 0 ||
					e.one(`SELECT COUNT(*) FROM library_action`) != 0 {
					t.Fatal("refused delete changed monitoring or emitted an action")
				}
			}
			if e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id IN (1, 2)`) != removed ||
				e.one(`SELECT COUNT(*) FROM link_trash`) != moved || e.one(`SELECT COUNT(*) FROM link WHERE state = 'unlinked'`) != moved {
				t.Fatal("wrong removal or trash state")
			}
			for path, want := range map[string]string{arrPath: "arr file", target: "mount content"} {
				if got, err := os.ReadFile(path); err != nil || string(got) != want {
					t.Fatalf("content changed: %s: %q, %v", path, got, err)
				}
			}
			if e.one(`SELECT COUNT(*) FROM version_file WHERE title_id = 1`) != 1 || e.one(`SELECT COUNT(*) FROM version WHERE title_id = 1`) != 2 {
				t.Fatal("arr file or version rows deleted")
			}
		})
	}
}
