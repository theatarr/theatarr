package store

import (
	"context"
	"testing"
)

func TestSettingsVersionsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openAt(t, 68)
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 66); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := p.UpTo(ctx, 68); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"setting_version", "setting_view_binding"} {
			if _, err := s.Writer().ExecContext(ctx, "INSERT INTO "+table+" (key,config,created_at,updated_at) VALUES ('fixture','{}',0,0)"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Writer().ExecContext(ctx, "UPDATE "+table+" SET config='invalid'"); err == nil {
				t.Fatal("accepted invalid JSON")
			}
		}
		if _, err := p.DownTo(ctx, 66); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"setting_version", "setting_view_binding", "settings_e3_bootstrap"} {
			if _, err := s.Reader().QueryContext(ctx, "SELECT * FROM "+table); err == nil {
				t.Fatalf("%s survived migration down", table)
			}
		}
	}
}

// 00120: the guided migration's plans go, and a library row loses only its golive_done.
func TestDropGuidedMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 119)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 119); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO setting_version(key,config,created_at,updated_at) VALUES
			('movies','{"config":{"mode":"active","golive_done":["homepage"]},"roots":["/media/movies"]}',1,1),
			('tv','{"config":{"mode":"shadow"}}',1,1)`,
		`INSERT INTO migration_plan(name,inventory,plan,created_at,updated_at) VALUES('old','{}','{}',1,1)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := p.UpTo(ctx, 120); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"movies": `{"config":{"mode":"active"},"roots":["/media/movies"]}`,
		"tv":     `{"config":{"mode":"shadow"}}`,
	} {
		var config string
		if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM setting_version WHERE key=?`, key).Scan(&config); err != nil || config != want {
			t.Fatalf("%s config %s, %v", key, config, err)
		}
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='migration_plan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("migration_plan still there: %d %v", n, err)
	}
}
