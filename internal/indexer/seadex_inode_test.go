//go:build linux

package indexer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPublicationAliasSQLiteInode(t *testing.T) {
	for _, kind := range []string{"symlink-sidecar", "hardlink-sidecar"} {
		t.Run(kind, func(t *testing.T) {
			st := lockMappedStore(t)
			tx, err := st.Reader().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM title`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			script := `import fcntl, sys
f = open(sys.argv[1], 'r+b')
try:
 fcntl.lockf(f, fcntl.LOCK_EX | fcntl.LOCK_NB, 1, 1073741826)
 print('free')
except BlockingIOError:
 print('locked')`
			probe := func() string {
				out, err := exec.Command("python3", "-c", script, st.Path()).CombinedOutput()
				if err != nil {
					t.Fatalf("probe: %v %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			before := probe()
			if before != "locked" {
				t.Fatalf("bad fixture: %s", before)
			}
			target := st.Path() + ".seadex-lock"
			if strings.HasPrefix(kind, "symlink") {
				err = os.Symlink(st.Path(), target)
			} else {
				err = os.Link(st.Path(), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			f, err := openSeaDexLock(st.Path(), os.OpenFile)
			if err == nil {
				f.Close()
			}
			after := probe()
			t.Logf("%s publish alias: open error=%v; SQLite lock before=%s after=%s", kind, err, before, after)
			if err == nil {
				t.Error("database alias accepted")
			}
			if after != "locked" {
				t.Fatalf("SQLite lock was released")
			}
		})
	}
}
