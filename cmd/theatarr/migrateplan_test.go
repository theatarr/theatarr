package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/web"
)

const (
	cliArrKey     = "cli-radarr-key-0123456789"
	cliServiceKey = "cli-theatarr-key-0123456789"
	cliRDKey      = "cli-rd-key-0123456789"
)

// cliRadarr answers one Radarr from the migration fixtures with GET only.
func cliRadarr(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("radarr got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Api-Key") != cliArrKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ep := strings.TrimPrefix(r.URL.Path, "/api/v3/")
		file := filepath.Join("..", "..", "internal", "migration", "testdata", "stack", "radarr_uhd", strings.ReplaceAll(ep, "/", "_")+".json")
		switch ep {
		case "qualityprofile", "customformat", "qualitydefinition":
			file = filepath.Join("..", "..", "internal", "decide", "testdata", "arr", "radarr_4k_debrid", "snapshot", ep+".json")
		case "config/naming":
			w.Write([]byte(`{"renameMovies": true, "standardMovieFormat": "{Movie CleanTitle} ({Release Year})", "movieFolderFormat": "{Movie CleanTitle} ({Release Year})"}`))
			return
		}
		b, err := os.ReadFile(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cliService stands in for theatarr serve's /migrate API and records what the CLI sent.
type cliService struct {
	*httptest.Server
	bodies []string
	paths  []string
	plan   migration.Plan
	report migration.VerifyReport
}

func newCLIService(t *testing.T) *cliService {
	s := &cliService{}
	s.plan = migration.Plan{SchemaVersion: migration.SchemaVersion, ID: 7, Name: "cli", Steps: []migration.Step{
		{ID: "provider/realdebrid", Kind: migration.KindProvider, What: "add realdebrid, disabled", Why: "the arr's client",
			Reversible: true, Automatic: true, Status: migration.StatusPending,
			Payload: &migration.Payload{Key: "realdebrid", Secret: "CLI_RD_KEY"}},
		{ID: "provider/torbox", Kind: migration.KindProvider, What: "add torbox, disabled", Why: "asked for", Reversible: true,
			Automatic: true, Status: migration.StatusPending, Payload: &migration.Payload{Key: "torbox", Secret: "CLI_UNSET_KEY"}},
		{ID: "import/radarr_uhd", Kind: migration.KindManual, What: "theatarr import-arr", Why: "titles", Status: migration.StatusManual},
	}, Gaps: []migration.Gap{{Topic: migration.GapUnsupported, Subject: "radarr_uhd", What: "delay profiles", Why: "no equivalent"}}}
	s.report = migration.VerifyReport{SchemaVersion: migration.SchemaVersion, PlanID: 7, Versions: []migration.VersionReadiness{{
		Key: "movie-uhd", Instance: "radarr_uhd", Checks: []migration.Check{{Name: "plan", Status: golive.Fail,
			Message: "1 step not done", Fix: "theatarr migrate apply -plan 7"}}, Cutover: []string{"turn off Radarr's RSS sync"},
		Rollback: []string{"theatarr backup restore"}}}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(auth.APIKeyHeader) != cliServiceKey {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"sign in"}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.bodies = append(s.bodies, string(b))
		s.paths = append(s.paths, r.Method+" "+strings.TrimPrefix(r.URL.Path, web.APIBase))
		var out any
		switch r.Method + " " + strings.TrimPrefix(r.URL.Path, web.APIBase+"/migrate") {
		case "POST /plans":
			var req web.MigratePlanRequest
			if err := json.Unmarshal(b, &req); err != nil || req.Inventory == nil || req.Sources != nil || len(req.Keys) > 0 {
				t.Errorf("plan request %s %v", b, err)
			}
			w.WriteHeader(http.StatusCreated)
			out = web.MigratePlan{Plan: &s.plan, Inventory: req.Inventory}
		case "GET /plans":
			out = []migration.Summary{{ID: 7, Name: "cli", Steps: map[string]int{"pending": 2, "manual": 1}}}
		case "GET /plans/7":
			out = web.MigratePlan{Plan: &s.plan}
		case "POST /plans/7/apply":
			p := s.plan
			p.Steps = append([]migration.Step(nil), p.Steps...)
			p.Steps[0].Status = migration.StatusDone
			p.Steps[1].Status = migration.StatusFailed
			p.Steps[1].Error = "CLI_UNSET_KEY is not set"
			out = p
		case "POST /plans/7/verify":
			out = s.report
		default:
			w.WriteHeader(http.StatusNotFound)
			out = map[string]string{"error": "not found"}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(s.Close)
	return s
}

func runMigrationCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runMigration(args, &out)
	return out.String(), err
}

func TestMigrateCLISourcesAndOptions(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "sources.yaml")
	os.WriteFile(srcFile, []byte("arr:\n  - {name: sonarr_anime, flavour: sonarr, url: 'http://192.168.1.5:8989', key: SONARR_KEY}\n"), 0o600)
	f := newMigrationFlags("inspect")
	if err := f.fs.Parse([]string{"-sources", srcFile, "-arr", "radarr_uhd=radarr,http://192.168.1.6:7878,RADARR_KEY",
		"-prowlarr", "http://192.168.1.7:9696,PROWLARR_KEY", "-seerr", "http://192.168.1.8:5055,SEERR_KEY"}); err != nil {
		t.Fatal(err)
	}
	src, err := f.sources()
	if err != nil || len(src.Arr) != 2 || src.Arr[1].Key != "RADARR_KEY" || src.Prowlarr.Key != "PROWLARR_KEY" ||
		src.Seerr.URL != "http://192.168.1.8:5055" {
		t.Fatalf("sources %+v %v", src, err)
	}
	for _, bad := range [][]string{{"-arr", "radarr_uhd=radarr,http://x"}, {"-prowlarr", "http://192.168.1.7:9696"}} {
		f := newMigrationFlags("inspect")
		f.fs.Parse(bad)
		if _, err := f.sources(); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	os.WriteFile(srcFile, []byte("arr: []\nkeys: {X: secret}\n"), 0o600)
	f = newMigrationFlags("inspect")
	f.fs.Parse([]string{"-sources", srcFile})
	if _, err := f.sources(); err == nil {
		t.Error("a sources file with an unknown field (key values) was accepted")
	}

	f = newMigrationFlags("plan")
	f.fs.Parse([]string{"-name", "n", "-native", "a, b", "-provider", "torbox:tb-migrated=TB_KEY", "-provider", "realdebrid=RD_KEY",
		"-path-map", "/movies=/mnt/media/movies"})
	o, err := f.planOptions()
	if err != nil || len(o.Native) != 2 || o.Providers[0] != (migration.ProviderChoice{Kind: "torbox", Key: "tb-migrated", Secret: "TB_KEY"}) ||
		o.Providers[1].Key != "" || o.PathMap[0].To != "/mnt/media/movies" {
		t.Fatalf("options %+v %v", o, err)
	}
	for _, bad := range [][]string{{"-provider", "realdebrid"}, {"-path-map", "movies=/x"}} {
		f := newMigrationFlags("plan")
		f.fs.Parse(bad)
		if _, err := f.planOptions(); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := runMigrationCLI(t, "show"); err == nil || !strings.Contains(err.Error(), "-plan") {
		t.Errorf("show without -plan: %v", err)
	}
	if !isMigrationCommand([]string{"verify"}) || isMigrationCommand([]string{"up"}) || isMigrationCommand(nil) {
		t.Error("dispatch")
	}
}

func TestMigrateCLIInspectAndPlanKeepKeysLocal(t *testing.T) {
	radarr := cliRadarr(t)
	dial := migrationDialer
	migrationDialer = migration.NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 1,
		Backoff: time.Millisecond})
	t.Cleanup(func() { migrationDialer = dial })
	svc := newCLIService(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "secrets.env")
	os.WriteFile(envFile, []byte("CLI_RADARR_KEY="+cliArrKey+"\n"), 0o600)
	t.Cleanup(func() { os.Unsetenv("CLI_RADARR_KEY") })
	arrFlag := "radarr_uhd=radarr," + radarr.URL + ",CLI_RADARR_KEY"

	if _, err := runMigrationCLI(t, "inspect", "-arr", arrFlag); err == nil || !strings.Contains(err.Error(), "CLI_RADARR_KEY") {
		t.Fatalf("inspect without the key: %v", err)
	}
	human, err := runMigrationCLI(t, "inspect", "-env-file", envFile, "-arr", arrFlag)
	if err != nil || !strings.Contains(human, "radarr_uhd (radarr") || !strings.Contains(human, "titles: 3") {
		t.Fatalf("inspect: %v\n%s", err, human)
	}
	// Connect targets and lists print with their host only; the fixture's tokens, passwords and keys never.
	for _, want := range []string{"connect Plex (PlexServer, on Download, MovieDelete, Rename, Upgrade) http://plex:32400",
		"connect Discord grabs (Discord, on Grab) https://discord.com", "connect usenet-bridge (Webhook, no triggers) http://usenet-bridge:8080",
		"list radarr_4k_web (RadarrImport, enabled, auto add true) http://radarr4kweb:7878",
		"list Watchlist (PlexRssImport, disabled, auto add false) https://rss.plex.tv",
		"connect Signal (Signal, on HealthIssue) https://signal.example:8080", "connect Mail (Email, on Download) smtp.example.com:587"} {
		if !strings.Contains(human, want) {
			t.Errorf("inspect output lacks %q:\n%s", want, human)
		}
	}
	for _, secret := range []string{"plex-token", "discord-hook-token", "hookpass", "webhook-key", "list-key", "plex-rss-secret",
		"/api/webhooks", "apikey", "sigpass", "sigUser", "mailpass"} {
		if strings.Contains(human, secret) {
			t.Errorf("inspect output holds %q:\n%s", secret, human)
		}
	}
	invFile := filepath.Join(dir, "inventory.json")
	if _, err := runMigrationCLI(t, "inspect", "-arr", arrFlag, "-json", "-raw", "-o", invFile); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(invFile)
	var inv migration.Inventory
	if err := json.Unmarshal(b, &inv); err != nil || inv.SchemaVersion != migration.SchemaVersion || inv.Arr[0].Raw == nil {
		t.Fatalf("inventory: %v", err)
	}

	t.Setenv("THEATARR_API_KEY", "")
	if _, err := runMigrationCLI(t, "list", "-service", svc.URL); err == nil || !strings.Contains(err.Error(), "THEATARR_API_KEY") {
		t.Fatalf("list without an API key: %v", err)
	}
	t.Setenv("THEATARR_API_KEY", cliServiceKey)
	out, err := runMigrationCLI(t, "plan", "-arr", arrFlag, "-service", svc.URL, "-name", "cli")
	if err != nil || !strings.Contains(out, `plan 7 "cli"`) || !strings.Contains(out, "NOT reversible") ||
		!strings.Contains(out, "not migrated automatically") {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if _, err := runMigrationCLI(t, "plan", "-inventory", invFile, "-service", svc.URL, "-json"); err != nil {
		t.Fatal(err)
	}
	out, err = runMigrationCLI(t, "list", "-service", svc.URL)
	if err != nil || !strings.Contains(out, "7\t") || !strings.Contains(out, "1 manual, 2 pending") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	all := human + out + string(b) + strings.Join(svc.bodies, "\n")
	if strings.Contains(all, cliArrKey) {
		t.Error("the arr key reached the output, the inventory file or Theatarr")
	}
}

func TestMigrateCLIApplyAndVerify(t *testing.T) {
	svc := newCLIService(t)
	t.Setenv("THEATARR_API_KEY", cliServiceKey)
	t.Setenv("CLI_RD_KEY", cliRDKey)
	t.Setenv("CLI_OTHER_KEY", "never-sent-0123456789")

	out, err := runMigrationCLI(t, "apply", "-plan", "7", "-service", svc.URL, "-step", "provider/realdebrid")
	if err == nil || !strings.Contains(err.Error(), "provider/torbox (failed)") || !strings.Contains(out, "[done] provider/realdebrid") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	var req web.MigrateApplyRequest
	if err := json.Unmarshal([]byte(svc.bodies[len(svc.bodies)-1]), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Keys) != 1 || req.Keys["CLI_RD_KEY"] != cliRDKey || req.Steps[0] != "provider/realdebrid" || len(req.Confirm) != 0 {
		t.Errorf("apply request %+v", req)
	}
	if strings.Contains(out, cliRDKey) {
		t.Error("apply printed a key")
	}

	out, err = runMigrationCLI(t, "verify", "-plan", "7", "-service", svc.URL)
	if err == nil || !strings.Contains(err.Error(), "not ready: movie-uhd") || !strings.Contains(out, "NOT READY") ||
		!strings.Contains(out, "1. turn off Radarr's RSS sync") || !strings.Contains(out, "fix: theatarr migrate apply") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	svc.report.Versions[0].Ready = true
	out, err = runMigrationCLI(t, "verify", "-plan", "7", "-service", svc.URL, "-json")
	var rep migration.VerifyReport
	if err != nil || json.Unmarshal([]byte(out), &rep) != nil || rep.SchemaVersion != migration.SchemaVersion || !rep.Versions[0].Ready {
		t.Fatalf("verify -json: %v\n%s", err, out)
	}

	if _, err := runMigrationCLI(t, "show", "-plan", "8", "-service", svc.URL); err == nil || !strings.Contains(err.Error(), "not found (404)") {
		t.Errorf("unknown plan: %v", err)
	}
	if _, err := runMigrationCLI(t, "list", "-service", "http://8.8.8.8:7700"); err == nil || !strings.Contains(err.Error(), "LAN") {
		t.Errorf("a public -service: %v", err)
	}
	t.Setenv("THEATARR_API_KEY", "wrong")
	if _, err := runMigrationCLI(t, "show", "-plan", "7", "-service", svc.URL); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad key: %v", err)
	}
}
