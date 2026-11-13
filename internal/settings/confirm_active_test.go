package settings

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Deleting a version removes its links (removeLibraryRows), so re-creating its key with another root is not refused
// for links left at the old root; re-creating it at the old root is allowed too.
func TestRecreatedVersionWithOtherRootChecksActiveLinks(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	oldRoot := initial.Policy.Versions["movie-web"].Library
	for _, query := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1002,'movie','Recreated',0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1002,1002,'movie-web',0,0)`,
		`INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(1002,'` + filepath.Join(oldRoot, "Recreated (2020)", "Recreated.mkv") + `','fixture-target',1002,0,0)`,
		`INSERT INTO link_version(link_id,version_id) VALUES(1002,1002)`,
	} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	req := saveRequest(p, initial)
	for i, v := range req.Records.Views {
		if v.Key == "radarr" {
			v.Versions = []string{"movie-remux", "anime-movie"}
			v.Profiles = nil
			req.Records.Views[i] = v
		}
	}
	var removed Version
	req.Records.Versions = slices.DeleteFunc(req.Records.Versions, func(v Version) bool {
		if v.Key == "movie-web" {
			removed = v
			return true
		}
		return false
	})
	if removed.Key == "" {
		t.Fatal("no movie-web record")
	}
	m := versionCoordinator(t, s, p, initial)
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatalf("confirm deletion: %v", err)
	}
	req.ConfirmationToken = proof.Token
	deleted, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("delete version: %v", err)
	}
	newRoot := filepath.Join(p.Library.Dangling.Roots[0], "recreated-root")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, deleted)
	removed.Config.Library = newRoot
	removed.Roots = []string{newRoot}
	req.Records.Versions = append(req.Records.Versions, removed)
	var links int
	if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM link`).Scan(&links); err != nil || links != 0 {
		t.Fatalf("links after the deletion: %d %v", links, err)
	}
	if _, err := m.Confirm(ctx, req); err != nil {
		t.Fatalf("re-created with another root: %v", err)
	}
	// The same root again: allowed.
	req = saveRequest(p, deleted)
	removed.Config.Library = oldRoot
	removed.Roots = []string{oldRoot}
	req.Records.Versions = append(req.Records.Versions, removed)
	if _, err := m.Confirm(ctx, req); err != nil {
		t.Fatalf("re-created at its old root: %v", err)
	}
}
