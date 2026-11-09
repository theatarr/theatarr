package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// searchNet maps torrentio.test to a local server; everything else (the local echo) dials as usual.
func searchNet(t *testing.T, body string) (func(context.Context, string, string) (net.Conn, error), *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "torrentio.test:80" {
			addr = srv.Listener.Addr().String()
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return dial, &hits
}

func pointTorrentioAtTest(t *testing.T, dir string) {
	p := filepath.Join(dir, "indexers.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "https://torrentio.strem.fun", "http://torrentio.test", 1)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tioBody = `{"streams":[{"name":"Torrentio\n4k","title":"The.Matrix.1999.2160p.UHD.BluRay.REMUX-GROUP\n👤 57 💾 61.4 GB ⚙️ ThePirateBay","infoHash":"1111111111111111111111111111111111111111"}]}`

func TestSearchCommandRefusesOffVPN(t *testing.T) {
	t.Parallel()
	dir := examplePolicy(t, "192.0.2.1")
	pointTorrentioAtTest(t, dir)
	dial, hits := searchNet(t, tioBody)
	var out bytes.Buffer
	err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "torrentio", "-imdb", "tt0133093"}), &out, dial)
	if !errors.Is(err, errNotOnVPN) || !strings.Contains(err.Error(), "127.0.0.1 is not in the VPN set") {
		t.Fatalf("off the VPN: %v", err)
	}
	if hits.Load() != 0 {
		t.Error("the indexer was asked although egress is not on the VPN")
	}
}

func TestSearchCommand(t *testing.T) {
	t.Parallel()
	dir := examplePolicy(t, "127.0.0.1")
	pointTorrentioAtTest(t, dir)
	dial, hits := searchNet(t, tioBody)
	var out bytes.Buffer
	if err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "torrentio", "-imdb", "tt0133093"}), &out, dial); err != nil {
		t.Fatalf("search: %v\n%s", err, out.String())
	}
	s := out.String()
	for _, w := range []string{"# torrentio: 1 release(s), 1 request(s)", "61.4 GiB", "57", "pub", "111111111111", "magnet",
		"The.Matrix.1999.2160p.UHD.BluRay.REMUX-GROUP"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
	if strings.Contains(s, "magnet:") || hits.Load() != 1 {
		t.Errorf("hits %d, output:\n%s", hits.Load(), s)
	}
	out.Reset()
	if err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "torrentio", "-imdb", "tt0133093", "-json"}), &out, dial); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"info_hash":"1111111111111111111111111111111111111111"`) || strings.Contains(out.String(), "magnet:") {
		t.Errorf("json:\n%s", out.String())
	}
	if err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "nope", "-imdb", "tt0133093"}), &out, dial); err == nil ||
		!strings.Contains(err.Error(), "unknown indexer") {
		t.Errorf("unknown indexer: %v", err)
	}
}

func TestSearchCommandOptionalVPN(t *testing.T) {
	t.Parallel()
	for _, classes := range []string{"off", "[]", "[providers]"} {
		t.Run(classes, func(t *testing.T) {
			dir := examplePolicy(t, "192.0.2.1")
			pointTorrentioAtTest(t, dir)
			optionalEgress(t, dir, classes)
			dial, hits := searchNet(t, tioBody)
			var out bytes.Buffer
			if err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "torrentio", "-imdb", "tt0133093"}), &out, dial); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 1 {
				t.Fatalf("indexer hits: %d", hits.Load())
			}
		})
	}
}

func optionalEgress(t *testing.T, dir, classes string) {
	t.Helper()
	path := filepath.Join(dir, "egress.yaml")
	if classes == "off" {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), "require_vpn_for: [indexers]", "require_vpn_for: "+classes, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// theatarr search asks DMM's availability, with DMM's key and through the DMM indexer's host, about the Torrentio
// release no feed listed, and prints what DMM listed in the cached column (cached_via in JSON).
func TestSearchCommandDMMAvailability(t *testing.T) {
	t.Setenv("THEATARR_DMM_KEY", "test-dmm-key")
	dir := examplePolicy(t, "127.0.0.1")
	pointTorrentioAtTest(t, dir)
	p := filepath.Join(dir, "indexers.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "https://debridmediamanager.com", "http://dmm.test", 1)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	dmm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sponsor/availability" {
			t.Errorf("DMM asked %s", r.URL.Path)
			return
		}
		asked.Add(1)
		var body struct {
			Service string   `json:"service"`
			IMDb    *string  `json:"imdbId"`
			Hashes  []string `json:"hashes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Header.Get("x-api-key") != "test-dmm-key" ||
			body.IMDb != nil || r.URL.RawQuery != "" {
			t.Errorf("availability request: %v %+v %q", err, body, r.URL.RawQuery)
		}
		if body.Service == "ad" && slices.Contains(body.Hashes, "1111111111111111111111111111111111111111") {
			io.WriteString(w, `{"available":[{"hash":"1111111111111111111111111111111111111111","files":[]}]}`)
			return
		}
		io.WriteString(w, `{"available":[]}`)
	}))
	t.Cleanup(dmm.Close)
	tio, _ := searchNet(t, tioBody)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "dmm.test:80" {
			addr = dmm.Listener.Addr().String()
		}
		return tio(ctx, network, addr)
	}
	var out bytes.Buffer
	if err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "torrentio", "-imdb", "tt0133093",
		"-torbox-check=false"}), &out, dial); err != nil {
		t.Fatalf("search: %v\n%s", err, out.String())
	}
	if asked.Load() != 2 || !regexp.MustCompile(`(?m)\sad\s+The\.Matrix`).MatchString(out.String()) {
		t.Fatalf("%d availability requests, output:\n%s", asked.Load(), out.String())
	}
	out.Reset()
	if err := runSearch(commandSettingsArgs(t, []string{"-policy", dir, "-indexer", "torrentio", "-imdb", "tt0133093",
		"-torbox-check=false", "-json"}), &out, dial); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"cached_via":{"ad":"feed:dmm/availability"}`) {
		t.Errorf("json:\n%s", out.String())
	}
}
