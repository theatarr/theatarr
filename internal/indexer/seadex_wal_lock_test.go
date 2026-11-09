//go:build linux

package indexer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSeaDexOpeningDropsSQLiteWALDatabaseLock(t *testing.T) {
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
	probe := func() string {
		t.Helper()
		script := `import fcntl, sys
f = open(sys.argv[1], 'r+b')
try:
 fcntl.lockf(f, fcntl.LOCK_EX | fcntl.LOCK_NB, 1, 1073741826)
 print('free')
except BlockingIOError:
 print('locked')`
		out, err := exec.Command("python3", "-c", script, st.Path()).CombinedOutput()
		if err != nil {
			t.Fatalf("child: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	before := probe()
	if before != "locked" {
		t.Fatalf("fixture needs SQLite's WAL shared db lock, got %q", before)
	}
	f, err := openSeaDexLock(st.Path(), os.OpenFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	after := probe()
	t.Logf("WAL read transaction open: other process's exclusive byte-range lock before=%s after=%s", before, after)
	if after != "locked" {
		t.Fatal("opening SeaDex sidecar silently stripped SQLite's lock")
	}
}
