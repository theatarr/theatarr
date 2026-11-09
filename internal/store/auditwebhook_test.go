package store

import (
	"context"
	"strings"
	"testing"
)

// Migration 00083 scrubs notify target URLs (which carry webhook tokens) from existing settings audit rows
// and leaves every other row as it was.
func TestAuditWebhookURLMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 83)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 82); err != nil {
		t.Fatal(err)
	}
	const notify = `{"entity":"sections","key":"notify","config":{"allow_hosts":[],"targets":{"a":{"url":"https://hook.example/api/webhooks/1/secret-token-0123","native":true},"b":{"events":["grab"]}}}}`
	const library = `{"entity":"sections","key":"library","config":{"plex":{"url":"http://plex:32400/?X-Plex-Token=keep"}}}`
	for _, q := range []string{
		`INSERT INTO settings_generation (generation, revision, created_at) VALUES (1, 'r', 0)`,
		`INSERT INTO settings_audit (actor, created_at, generation, entity, key, operation, before, after) VALUES
			('t', 0, 1, 'sections', 'notify', 'update', 'null', '` + notify + `'),
			('t', 0, 1, 'sections', 'notify', 'update', '` + notify + `', '{"entity":"sections","key":"notify","config":{"targets":null}}'),
			('t', 0, 1, 'sections', 'library', 'update', 'null', '` + library + `')`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 83); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Reader().Query(`SELECT key, before, after FROM settings_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var key, before, after string
		if err := rows.Scan(&key, &before, &after); err != nil {
			t.Fatal(err)
		}
		got = append(got, key+" "+before+" "+after)
	}
	all := strings.Join(got, "\n")
	if len(got) != 3 || strings.Contains(all, "secret-token") || strings.Count(all, `"url":"[redacted]"`) != 2 ||
		!strings.Contains(all, `"events":["grab"]`) || !strings.Contains(all, `"native":true`) ||
		!strings.Contains(got[1], `"targets":null`) || !strings.Contains(got[2], "X-Plex-Token=keep") {
		t.Fatalf("audit after migration:\n%s", all)
	}
	if _, err := p.DownTo(ctx, 82); err != nil {
		t.Fatal(err)
	}
}
