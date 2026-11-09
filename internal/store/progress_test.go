package store

import (
	"context"
	"testing"
)

func TestDownloadProgressMigrationUpDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 35), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 35); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 37); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Writer().ExecContext(ctx, `SELECT download_id, sampled_at, provider, torrent_id, progress FROM download_progress`); err != nil {
			t.Fatal(err)
		}
		if _, err := p.DownTo(ctx, 35); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Writer().ExecContext(ctx, `SELECT * FROM download_progress`); err == nil {
			t.Fatal("progress table left after rollback")
		}
	}
}
