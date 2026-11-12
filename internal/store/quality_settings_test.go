package store

import (
	"context"
	"testing"
)

func TestQualitySettingsMigration(t *testing.T) {
	t.Parallel()
	st := openAt(t, 67)
	ctx := context.Background()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 66); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec("INSERT INTO setting_budget(key,config,created_at,updated_at) VALUES('retained','{}',1,1)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 67); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"setting_quality_profile", "setting_custom_format", "setting_format_set", "setting_version_naming", "setting_quality_sync"} {
			if _, err := st.Writer().Exec("INSERT INTO " + table + "(key,config,created_at,updated_at) VALUES('example','{}',1,1)"); err != nil {
				t.Fatal(table, err)
			}
			if _, err := st.Writer().Exec("INSERT INTO " + table + "(key,config,created_at,updated_at) VALUES('invalid','not JSON',1,1)"); err == nil {
				t.Fatal("JSON constraint absent", table)
			}
		}
		if _, err := p.DownTo(ctx, 66); err != nil {
			t.Fatal(err)
		}
		var key string
		if err := st.Reader().QueryRow("SELECT key FROM setting_budget").Scan(&key); err != nil || key != "retained" {
			t.Fatal("source record changed", err)
		}
	}
}

// 00119: quality sync applies by itself. Pending reviews are superseded, the stored mode goes and every source's run
// digest is cleared, so the next run re-reads its upstream.
func TestQualitySyncAutoMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 116)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 116); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO setting_quality_sync(key,config,created_at,updated_at) VALUES('trash','{"kind":"trash","mode":"review","enabled":true}',1,1), ('plain','{"kind":"trash"}',1,1)`,
		`INSERT INTO settings_quality_review(id,source_key,base_generation,commit_id,preview,candidate,state,created_at) VALUES
			(1,'trash',1,'c','{}','null','pending',1), (2,'trash',1,'c','{}','null','applied',1), (3,'trash',1,'c','{}','null','superseded',1)`,
		`INSERT INTO settings_quality_sync_run(source_key,started_at,commit_id,outcome,config_digest) VALUES('trash',1,'c','review','abc')`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := p.UpTo(ctx, 119); err != nil {
		t.Fatal(err)
	}
	var states string
	if err := st.Reader().QueryRowContext(ctx, `SELECT group_concat(state, ',') FROM (SELECT state FROM settings_quality_review ORDER BY id)`).Scan(&states); err != nil || states != "superseded,applied,superseded" {
		t.Fatalf("review states %q, %v", states, err)
	}
	var config, plain string
	if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM setting_quality_sync WHERE key='trash'`).Scan(&config); err != nil || config != `{"kind":"trash","enabled":true}` {
		t.Fatalf("config %s, %v", config, err)
	}
	if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM setting_quality_sync WHERE key='plain'`).Scan(&plain); err != nil || plain != `{"kind":"trash"}` {
		t.Fatalf("config without mode %s, %v", plain, err)
	}
	var digest string
	if err := st.Reader().QueryRowContext(ctx, `SELECT config_digest FROM settings_quality_sync_run`).Scan(&digest); err != nil || digest != "" {
		t.Fatalf("digest %q, %v", digest, err)
	}
}
