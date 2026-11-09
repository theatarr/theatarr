package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// copyTree copies the example policy, overrides and all, so a test may edit it.
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// A backup set restores to a database with the same titles, versions, links and users, and the same policy; the
// manifest names the secrets and never holds a value.
func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	polDir := copyTree(t, filepath.Join("..", "..", "examples", "policy"))
	pol, err := policy.Load(polDir)
	if err != nil {
		t.Fatal(err)
	}
	secrets := pol.SecretEnvs()
	var someEnv string
	for env := range secrets {
		someEnv = env
		break
	}
	if someEnv == "" {
		t.Fatal("the example policy names no secrets")
	}
	t.Setenv(someEnv, "TOPSECRETVALUE")
	src := filepath.Join(dir, "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, src), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := store.Millis(time.Now())
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 2020, 101, ?1, ?1),
			(2, 'series', 'Gamma', 2019, NULL, ?1, ?1)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-remux', ?1, ?1)`,
		`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES
			('/media/movies/Alpha (2020)/Alpha.mkv', '/mnt/zurg/__all__/Alpha/Alpha.mkv', 1, ?1, ?1)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q, now); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	backups := filepath.Join(dir, "backups")
	base, m, err := writeBackupSet(ctx, st, pol, backups, time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(base) != "theatarr-20261026-030000" || m.Policy.Revision != pol.Revision || m.Schema == 0 {
		t.Fatalf("set %s %+v", base, m)
	}
	raw, _ := os.ReadFile(base + ".manifest.json")
	if bytes.Contains(raw, []byte("TOPSECRETVALUE")) || !bytes.Contains(raw, []byte(`"`+someEnv+`"`)) {
		t.Fatalf("manifest secrets: %s", raw)
	}
	for _, s := range m.Secrets {
		if s.Env == someEnv && !s.Set || s.Env != someEnv && s.Set && os.Getenv(s.Env) == "" {
			t.Errorf("secret %+v", s)
		}
	}
	if !strings.Contains(strings.Join(m.Policy.Files, ","), "overrides/") {
		t.Errorf("the policy archive lacks the overrides: %v", m.Policy.Files)
	}

	// Restore into a fresh place; refused over an existing database without -force.
	var out bytes.Buffer
	dst, polOut := filepath.Join(dir, "restored", "theatarr.db"), filepath.Join(dir, "restored", "policy")
	getenv := func(k string) string { return "" }
	// The restore names the settings import of the policy files only when it previews; getenv reports the secrets.
	setFixtureEnv(t, base)
	if err := restoreBackup(ctx, &out, base+".db", dst, polOut, false, getenv, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), someEnv) || !strings.Contains(out.String(), "MISSING") ||
		strings.Contains(out.String(), "TOPSECRETVALUE") {
		t.Errorf("restore output:\n%s", out.String())
	}
	got, err := policy.Load(polOut)
	if err != nil || got.Revision != pol.Revision {
		t.Fatalf("restored policy %v", err)
	}
	a, err := fingerprint(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fingerprint(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) || a["titles"] != 2 || a["links"] != 1 {
		t.Errorf("fingerprints differ or wrong:\n%s\n%s", ja, jb)
	}
	if err := restoreBackup(ctx, &out, base, dst, filepath.Join(dir, "p2"), false, getenv, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "exists") {
		t.Errorf("restore over a database: %v", err)
	}
	if err := restoreBackup(ctx, &out, base, dst, polOut, true, getenv, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst + ".pre-restore-20261027-000000"); err != nil {
		t.Errorf("the replaced database was not kept: %v", err)
	}

	// A damaged set is refused before anything is written.
	os.WriteFile(base+".db", []byte("not a database"), 0o600)
	if err := restoreBackup(ctx, &out, base, filepath.Join(dir, "x.db"), filepath.Join(dir, "p3"), false, getenv,
		time.Now()); err == nil || !strings.Contains(err.Error(), "does not match its manifest") {
		t.Errorf("damaged set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.db")); err == nil {
		t.Error("a damaged set wrote a database")
	}

	var list bytes.Buffer
	if err := listBackups(&list, backups); err != nil || !strings.Contains(list.String(), "theatarr-20261026-030000") {
		t.Errorf("list: %v\n%s", err, list.String())
	}
}

func TestUntarRefusesEscapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"../evil.yaml", "/etc/evil.yaml"} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
		tw.Close()
		p := filepath.Join(dir, "a.tar")
		os.WriteFile(p, b.Bytes(), 0o600)
		if err := untar(p, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "link.yaml", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	tw.Close()
	p := filepath.Join(dir, "b.tar")
	os.WriteFile(p, b.Bytes(), 0o600)
	if err := untar(p, filepath.Join(dir, "out")); err == nil {
		t.Error("a symlink was unpacked")
	}
}

func TestReadEnvNames(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "theatarr.env")
	os.WriteFile(p, []byte("# keys\nTHEATARR_A=abc\nexport THEATARR_B=\"x\"\nTHEATARR_EMPTY=\nTHEATARR_Q=''\n"), 0o600)
	got, err := readEnvNames(p)
	if err != nil || !got["THEATARR_A"] || !got["THEATARR_B"] || got["THEATARR_EMPTY"] || got["THEATARR_Q"] || len(got) != 2 {
		t.Errorf("names %v %v", got, err)
	}
}
