package format

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// LoadTrash names the files of a checkout by their full path, as it always has; LoadTrashFS reads the same tree from
// a file system and names them from its root.
func TestLoadTrashDiskAndFS(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := LoadTrash(root, Radarr); err == nil || !strings.Contains(err.Error(), "open "+filepath.Join(root, "metadata.json")) {
		t.Fatalf("missing metadata.json: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("metadata.json", `{"json_paths":{"radarr":{"custom_formats":["cf"],"quality_profiles":["qp"],"custom_format_groups":["groups"]}}}`)
	if _, err := LoadTrash(root, Sonarr); err == nil || err.Error() != "trash: metadata.json has no sonarr paths" {
		t.Fatalf("flavour without paths: %v", err)
	}
	if _, err := LoadTrash(root, "lidarr"); err == nil || err.Error() != `trash: unknown flavour "lidarr"` {
		t.Fatalf("unknown flavour: %v", err)
	}
	write("cf/b.json", `{"trash_id":"b","name":"B","specifications":[]}`)
	write("cf/a.json", `{"trash_id":"a","name":"A","trash_scores":{"default":5},"specifications":[]}`)
	write("qp/p.json", `{"trash_id":"p","name":"P","formatItems":{"A":"a"}}`)
	write("groups/g.json", `{"trash_id":"g","name":"G","default":"true","custom_formats":[{"name":"B","trash_id":"b","required":true}],"quality_profiles":{"include":{"P":"p"}}}`)
	disk, err := LoadTrash(root, Radarr)
	if err != nil {
		t.Fatal(err)
	}
	fromFS, err := LoadTrashFS(os.DirFS(root), Radarr)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range []*Trash{disk, fromFS} {
		if len(tr.Formats) != 2 || tr.Formats[0].TrashID != "a" || tr.Formats[1].ID != 2 || string(tr.Raw("b")) == "" || tr.Profiles["p"] == nil || !tr.Groups["g"].IsDefault() {
			t.Fatalf("tree %+v", tr)
		}
		p, formats, err := tr.Profile("p", ProfileOptions{DefaultGroups: true})
		if err != nil || len(formats) != 2 || p.FormatItems[0].Score != 5 {
			t.Fatalf("profile %+v %v", p, err)
		}
	}
	write("cf/c.json", `{"trash_id":`)
	if _, err := LoadTrash(root, Radarr); err == nil || !strings.HasPrefix(err.Error(), filepath.Join(root, "cf", "c.json")+": ") {
		t.Fatalf("bad format on disk: %v", err)
	}
	if _, err := LoadTrashFS(os.DirFS(root), Radarr); err == nil || !strings.HasPrefix(err.Error(), "cf/c.json: ") {
		t.Fatalf("bad format in a file system: %v", err)
	}
	if _, err := LoadTrashFS(fstest.MapFS{}, Radarr); err == nil || !strings.Contains(err.Error(), "metadata.json") {
		t.Fatalf("empty file system: %v", err)
	}
}
