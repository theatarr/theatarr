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
