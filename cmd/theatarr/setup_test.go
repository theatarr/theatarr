package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/preflight"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
	"github.com/theatarr/theatarr/internal/web"
)

// A fresh install: no policy directory and an empty data directory. serve starts, serves the UI and the API, and
// says what is missing (the SetupCheck notice, GET /setup, the preflight) instead of exiting; nothing that could
// grab, write or add is on.
func TestServeStartsWithNothingConfigured(t *testing.T) {
	for _, env := range []string{"THEATARR_POLICY", "THEATARR_DB", "THEATARR_TRASH", "THEATARR_SETTINGS_SOURCE"} {
		t.Setenv(env, "")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{"-db", filepath.Join(data, "theatarr.db"), "-policy", filepath.Join(dir, "policy"),
			"-listen", addr, "-backup-dir", "off", "-mount-helper=false", "-log-level", "warn"})
	}()
	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("serve exited: %v", err)
		default:
		}
		// The port was free when picked, but until serve binds it another test process may take it: ask only once
		// this process listens there, so no request lands on another test's server.
		var resp *http.Response
		err := errNotListening
		if listensHere(t, addr) {
			resp, err = http.Get(base + "/readyz")
		}
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("serve never became ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("serve did not stop")
		}
	}()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	call := func(method, path, csrf string, body any, out any) int {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		if csrf != "" {
			req.Header.Set(auth.CSRFHeader, csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
		}
		return resp.StatusCode
	}
	// The UI is served.
	if code := call("GET", "/", "", nil, nil); code != http.StatusOK {
		t.Fatalf("GET / = %d", code)
	}
	// The first start made no account and wrote no password file: the first visit creates the admin.
	if _, err := os.Stat(filepath.Join(data, auth.InitialPasswordFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("password file on a new install: %v", err)
	}
	var first web.FirstAccountStatus
	if code := call("GET", web.APIBase+"/auth/first-account", "", nil, &first); code != http.StatusOK ||
		!first.NeedsAccount || first.PasswordPending {
		t.Fatalf("first-account = %d %+v", code, first)
	}
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	if code := call("POST", web.APIBase+"/auth/first-account", "", map[string]string{"username": auth.AdminUsername,
		"password": "my own admin password"}, &login); code != http.StatusOK || login.CSRF == "" {
		t.Fatalf("create admin = %d", code)
	}
	var setup web.SetupState
	if code := call("GET", web.APIBase+"/setup", "", nil, &setup); code != http.StatusOK {
		t.Fatalf("GET setup = %d", code)
	}
	// The account step is done: the password was chosen on the create page.
	if !setup.FirstRun || setup.SettingsSource != "db" || setup.AdminPasswordGenerated || setup.Versions != 0 ||
		len(setup.Missing) != 5 {
		t.Fatalf("setup = %+v", setup)
	}
	var hl struct {
		Checks []struct{ Source, Type, Message string }
	}
	if code := call("GET", web.APIBase+"/health", "", nil, &hl); code != http.StatusOK {
		t.Fatalf("GET health = %d", code)
	}
	found := false
	for _, c := range hl.Checks {
		if c.Source == "SetupCheck" {
			found = c.Type == "notice" && strings.Contains(c.Message, "no libraries")
		}
	}
	if !found {
		t.Fatalf("no SetupCheck notice: %+v", hl.Checks)
	}
	var report preflight.Report
	if code := call("POST", web.APIBase+"/preflight", login.CSRF, map[string]bool{"sources": false}, &report); code != http.StatusOK {
		t.Fatalf("POST preflight = %d", code)
	}
	statuses := map[string]preflight.Status{}
	for _, it := range report.Items {
		statuses[it.Area+"/"+it.Check] = it.Status
	}
	for check, want := range map[string]preflight.Status{"versions/versions": preflight.Warn, "providers/providers": preflight.Warn,
		"indexers/indexers": preflight.Warn, "metadata/tmdb": preflight.Fail, "library/mount": preflight.Warn,
		"switches/pause": preflight.OK} {
		if statuses[check] != want {
			t.Errorf("%s = %q, want %q (%+v)", check, statuses[check], want, report.Items)
		}
	}
	// The features that are off say so instead of acting: TMDb search is "off", and an add has no version to take it.
	var search struct {
		State string `json:"tmdb_state"`
	}
	if code := call("GET", web.APIBase+"/search?q=alien", "", nil, &search); code != http.StatusOK || search.State != "off" {
		t.Errorf("search = %d, tmdb_state %q", code, search.State)
	}
	var refused struct {
		Error string `json:"error"`
	}
	if code := call("POST", web.APIBase+"/titles", login.CSRF, map[string]any{"kind": "movie", "tmdb_id": 348}, &refused); code != http.StatusBadRequest ||
		!strings.Contains(refused.Error, "no library") {
		t.Errorf("add = %d %q", code, refused.Error)
	}
	// Nothing that could act is on: Pause is on and there is no version to grab for.
	for _, it := range report.Items {
		if it.Area == preflight.AreaSwitches && !strings.HasPrefix(it.Why, "on: ") {
			t.Errorf("switch on in a fresh install: %+v", it)
		}
	}
}

// theatarr preflight on a fresh install: no database and no policy are not errors; the report says what is missing
// and the command exits non-zero for the one failing check (no TMDb key).
func TestPreflightCommandFreshInstall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var out bytes.Buffer
	err := runPreflight([]string{"-db", filepath.Join(dir, "theatarr.db"),
		"-settings-source", "db", "-json", "-sources=false"}, &out)
	if err == nil || !strings.Contains(err.Error(), "1 check(s) failed") {
		t.Fatalf("err = %v", err)
	}
	var r preflight.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if r.Fail != 1 || r.Items[0].Area != preflight.AreaVersions || r.Items[0].Status != preflight.Warn {
		t.Fatalf("report = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "theatarr.db")); !os.IsNotExist(err) {
		t.Fatalf("preflight created the database: %v", err)
	}
	out.Reset()
	_ = runPreflight([]string{"-db", filepath.Join(dir, "theatarr.db"), "-sources=false"}, &out)
	if !strings.Contains(out.String(), "FAIL  metadata") || !strings.Contains(out.String(), "next: ") {
		t.Fatalf("text report:\n%s", out.String())
	}
}

var errNotListening = errors.New("not listening yet")

// listensHere reports whether this process has a listening TCP socket on addr: a socket inode among its open files
// (/proc/self/fd) that /proc/self/net/tcp lists in state LISTEN on addr's port. Without procfs it reports true.
func listensHere(t *testing.T, addr string) bool {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	table, err := os.ReadFile("/proc/self/net/tcp")
	if err != nil {
		return true
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return true
	}
	own := map[string]bool{}
	for _, fd := range fds {
		if l, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && strings.HasPrefix(l, "socket:[") {
			own[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = true
		}
	}
	want := fmt.Sprintf(":%04X", port)
	for _, line := range strings.Split(string(table), "\n")[1:] {
		f := strings.Fields(line)
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		if len(f) > 9 && strings.HasSuffix(f[1], want) && f[3] == "0A" && own[f[9]] {
			return true
		}
	}
	return false
}

// The CLI checks the running set, which drops disabled records; with none enabled, the line names the disabled ones.
func TestPreflightNamesDisabledSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte("router:\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.db")
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Bootstrap(ctx, base, settings.ImportRequest{Dir: dir, Actor: "test", Lookup: func(string) (string, bool) { return "", false }}); err != nil {
		t.Fatal(err)
	}
	snap, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	// A provider added without its key, and an indexer, both left disabled.
	snap.Records.Providers = append(snap.Records.Providers, settings.Provider{Record: settings.Record{Key: "realdebrid"},
		Config: policy.Provider{Kind: "realdebrid", Order: 1, URL: "https://api.real-debrid.com/rest/1.0"}})
	snap.Records.Indexers = append(snap.Records.Indexers, settings.Indexer{Record: settings.Record{Key: "feed"},
		Config: policy.Indexer{Kind: "torznab", URL: "http://prowlarr:9696/1/api"}})
	if _, err := st.Save(ctx, settings.SaveRequest{Base: base, ExpectedGeneration: snap.Generation, Records: snap.Records, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_ = runPreflight([]string{"-settings-source", "db", "-db", path, "-sources=false", "-json"}, &out)
	var report preflight.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("no report: %v; %s", err, out.String())
	}
	want := map[string][2]string{
		"providers/providers": {"no enabled debrid provider: realdebrid is added but disabled", uipages.Providers},
		"indexers/indexers":   {"no enabled indexer: feed is added but disabled", uipages.Indexers},
	}
	for _, it := range report.Items {
		if w, ok := want[it.Area+"/"+it.Check]; ok {
			if it.Why != w[0] || !strings.Contains(it.Next, "enable it in "+w[1]) {
				t.Errorf("%s/%s: %q, next %q", it.Area, it.Check, it.Why, it.Next)
			}
			delete(want, it.Area+"/"+it.Check)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing %v in %+v", want, report.Items)
	}
}

func TestPreflightInvalidInitializedSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	overrides := filepath.Join(dir, "overrides")
	if err := os.Mkdir(overrides, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte("metadata:\n  overrides_dir: overrides\nrouter:\n  enabled: true\nindexers:\n  disabled:\n    kind: torznab\n    url: http://127.0.0.1:1\n    api_key_env: TEST_DISABLED_KEY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.db")
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Bootstrap(ctx, base, settings.ImportRequest{Dir: dir, Actor: "test", Lookup: func(string) (string, bool) { return "fixture", true }}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BootstrapSections(ctx, base, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BootstrapSwitches(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	snap, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	snap.Records.Indexers[0].Enabled = false
	if _, err := st.Save(ctx, settings.SaveRequest{Base: base, ExpectedGeneration: snap.Generation, Records: snap.Records, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("THEATARR_FORCE_OFF", "router")
	t.Setenv("TEST_DISABLED_KEY", "")
	if err := os.Remove(overrides); err != nil {
		t.Fatal(err)
	}
	// The service's normal loader must keep rejecting this stored generation.
	if snap, err := st.Load(ctx, base); err == nil || snap != nil {
		t.Fatalf("strict Load = %v, %v", snap, err)
	}
	if _, snap, pol, err := loadServePolicy(ctx, "db", dir, base, db); err == nil || snap != nil || pol != nil {
		t.Fatalf("serve accepted invalid settings: %v", err)
	}
	var out bytes.Buffer
	err = runPreflight([]string{"-settings-source", "db", "-db", path, "-sources=false", "-json"}, &out)
	if err == nil {
		t.Fatal("invalid settings succeeded")
	}
	var report preflight.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("no report: %v; %s", err, out.String())
	}
	validation, switches := false, false
	for _, item := range report.Items {
		if strings.Contains(item.Check, "disabled") {
			t.Fatalf("disabled source in report: %+v", item)
		}
		if item.Check == "validation" && item.Status == preflight.Fail && strings.Contains(item.Why, "metadata.overrides_dir") {
			validation = true
		}
		if item.Area == preflight.AreaSwitches && item.Check == "pause" && item.Status == preflight.OK && item.Why == "on, forced by environment (THEATARR_FORCE_OFF): nothing is searched, downloaded or linked" {
			switches = true
		}
	}
	if !validation || !switches {
		t.Fatalf("incomplete report: %+v", report)
	}
}
