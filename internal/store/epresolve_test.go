package store

import (
	"context"
	"testing"
)

// TestEpresolveMigrationUpDown: 00029 adds download.episode_map, download.source_path and download_file, and its
// Down removes them again.
func TestEpresolveMigrationUpDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 29), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 29); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `SELECT episode_map, source_path FROM download;
		SELECT download_id, rel, episodes, source, how, updated_at FROM download_file`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `SELECT episode_map FROM download`); err == nil {
		t.Error("download.episode_map is still there")
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'download_file'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("download_file left %d: %v", n, err)
	}
	if _, err := p.UpTo(ctx, 29); err != nil {
		t.Fatal(err)
	}
}
