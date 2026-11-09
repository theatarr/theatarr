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
