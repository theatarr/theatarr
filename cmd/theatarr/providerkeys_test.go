package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// TestCommandsUseStoredProviderKeys: a provider key kept in the settings database (as the web UI stores it) counts
// as set for every command that reads provider keys, with nothing in the environment; a record that never had a
// stored key still reads its env variable as before.
func TestCommandsUseStoredProviderKeys(t *testing.T) {
	for _, env := range []string{"THEATARR_RD_KEY", "THEATARR_AD_KEY", "THEATARR_TB_KEY"} {
		t.Setenv(env, "")
		os.Unsetenv(env)
	}
	ctx := context.Background()
	polDir := filepath.Join("..", "..", "examples", "policy")
	db := filepath.Join(t.TempDir(), "theatarr.db")
	// The fixture stores each provider's key in the database ("fixture-<env name>").
	commandSettingsArgs(t, []string{"-db", db, "-policy", polDir})
	st, err := store.Open(ctx, db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// ad: a record from before stored keys, with only its env variable.
	if _, err := st.Writer().ExecContext(ctx, `UPDATE setting_provider SET secret_id = NULL WHERE key = 'ad'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("THEATARR_AD_KEY", "env-ad")

	pol, snap, err := loadSettingsSnapshot(ctx, "db", polDir, db)
	if err != nil {
		t.Fatal(err)
	}
	keys := providerKeys(pol, snap)
	if keys["rd"] != "fixture-THEATARR_RD_KEY" || keys["tb"] != "fixture-THEATARR_TB_KEY" || keys["ad"] != "env-ad" {
		t.Fatalf("keys not resolved as serve does (values hidden): rd=%t tb=%t ad=%t", keys["rd"] != "",
			keys["tb"] != "", keys["ad"] == "env-ad")
	}
	if nosnap := providerKeys(pol, nil); nosnap["rd"] != "" || nosnap["ad"] != "env-ad" {
		t.Fatal("without a snapshot only the environment counts")
	}

	// providers sync / status -account / torrents.
	e, err := openProviderEnv(ctx, db, polDir, "", false, false, "db")
	if err != nil {
		t.Fatal(err)
	}
	// Without the gateway no client is built; the keys the clients and torrents' adders take are resolved.
	if e.keys["rd"] == "" || e.keys["ad"] != "env-ad" || e.keys["tb"] == "" {
		t.Error("provider env: stored keys not resolved")
	}
	e.Close()
	// With the gateway the read clients are built from those keys (nothing is sent here).
	if e, err = openProviderEnv(ctx, db, polDir, "", false, true, "db"); err != nil {
		t.Fatal(err)
	}
	if len(e.missing) != 0 || e.clients["rd"] == nil || e.clients["ad"] == nil || e.clients["tb"] == nil {
		t.Errorf("provider clients: missing %v", e.missing)
	}
	e.Close()

	// router dry-run reads the snapshot its engine loaded.
	df := decideFlags{settingsSource: "db", policyDir: polDir, db: db}
	if _, _, err := df.engine("movie-remux", io.Discard); err != nil {
		t.Fatal(err)
	}
	if providerKeys(pol, df.snap)["rd"] == "" {
		t.Error("router dry-run: the stored key is not seen")
	}
	// Its search service gets the indexer keys stored in the database (the fixture stores prowlarr-public's).
	if ik := indexerKeys(pol, df.snap); ik["prowlarr-public"] != "fixture-THEATARR_PROWLARR_KEY" {
		t.Errorf("router dry-run indexer keys: prowlarr-public set=%t", ik["prowlarr-public"] != "")
	}

	// router status.
	var out bytes.Buffer
	if err := runRouter([]string{"status", "-db", db, "-json"}, &out, nil); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Providers []struct {
			Key    string `json:"key"`
			KeySet bool   `json:"key_set"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, p := range status.Providers {
		set[p.Key] = p.KeySet
	}
	if !set["rd"] || !set["ad"] || !set["tb"] {
		t.Errorf("router status key_set: %v", set)
	}

	// health.
	syncCheck := func(name string, msg string) {
		if msg == "" || strings.Contains(msg, "No debrid provider keys set") {
			t.Errorf("%s: ProviderSyncCheck %q", name, msg)
		}
	}
	out.Reset()
	_ = runHealth([]string{"-db", db, "-backup-dir", "off", "-json"}, &out)
	var hr struct {
		Checks []struct {
			Source  string `json:"source"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &hr); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	msg := ""
	for _, c := range hr.Checks {
		if c.Source == "ProviderSyncCheck" {
			msg = c.Message
		}
	}
	syncCheck("health", msg)

	// backup: rd's key is in the database (and so in the backup), not an env variable to set; ad reads its env
	// variable and is listed as before.
	_, m, err := writeBackupSet(ctx, st, pol, t.TempDir(), time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]backupSecret{}
	for _, s := range m.Secrets {
		listed[s.Env] = s
		if settingsHeldSecret(s.Env) {
			t.Errorf("backup lists %s as an env secret", s.Env)
		}
	}
	if _, ok := listed["THEATARR_RD_KEY"]; ok {
		t.Error("a key stored in the database is listed as an env secret")
	}
	if ad, ok := listed["THEATARR_AD_KEY"]; !ok || !ad.Set {
		t.Errorf("the env-only provider: %+v", listed)
	}
}

func TestProviderKeyUnsetAndBackupWording(t *testing.T) {
	if got := providerKeyUnset(&policy.Provider{APIKeyEnv: settings.CredentialSentinel}); strings.Contains(got, settings.CredentialSentinel) {
		t.Errorf("sentinel named as an env variable: %q", got)
	}
	if got := providerKeyUnset(&policy.Provider{APIKeyEnv: "THEATARR_RD_KEY"}); got != "THEATARR_RD_KEY is not set" {
		t.Errorf("env provider: %q", got)
	}
	var out bytes.Buffer
	// An older manifest still lists the placeholder and derived names: they are not shown as MISSING.
	printSecrets(&out, []backupSecret{{Env: settings.CredentialSentinel}, {Env: policy.SettingsSecretPrefix + "VIEWS_X"},
		{Env: "THEATARR_TMDB_KEY", Set: true}}, nil)
	s := out.String()
	if strings.Contains(s, "MISSING") || strings.Contains(s, "never stored") || !strings.Contains(s, "stored in the database") ||
		!strings.Contains(s, "keep it private") || !strings.Contains(s, "THEATARR_TMDB_KEY") {
		t.Errorf("secrets block:\n%s", s)
	}
}

// TestSearchCommandsUseStoredIndexerKeys: decide -search, search and router dry-run send the indexer key stored in
// the settings database, with nothing in the environment.
func TestSearchCommandsUseStoredIndexerKeys(t *testing.T) {
	const env = "THEATARR_TEST_PUBLIC_KEY"
	t.Setenv(env, "")
	os.Unsetenv(env)
	for _, tool := range []string{"decide", "search", "router"} {
		t.Run(tool, func(t *testing.T) {
			dir, db := decideFixture(t, "127.0.0.1")
			rest := filepath.Join(dir, "rest.yaml")
			b, err := os.ReadFile(rest)
			if err != nil {
				t.Fatal(err)
			}
			// The fixture stores "fixture-<env name>" as the key of every record naming an env variable.
			s := strings.Replace(string(b), "url: http://prowlarr:9696/1/api\n", "url: http://prowlarr:9696/1/api\n    api_key_env: "+env+"\n", 1)
			if err := os.WriteFile(rest, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var sent []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/1/") { // "public"; the other indexers have no key
					mu.Lock()
					sent = append(sent, r.URL.Query().Get("apikey"))
					mu.Unlock()
				}
				if r.URL.Query().Get("t") == "caps" {
					io.WriteString(w, `<caps><searching><search available="yes" supportedParams="q" />`+
						`<movie-search available="yes" supportedParams="q,imdbid,tmdbid" /></searching>`+
						`<categories><category id="2000" name="Movies" /></categories></caps>`)
					return
				}
				io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
			}))
			defer srv.Close()
			dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr == "prowlarr:9696" {
					addr = srv.Listener.Addr().String()
				}
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}
			args := []string{"-policy", dir, "-db", db, "-arr-snapshot", filepath.Join("..", "..", "internal", "decide", "testdata", "arr"),
				"-title", "7", "-version", "movie-remux"}
			var out bytes.Buffer
			switch tool {
			case "decide":
				err = runDecide(commandSettingsArgs(t, append(args, "-search")), &out, dial)
			case "router":
				err = runRouter(commandSettingsArgs(t, append([]string{"dry-run"}, args...)), &out, dial)
			case "search":
				err = runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-db", db, "-indexer", "public", "-imdb", "tt0133093"}), &out, dial)
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sent) == 0 {
				t.Fatal("no indexer was searched")
			}
			for _, k := range sent {
				if k != "fixture-"+env {
					t.Errorf("the stored key was not sent (set=%t)", k != "")
				}
			}
		})
	}
}
