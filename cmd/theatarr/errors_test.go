package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// errorsDB is a migrated database with two inbox rows: a log error (id 1) and a panic with a stack (id 2).
func errorsDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	in := errinbox.New(st, errinbox.Options{Version: "test"})
	in.Report(errinbox.Event{Source: errinbox.SourceLog, Component: "router", Level: "ERROR",
		Message: "route failed for title 12: disk full", Time: time.Now()})
	in.Report(errinbox.Event{Source: errinbox.SourcePanic, Component: "jobs", Level: "ERROR", Message: "boom",
		Stack: "goroutine 1 [running]:\npanic({0x0, 0x0})\ngithub.com/theatarr/theatarr/internal/jobs.run()\n", Time: time.Now()})
	if err := in.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestErrorsCommand(t *testing.T) {
	t.Parallel()
	db := errorsDB(t)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runErrors(append([]string{args[0], "-db", db}, args[1:]...), &out)
		return out.String(), err
	}
	out, err := run("list", "-status", "new", "-since", "1h")
	if err != nil || !strings.Contains(out, "route failed for title 12: disk full") || !strings.Contains(out, "2 shown of 2") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = run("list", "-json", "-source", "panic")
	var p errinbox.Page
	if err != nil || json.Unmarshal([]byte(out), &p) != nil || p.Total != 1 || p.Items[0].Component != "jobs" {
		t.Fatalf("list -json: %v\n%s", err, out)
	}
	id := p.Items[0].ID
	if out, err = run("show", itoaCLI(id)); err != nil || !strings.Contains(out, "internal/jobs.run()") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	var row errinbox.Row
	if out, err = run("show", itoaCLI(id), "-json"); err != nil || json.Unmarshal([]byte(out), &row) != nil || row.ID != id {
		t.Fatalf("show -json: %v\n%s", err, out)
	}
	if out, err = run("resolve", itoaCLI(id), "-note", "fixed in 1.2.3"); err != nil || !strings.Contains(out, "resolved") {
		t.Fatalf("resolve: %v\n%s", err, out)
	}
	if out, err = run("show", itoaCLI(id), "-json"); err != nil || json.Unmarshal([]byte(out), &row) != nil ||
		row.Status != "resolved" || row.Note != "fixed in 1.2.3" || row.ResolvedAt == nil {
		t.Fatalf("after resolve: %v\n%s", err, out)
	}
	if out, err = run("list"); err != nil || !strings.Contains(out, "1 shown of 1") {
		t.Fatalf("list open: %v\n%s", err, out)
	}
	if out, err = run("ignore", "#1"); err != nil || !strings.Contains(out, "error 1: ignored") {
		t.Fatalf("ignore: %v\n%s", err, out)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"show"}, {"resolve"}, {"show", "1", "2"}, {"list", "1"},
		{"list", "-status", "gone"}, {"list", "-since", "soon"}, {"resolve", "x"}} {
		if len(bad) > 0 {
			bad = append([]string{bad[0], "-db", db}, bad[1:]...)
		}
		if err := runErrors(bad, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
	if _, err := run("show", "99"); err == nil {
		t.Error("show of a missing id")
	}
}

func itoaCLI(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestDebugBundle plants a secret in the environment (a credential variable and one the policy names), in log
// lines (as text, under a credential-named key, in a URL) and raw in an inbox row, and checks it appears nowhere
// in the bundle while the rest does. Stored credentials (a record's, and a section's by path) are masked too.
func TestDebugBundle(t *testing.T) {
	const secret = "s3cr3tPlanted42x"
	const policySecret = "tmdbPlantedKey77"
	const storedSecret = "storedPlantedKeyNoDigits"
	const sectionSecret = "prowlarrPlantedKeyNoDigits"
	t.Setenv("THEATARR_TEST_BUNDLE_TOKEN", secret)
	t.Setenv("THEATARR_TMDB_KEY", policySecret)
	t.Setenv("THEATARR_SETTINGS_SOURCE", "yaml")
	db := errorsDB(t)
	st, err := store.Open(context.Background(), db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`UPDATE error_event SET sample = 'failed with ' || ?, context = ?, stack = stack || ?
		WHERE id = 1`, secret, `{"token":"`+secret+`","url":"https://x.test/api?apikey=`+policySecret+`"}`, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO setting_secret (value, created_at, updated_at) VALUES (?, 0, 0), (?, 0, 0)`,
		storedSecret, `{"prowlarr.api_key":"`+sectionSecret+`"}`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	logs := filepath.Join(filepath.Dir(db), "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"time":"2026-10-30T10:00:00Z","level":"ERROR","msg":"auth failed for ` + secret + `","component":"router"}`,
		`{"time":"2026-10-30T10:00:01Z","level":"INFO","msg":"fetch","api_key":"` + secret + `","url":"https://idx.test/api?t=search&apikey=` + policySecret + `"}`,
		`plain text line with ` + policySecret,
		`stored credentials ` + storedSecret + ` and ` + sectionSecret,
	}
	if err := os.WriteFile(filepath.Join(logs, "theatarr.log.1"), []byte(`{"msg":"older line"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "theatarr.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	var stdout bytes.Buffer
	if err := runDebug(commandSettingsArgs(t, []string{"bundle", "-db", db, "-policy", filepath.Join("..", "..", "examples", "policy"),
		"-out", out}), &stdout); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("bundle file: %v %v", fi, err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[filepath.Base(h.Name)] = string(b)
	}
	for _, name := range []string{"build.json", "settings.yaml", "logs.jsonl", "errors.json", "health.json"} {
		if _, ok := got[name]; !ok {
			t.Errorf("bundle lacks %s: %v", name, got)
		}
	}
	for name, body := range got {
		if strings.Contains(body, secret) || strings.Contains(body, policySecret) || strings.Contains(body, storedSecret) ||
			strings.Contains(body, sectionSecret) {
			t.Errorf("%s carries a planted secret:\n%s", name, body)
		}
	}
	if !strings.Contains(got["logs.jsonl"], "older line") || !strings.Contains(got["logs.jsonl"], "auth failed for") ||
		!strings.Contains(got["logs.jsonl"], "stored credentials") {
		t.Errorf("logs: %s", got["logs.jsonl"])
	}
	if !strings.Contains(got["errors.json"], "internal/jobs.run()") || !strings.Contains(got["errors.json"], "disk full") {
		t.Errorf("errors: %s", got["errors.json"])
	}
	if !strings.Contains(got["build.json"], `"schema_version": 118`) {
		t.Errorf("build: %s", got["build.json"])
	}
}
