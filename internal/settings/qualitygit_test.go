package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityTrashCommit(t *testing.T) {
	t.Parallel()
	commit := strings.Repeat("a", 40)
	for _, kind := range []string{"detached", "loose", "packed", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, content string) {
				t.Helper()
				path = filepath.Join(root, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "detached":
				write(".git/HEAD", commit+"\n")
			case "loose":
				write(".git/HEAD", "ref: refs/heads/main\n")
				write(".git/refs/heads/main", commit+"\n")
			case "packed":
				write(".git/HEAD", "ref: refs/heads/main\n")
				write(".git/packed-refs", "# pack-refs\n"+commit+" refs/heads/main\n")
			case "worktree":
				write(".git", "gitdir: meta/worktrees/quality\n")
				write("meta/worktrees/quality/HEAD", "ref: refs/heads/main\n")
				write("meta/worktrees/quality/commondir", "../..\n")
				write("meta/packed-refs", commit+" refs/heads/main\n")
			}
			got, err := trashCommit(root)
			if err != nil || got != commit {
				t.Fatalf("commit=%s err=%v", got, err)
			}
		})
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "format.json"), []byte(`{"name":"A"}`), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := trashCommit(root)
	if err != nil || !strings.HasPrefix(first, "content:") {
		t.Fatal(first, err)
	}
	if err := os.WriteFile(filepath.Join(root, "format.json"), []byte(`{"name":"B"}`), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := trashCommit(root)
	if err != nil || first == second {
		t.Fatal("export digest did not track content", err)
	}
}
