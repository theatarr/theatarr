package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// File names travel byte for byte: a name that is not UTF-8 lists, stats and reads back as it is on disk.
func TestMountHelperNonUTF8Names(t *testing.T) {
	t.Parallel()
	hm := helperMount(t, 5*time.Second)
	rel := filepath.Join(hm.AllPath(), "Rel")
	bad := filepath.Join(rel, "bad-\xff.mkv")
	if err := os.WriteFile(bad, []byte("abc"), 0o644); err != nil {
		t.Skip("filesystem refuses the name:", err)
	}
	if err := os.Symlink("bad-\xff.mkv", filepath.Join(rel, "link-\xfe")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ents, err := hm.ReadDir(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
		if _, err := e.Info(); err != nil {
			t.Errorf("info of %q: %v", e.Name(), err)
		}
	}
	if want := []string{"bad-\xff.mkv", "link-\xfe", "movie.mkv"}; strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("names %q, want %q", names, want)
	}
	if fi, err := hm.Stat(ctx, bad); err != nil || fi.Size() != 3 || fi.Name() != "bad-\xff.mkv" {
		t.Errorf("stat %v %v", fi, err)
	}
	if loc, err := hm.Locate(ctx, filepath.Join(rel, "link-\xfe")); err != nil || loc.Rel != "link-\xfe" {
		t.Errorf("locate %+v %v", loc, err)
	}
	v, err := hm.withTimeout(ctx, rel, func(f mountFS) (any, error) { return f.Readlink(filepath.Join(rel, "link-\xfe")) })
	if err != nil || v.(string) != "bad-\xff.mkv" {
		t.Errorf("readlink %q %v", v, err)
	}
}
