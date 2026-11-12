package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The image cache (whose files the proxy removes) may not overlap a library root, the trash or the mount.
func TestImagesDirOverlap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, dir string }{
		{"in a root", "/media/movies/.images"},
		{"around the roots", "/media"},
		{"in the trash", "/media/.trash/images"},
		{"in the mount", "/mnt/debrid/images"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyExample(t)
			p := filepath.Join(dir, "ui.yaml")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(strings.Replace(string(b), "dir: /data/images", "dir: "+tc.dir, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "ui.images.dir") {
				t.Fatalf("want ui.images.dir refused, got %v", err)
			}
		})
	}
	if _, err := Load(copyExample(t)); err != nil {
		t.Fatalf("example: %v", err)
	}
}

// A root bind-mounted inside another version's root (the alias is below the other root, so neither its path
// nor its own device and inode match) overlaps.
func TestNestedBindAlias(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	arr, mine := filepath.Join(dir, "arr"), filepath.Join(dir, "theatarr")
	for _, d := range []string{arr, mine, filepath.Join(arr, "sub")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mi := filepath.Join(dir, "mountinfo")
	body := "20 1 0:99 / / rw,relatime - ext4 /dev/x rw\n" +
		"21 20 0:99 " + strings.ReplaceAll(mine, " ", `\040`) + " " + filepath.Join(arr, "sub") + " rw,relatime - ext4 /dev/x rw\n"
	if err := os.WriteFile(mi, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := mountInfoPath
	mountInfoPath = mi
	defer func() { mountInfoPath = old }()
	p := &Policy{Versions: map[string]*VersionPolicy{
		"F": {Kind: KindMovie, Library: arr},
		"K": {Kind: KindMovie, Mode: ModeActive, Library: mine},
	}, Library: &Library{Versions: map[string]*LibraryVersion{"K": {Roots: []string{mine}}}}}
	if _, err := p.CheckRootSeparation("K"); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("nested bind alias: %v", err)
	}
	// Without the bind the roots are separate.
	if err := os.WriteFile(mi, []byte("20 1 0:99 / / rw,relatime - ext4 /dev/x rw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CheckRootSeparation("K"); err != nil {
		t.Fatalf("separate roots: %v", err)
	}
}
