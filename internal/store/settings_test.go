package store

import (
	"context"
	"testing"
)

func TestSettingsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 55)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 54); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := p.UpTo(ctx, 55); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`INSERT INTO settings_generation VALUES (1, 'revision', 0)`,
			`INSERT INTO setting_secret VALUES (1, 'invented-key', 0, 0)`,
			`INSERT INTO setting_indexer VALUES (1, 'source', 'torznab', 1, 0, 1, '{}', 0, 0)`,
			`INSERT INTO setting_provider VALUES (1, 'account', 'torbox', 0, 1, '{}', NULL, '{}', 0, 0)`,
			`INSERT INTO setting_budget VALUES (1, 'budget', '{}', 0, 0)`,
			`INSERT INTO settings_audit VALUES (1, 'admin', 0, 1, 'indexers', 'source', 'create', 'null', '{}')`,
		} {
			if _, err := st.Writer().ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		for _, q := range []string{
			`DELETE FROM setting_secret WHERE id = 1`,
			`INSERT INTO setting_indexer VALUES (2, 'bad', 'torznab', 2, 0, NULL, '{}', 0, 0)`,
			`UPDATE setting_provider SET config = 'not json'`,
			`UPDATE setting_indexer SET secret_id = 999`,
			`UPDATE setting_indexer SET priority = -1`,
			`UPDATE setting_provider SET order_rank = 0`,
		} {
			if _, err := st.Writer().ExecContext(ctx, q); err == nil {
				t.Fatalf("accepted %s", q)
			}
		}
		if _, err := p.DownTo(ctx, 54); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"settings_generation", "setting_secret", "setting_indexer", "setting_provider", "setting_budget", "settings_audit"} {
			if _, err := st.Reader().ExecContext(ctx, "SELECT * FROM "+table); err == nil {
				t.Fatalf("%s survived down", table)
			}
		}
	}
}
