package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/theatarr/theatarr/internal/auth"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

func TestStartupApplyRestore(t *testing.T) {
	t.Parallel()
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "hash mismatch"}[corrupt], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			db := filepath.Join(dir, "theatarr.db")
			policyDir := filepath.Join(dir, "policy")
			if !corrupt {
				if err := os.Mkdir(policyDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			st, _, _, pol := dbOnlyInstall(t, db, policyDir)
			now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			base, _, err := writeBackupSet(ctx, st, pol, filepath.Join(dir, "backups"), now)
			if err != nil {
				t.Fatal(err)
			}
			want, err := fingerprint(ctx, base+".db")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Writer().ExecContext(ctx, `UPDATE title SET name='Changed'`); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			old, err := fingerprint(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				f, err := os.OpenFile(base+".db", os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("bad hash")
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			req := restoreRequest{Set: filepath.Base(base), RequestedBy: "admin", RequestedAt: now, SafetySet: "safety"}
			requestPath := filepath.Join(dir, "restore-request.json")
			if err := writeRestoreJSON(requestPath, req); err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			result, err := applyRestoreRequest(ctx, log, io.Discard, filepath.Dir(base), db, "", now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || result.OK == corrupt {
				t.Fatalf("result: %+v", result)
			}
			if !corrupt {
				if _, err := os.Stat(result.PolicyOut); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(result.Message, "reference only") {
					t.Fatal(result.Message)
				}
			}
			if corrupt && !strings.Contains(result.Error, "does not match") {
				t.Fatal(result.Error)
			}
			got, err := fingerprint(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				want = old
			} else if _, err := os.Stat(result.KeptDatabase); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fingerprint: %v want %v", got, want)
			}
			for _, p := range []string{requestPath, filepath.Join(dir, "restore-request.taken.json")} {
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("request remains: %s %v", p, err)
				}
			}
			data, err := os.ReadFile(filepath.Join(dir, "restore-result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved web.RestoreResult
			if err := json.Unmarshal(data, &saved); err != nil || saved.OK != result.OK {
				t.Fatalf("saved: %s %v", data, err)
			}
			// A claimed request is not replayed; an already-swapped database is recognized.
			if err := writeRestoreJSON(filepath.Join(dir, "restore-request.taken.json"), req); err != nil {
				t.Fatal(err)
			}
			if again, err := applyRestoreRequest(ctx, log, io.Discard, filepath.Dir(base), db, "", now.Add(2*time.Minute)); err != nil || again == nil || again.OK == corrupt {
				t.Fatalf("replayed: %+v", again)
			}
			reopened, err := store.Open(ctx, db, store.Options{})
			if err != nil {
				t.Fatalf("startup cannot continue: %v", err)
			}
			defer reopened.Close()
			if _, err := reopened.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			q := jobs.NewQueue(reopened)
			for range 2 {
				if err := enqueueRestoredProviderSync(ctx, q, result); err != nil {
					t.Fatal(err)
				}
			}
			if err := enqueueRestoredProviderSync(ctx, q, nil); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := reopened.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job WHERE kind='provider.sync'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if corrupt {
				expected = 0
			}
			if count != expected {
				t.Fatalf("sync jobs %d want %d", count, expected)
			}
		})
	}
}

func TestServeBackupsRequestsAndList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, _, _, pol := dbOnlyInstall(t, db, filepath.Join(dir, "no-policy"))
	now := time.Now().UTC().Truncate(time.Second)
	b := &serveBackups{st: st, dir: filepath.Join(dir, "backups"), dbPath: db, source: "db", keep: 14, hour: 3, now: func() time.Time { return now }, policyNow: func(context.Context) *policy.Policy { return pol }}
	name, err := b.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	check := func(want int, name string) {
		t.Helper()
		_, err := b.RequestRestore(ctx, name, "admin")
		var e *web.BackupError
		if !errors.As(err, &e) || e.Status != want {
			t.Fatalf("want %d: %v", want, err)
		}
	}
	check(http.StatusNotFound, "theatarr-20000101-000000")
	check(http.StatusNotFound, "../escape")
	backupDir := b.dir
	b.dir = ""
	check(http.StatusConflict, name)
	b.dir = backupDir
	base := filepath.Join(b.dir, name)
	manifest, err := readManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	schema := manifest.Schema
	manifest.Schema = 999999
	if err := writeRestoreJSON(base+".manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	check(http.StatusUnprocessableEntity, name)
	manifest.Schema = schema
	if err := writeRestoreJSON(base+".manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	incomplete := "theatarr-20000101-000001"
	if err := os.WriteFile(filepath.Join(b.dir, incomplete+".db"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	check(http.StatusUnprocessableEntity, incomplete)
	list, err := b.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sets) != 2 || list.Sets[0].Name != name || !list.Sets[0].Restorable || list.Sets[1].Complete || list.Sets[1].Restorable || list.Sets[1].WhyNot == "" {
		t.Fatalf("list: %+v", list)
	}
	safety, err := b.RequestRestore(ctx, name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(filepath.Join(b.dir, safety)); err != nil {
		t.Fatalf("safety: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "restore-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req restoreRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	if req.Set != name || req.SafetySet != safety || req.RequestedBy != "admin" || !req.RequestedAt.Equal(now) {
		t.Fatalf("request: %+v", req)
	}
	check(http.StatusConflict, name)
}

func TestStartupRecoversInterruptedRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, _, _, _ := dbOnlyInstall(t, db, filepath.Join(dir, "policy"))
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	want, err := fingerprint(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(db, db+".pre-restore-20261102-030000"); err != nil {
		t.Fatal(err)
	}
	// The newest database wins; SQLite sidecars are moved with it, not mistaken for databases.
	if err := os.WriteFile(db+".pre-restore-20261101-030000", []byte("older copy"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(db+".pre-restore-20261102-030000"+suffix, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}

	taken := filepath.Join(dir, "restore-request.taken.json")
	if err := writeRestoreJSON(taken, restoreRequest{Set: "theatarr-20261102-020000"}); err != nil {
		t.Fatal(err)
	}
	result, err := applyRestoreRequest(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), io.Discard, "", db, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.OK || result.Error != "the previous restore was interrupted; Theatarr started on the database it had before" {
		t.Fatalf("result: %+v", result)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + suffix); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(db + ".pre-restore-20261102-030000" + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("sidecar remains: %v", err)
		}
	}

	got, err := fingerprint(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fingerprint: %v want %v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(dir, "restore-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved web.RestoreResult
	if err := json.Unmarshal(data, &saved); err != nil || saved.OK || saved.Error != result.Error {
		t.Fatalf("saved: %s %v", data, err)
	}
	if _, err := os.Stat(taken); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("taken remains: %v", err)
	}
}

func backupAPI(t *testing.T, b *serveBackups) (func(string, string, string) *httptest.ResponseRecorder, *atomic.Int32) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(context.Background(), b.st, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// The first start creates no account; the admin (id 1) is made here.
	if err := auth.ResetPassword(context.Background(), b.st, auth.AdminUsername, "test admin password"); err != nil {
		t.Fatal(err)
	}
	restarted := &atomic.Int32{}
	ui, err := web.New(web.Config{Store: b.st, Auth: au, Policy: b.policyNow(context.Background()), Logger: log, Backups: b, Restart: func() { restarted.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ui.Mount(mux)
	token, err := au.StartSession(context.Background(), 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, web.APIBase+"/system/backups"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		r.Header.Set(auth.CSRFHeader, au.SessionCSRF(token))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}, restarted
}

func TestRestoreRefusesPolicyOnlySet(t *testing.T) { testBrowserBackupRefusal(t, "policy") }
func TestRestoreRefusesIncompleteSet(t *testing.T) { testBrowserBackupRefusal(t, "incomplete") }
func testBrowserBackupRefusal(t *testing.T, kind string) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, _, _, pol := dbOnlyInstall(t, db, filepath.Join(dir, "no-policy"))
	now := time.Now().UTC().Truncate(time.Second)
	b := &serveBackups{st: st, dir: filepath.Join(dir, "backups"), dbPath: db, source: "db", now: func() time.Time { return now }, policyNow: func(context.Context) *policy.Policy { return pol }}
	name, err := b.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	base := filepath.Join(b.dir, name)
	wantStatus, wantMessage := 422, "This backup is incomplete"
	if kind == "yaml" {
		b.source = "yaml"
		wantStatus, wantMessage = 409, web.BackupYAMLNote
	} else {
		m, err := readManifest(base)
		if err != nil {
			t.Fatal(err)
		}
		m.Policy.State = policyFiles
		m.Policy.File = name + ".policy.tar"
		if err := writeRestoreJSON(base+".manifest.json", m); err != nil {
			t.Fatal(err)
		}
		if kind == "policy" {
			wantMessage = "This backup is from before settings were stored in Theatarr's database. Restore it from the command line (docs/restore.md)."
			copied, err := store.Open(ctx, base+".db", store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := copied.Writer().ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := copied.Writer().ExecContext(ctx, `DELETE FROM settings_generation`); err != nil {
				t.Fatal(err)
			}
			copied.Close()
			if err := os.WriteFile(base+".policy.tar", []byte("archive"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	request, restarted := backupAPI(t, b)
	if kind == "yaml" {
		if rec := request("POST", "", ""); rec.Code != 200 {
			t.Errorf("backup now: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := request("POST", "/"+name+"/restore", `{"confirm":"`+name+`"}`)
	if rec.Code != wantStatus || !strings.Contains(rec.Body.String(), wantMessage) {
		t.Errorf("restore: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "restore-request.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("request exists: %v", err)
	}
	rec = request("GET", "", "")
	var list web.BackupList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.Sets {
		if item.Name == name {
			found = true
		}
		if item.Name == name && (item.Restorable || item.WhyNot != wantMessage) {
			t.Errorf("list: %+v", item)
		}
	}
	if !found {
		t.Fatal("set missing from list")
	}
	time.Sleep(1100 * time.Millisecond)
	if restarted.Load() != 0 {
		t.Error("restart called")
	}
	if kind == "policy" {
		if err := writeRestoreJSON(filepath.Join(dir, "restore-request.json"), restoreRequest{Set: name}); err != nil {
			t.Fatal(err)
		}
		st.Close()
		result, err := applyRestoreRequest(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), io.Discard, b.dir, db, "", now)
		if err != nil {
			t.Fatal(err)
		}
		if result == nil || result.OK || result.Error != wantMessage {
			t.Fatalf("startup: %+v", result)
		}
	}
}

func TestStartupFinishesRestoreAfterSwap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, _, _, pol := dbOnlyInstall(t, db, filepath.Join(dir, "no-policy"))
	now := time.Now().UTC().Truncate(time.Second)
	base, _, err := writeBackupSet(ctx, st, pol, filepath.Join(dir, "backups"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `UPDATE title SET name='Changed'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	taken := filepath.Join(dir, "restore-request.taken.json")
	req := restoreRequest{Set: filepath.Base(base), RequestedBy: "admin", SafetySet: "safety", RequestedAt: now}
	if err := writeRestoreJSON(taken, req); err != nil {
		t.Fatal(err)
	}
	if err := restoreBackup(ctx, io.Discard, base, db, "", true, func(string) string { return "" }, now); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	result, err := applyRestoreRequest(ctx, log, io.Discard, filepath.Dir(base), db, "", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.OK || result.Error != "" || !strings.Contains(result.Message, "completed after an interrupted restart") || result.Set != req.Set || result.RequestedBy != req.RequestedBy || result.SafetySet != req.SafetySet {
		t.Fatalf("result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "restore-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved web.RestoreResult
	if err := json.Unmarshal(data, &saved); err != nil || !reflect.DeepEqual(&saved, result) {
		t.Fatalf("saved: %s %v", data, err)
	}
	if _, err := os.Stat(taken); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("taken remains: %v", err)
	}
	st, err = store.Open(ctx, db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := enqueueRestoredProviderSync(ctx, jobs.NewQueue(st), result); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job WHERE kind='provider.sync'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("sync jobs: %d", count)
	}
	if again, err := applyRestoreRequest(ctx, log, io.Discard, filepath.Dir(base), db, "", now.Add(2*time.Minute)); err != nil || again != nil {
		t.Fatalf("replayed: %+v %v", again, err)
	}
}

func TestBackupListIncludesManifestOnlySet(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, _, _, pol := dbOnlyInstall(t, db, filepath.Join(dir, "no-policy"))
	now := time.Now().UTC().Truncate(time.Second)
	b := &serveBackups{st: st, dir: filepath.Join(dir, "backups"), dbPath: db, source: "db", now: func() time.Time { return now }, policyNow: func(context.Context) *policy.Policy { return pol }}
	name, err := b.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	complete, err := b.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.dir, name+".db")); err != nil {
		t.Fatal(err)
	}
	request, _ := backupAPI(t, b)
	rec := request("GET", "", "")
	var list web.BackupList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(list.Sets) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if list.Sets[0].Name != complete || !list.Sets[0].Restorable {
		t.Fatalf("complete set: %+v", list.Sets[0])
	}
	item := list.Sets[1]
	if item.Name != name || item.Complete || item.Restorable || item.WhyNot != "This backup is incomplete" {
		t.Fatalf("incomplete set: %+v", item)
	}
}

// browserRestore requests a restore of set as the System › Backup page does and applies it as serve's next start does,
// with the configured policy directory policyDir. It returns the result and asserts the live database is unchanged
// when the restore was refused.
func browserRestore(t *testing.T, set, dbPath, policyDir string) *web.RestoreResult {
	t.Helper()
	ctx := context.Background()
	before, err := fingerprint(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := writeRestoreJSON(filepath.Join(filepath.Dir(dbPath), "restore-request.json"), restoreRequest{Set: filepath.Base(set), RequestedBy: "admin", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	result, err := applyRestoreRequest(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), io.Discard, filepath.Dir(set), dbPath, policyDir, now)
	if err != nil || result == nil {
		t.Fatalf("apply: %+v %v", result, err)
	}
	if result.OK {
		return result
	}
	after, err := fingerprint(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := filepath.Glob(dbPath + ".pre-restore-*"); !reflect.DeepEqual(before, after) || len(kept) != 0 {
		t.Fatalf("refused restore changed the database: kept %v", kept)
	}
	return result
}

// liveDatabase is the database a running service has: migrated, with no settings.
func liveDatabase(t *testing.T, dir string) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(dir, "live", "theatarr.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return path
}

// The browser never restores a legacy set (one whose settings load only over its policy files): that needs the
// command-line restore and its settings import. It is refused before anything is replaced.
func TestBrowserRestoreRefusesLegacySet(t *testing.T) {
	dir := t.TempDir()
	set := legacyCheckpoint(t, dir)
	result := browserRestore(t, set, liveDatabase(t, dir), filepath.Join(dir, "configured-policy"))
	want := "the restored settings (generation 2) do not load with the policy directory this service uses (" +
		filepath.Join(dir, "configured-policy") + "): views[radarr]: view metadata is missing from the policy files; restore it " +
		"from the command line (docs/restore.md) to use the archived policy files; nothing was restored"
	if result.OK || result.Error != want {
		t.Fatalf("result: %+v", result)
	}
}

// The restored settings must load with the policy directory the service runs with (-policy), not the archived copy:
// a relative overrides_dir missing there refuses the restore before anything is replaced.
func TestBrowserRestoreValidatesConfiguredPolicyDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	polDir := importPolicyDir(t, root)
	dbPath := filepath.Join(root, "theatarr.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", polDir, "-db", dbPath, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polDir, "overrides", "franchises.yaml"), []byte("franchises: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := databasePolicyBase(polDir)
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	live := liveDatabase(t, root)
	configured := filepath.Join(root, "configured-policy")
	if err := os.MkdirAll(configured, 0o700); err != nil {
		t.Fatal(err)
	}
	result := browserRestore(t, set, live, configured)
	want := "the restored settings (generation 1) do not load with the policy directory this service uses (" + configured + "): " +
		"metadata.overrides_dir: stat " + filepath.Join(configured, "overrides") + ": no such file or directory; restore it from the command line (docs/restore.md) to use the archived policy files; nothing was restored"
	if result.OK || result.Error != want {
		t.Fatalf("result: %+v", result)
	}
	if err := os.MkdirAll(filepath.Join(configured, "overrides"), 0o700); err != nil {
		t.Fatal(err)
	}
	if result := browserRestore(t, set, live, configured); !result.OK || result.Message != "Restored database. It runs with this "+
		"service's policy directory ("+configured+"); the archived policy files in "+result.PolicyOut+" are for reference only." {
		t.Fatalf("result: %+v", result)
	}
}

// databaseSet imports importPolicyDir's files into a database and backs it up over that policy directory, after edit
// (when set) changed the directory. It returns the set's base.
func databaseSet(t *testing.T, root string, edit func(polDir string)) string {
	t.Helper()
	ctx := context.Background()
	polDir := importPolicyDir(t, root)
	dbPath := filepath.Join(root, "theatarr.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", polDir, "-db", dbPath, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(polDir)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := databasePolicyBase(polDir)
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// The browser restore checks the restored settings against the policy directory the service runs with, only: an
// archive without the overrides directory is restored when the configured directory has it, and refused, with the
// database unchanged, when the configured directory lacks it.
func TestBrowserRestoreChecksOnlyTheConfiguredDirectory(t *testing.T) {
	root := t.TempDir()
	set := databaseSet(t, root, func(polDir string) {
		if err := os.Remove(filepath.Join(polDir, "overrides")); err != nil {
			t.Fatal(err)
		}
	})
	if m, err := readManifest(set); err != nil || strings.Contains(strings.Join(m.Policy.Files, ","), "overrides") {
		t.Fatalf("the archive has overrides: %+v %v", m, err)
	}
	live := liveDatabase(t, root)
	configured := filepath.Join(root, "configured-policy")
	if err := os.MkdirAll(configured, 0o700); err != nil {
		t.Fatal(err)
	}
	result := browserRestore(t, set, live, configured)
	want := "the restored settings (generation 1) do not load with the policy directory this service uses (" + configured + "): " +
		"metadata.overrides_dir: stat " + filepath.Join(configured, "overrides") + ": no such file or directory; restore it from the " +
		"command line (docs/restore.md) to use the archived policy files; nothing was restored"
	if result.OK || result.Error != want {
		t.Fatalf("configured directory without overrides: %+v", result)
	}
	if err := os.MkdirAll(filepath.Join(configured, "overrides"), 0o700); err != nil {
		t.Fatal(err)
	}
	if result := browserRestore(t, set, live, configured); !result.OK {
		t.Fatalf("configured directory with overrides: %+v", result)
	}
}
