package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// decideFixture copies internal/decide/testdata/policy with its egress check pointed at a local echo, and makes a
// database with one movie, its movie-remux version and a WEB file.
func decideFixture(t *testing.T, vpnIP string) (policyDir, db string) {
	t.Helper()
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		io.WriteString(w, host)
	}))
	t.Cleanup(echo.Close)
	src := filepath.Join("..", "..", "internal", "decide", "testdata", "policy")
	policyDir = t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(policyDir, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := strings.Replace(string(b), "https://api.ipify.org", echo.URL, 1)
		s = strings.Replace(s, "192.0.2.1", vpnIP, 1)
		return os.WriteFile(filepath.Join(policyDir, rel), []byte(s), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db = filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, tmdb_id, imdb_id, runtime_minutes, created_at, updated_at) VALUES (7, 'movie', 'Dune: Part Two', 2024, 693134, 'tt15239678', 166, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (3, 7, 'movie-remux', 'filled', 0, 0)`,
		`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, relative_path, size_bytes, quality, release_group, scene_name, languages)
		 VALUES (3, 7, 'radarr_4k_debrid', 1, '/m/Dune.mkv', 'Dune Part Two (2024) [WEBDL-2160p]-FLUX.mkv', 26843545600, 'WEBDL-2160p', 'FLUX', 'Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX', '["English"]')`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return policyDir, db
}

func TestDecideCommand(t *testing.T) {
	dir, db := decideFixture(t, "192.0.2.1")
	snap := filepath.Join("..", "..", "internal", "decide", "testdata", "arr")
	var out bytes.Buffer
	err := runDecide(commandSettingsArgs(t, []string{"-policy", dir, "-db", db, "-arr-snapshot", snap, "-tmdb", "693134", "-version", "movie-remux",
		"-size", "40GB", "-indexer", "public",
		"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR"}), &out, nil)
	if err != nil {
		t.Fatalf("decide: %v\n%s", err, out.String())
	}
	s := out.String()
	for _, w := range []string{"quality   Remux-2160p v1", "Remux Tier 01 +1950, TrueHD ATMOS +500 = 2450",
		"existing  WEBDL-2160p, WEB Tier 01 +1700 = 1700", "upgrade   yes: better quality: Remux-2160p over WEBDL-2160p",
		"decision  ACCEPTED"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}

	out.Reset()
	err = runDecide(commandSettingsArgs(t, []string{"-policy", dir, "-db", db, "-arr-snapshot", snap, "-title", "7", "-version", "movie-remux",
		"-json", "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX"}), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Accepted   bool
		Rejections []struct{ Reason string }
	}
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if d.Accepted || len(d.Rejections) != 1 || d.Rejections[0].Reason != "DiskCustomFormatScore" {
		t.Errorf("decision: %s", out.String())
	}
}

func TestDecideSearchRefusesOffVPN(t *testing.T) {
	dir, db := decideFixture(t, "192.0.2.1")
	snap := filepath.Join("..", "..", "internal", "decide", "testdata", "arr")
	var out bytes.Buffer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("dialled %s", addr)
			return nil, errors.New("no")
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	err := runDecide(commandSettingsArgs(t, []string{"-policy", dir, "-db", db, "-arr-snapshot", snap, "-title", "7", "-version", "movie-remux",
		"-search"}), &out, dial)
	if !errors.Is(err, errNotOnVPN) {
		t.Fatalf("off the VPN: %v", err)
	}
}

func TestSearchToolsOptionalVPN(t *testing.T) {
	for _, tool := range []string{"decide", "router", "shadow"} {
		for _, mode := range []string{"off", "monitor-only"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				dir, db := decideFixture(t, "192.0.2.1")
				path := filepath.Join(dir, "rest.yaml")
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				s := string(b)
				if mode == "off" {
					s = s[:strings.Index(s, "\negress:")] + "\n"
				} else {
					s += "  require_vpn_for: []\n"
				}
				if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
				if tool == "shadow" {
					path := filepath.Join(dir, "profiles.yaml")
					b, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					local := strings.Replace(string(b), "from_arr: radarr_4k_debrid/Remux + WEB 2160p", "qualities: [Remux-2160p]\n    cutoff: Remux-2160p", 1)
					if err := os.WriteFile(path, []byte(local), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
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
					} else if !strings.HasPrefix(addr, "127.0.0.1:") {
						return nil, fmt.Errorf("unexpected address %s", addr)
					}
					var d net.Dialer
					return d.DialContext(ctx, network, addr)
				}
				args := []string{"-policy", dir, "-db", db, "-arr-snapshot", filepath.Join("..", "..", "internal", "decide", "testdata", "arr"), "-title", "7", "-version", "movie-remux"}
				var out bytes.Buffer
				switch tool {
				case "decide":
					err = runDecide(commandSettingsArgs(t, append(args, "-search")), &out, dial)
				case "router":
					err = runRouter(commandSettingsArgs(t, append([]string{"dry-run"}, args...)), &out, dial)
				case "shadow":
					err = runShadowOnce(commandSettingsArgs(t, []string{"-policy", dir, "-db", db, "-rss"}), &out, dial)
				}
				if err != nil {
					t.Fatalf("%v\n%s", err, out.String())
				}
				if hits.Load() == 0 {
					t.Fatal("no indexer was searched")
				}
			})
		}
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{"123": 123, "25GB": 25e9, "1.5GiB": 3 << 29, "700 MiB": 700 << 20} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v, want %d", in, got, err, want)
		}
	}
	if _, err := parseSize("lots"); err == nil {
		t.Error("lots: no error")
	}
}
