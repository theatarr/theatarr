package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// previewRename keeps its link rows open on the reader pool and queries the pool again inside the loop
// (probeResult, NamingFor, linkEpisodes): one preview needs two reader connections at once. With every other reader
// connection busy (other previews in the same state, other reads) it waits for a connection that never frees.
func TestRenamePreviewNeedsOneReader(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	old := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "x.mkv"), old); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id,path,target,title_id,release_title,quality,file_metadata,state,created_at,updated_at) VALUES (1,?,?,1,'r','Remux-2160p',?,'active',0,0)`, old, "/mnt/zurg/x.mkv", linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv", SceneName: "Movie.Release"}))
	e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10)`)

	readers := max(4, runtime.GOMAXPROCS(0))
	// N concurrent previews: each takes a connection for its rows, then asks for a second one.
	held := make([]*sql.Rows, 0, readers)
	for i := 0; i < readers-1; i++ { // the other N-1 previews, each parked holding its rows
		rs, err := e.st.Reader().QueryContext(e.ctx, `SELECT id FROM link`)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, rs)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	_, err := e.lib.PreviewRename(ctx, 1, nil)
	for _, rs := range held {
		rs.Close()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("preview with %d of %d reader connections free hung until its deadline: %v", 1, readers, err)
	}

	// The real thing: N previews at once, no outside holder.
	var wg sync.WaitGroup
	var mu sync.Mutex
	hung := 0
	for i := 0; i < 8*readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
			defer cancel()
			if _, err := e.lib.PreviewRename(ctx, 1, nil); errors.Is(err, context.DeadlineExceeded) {
				mu.Lock()
				hung++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if hung > 0 {
		t.Errorf("%d of %d concurrent previews deadlocked on the %d-connection reader pool", hung, 8*readers, readers)
	}
}
