package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// decide -search and router dry-run give the native SeaDex indexer the stored lookups, so it resolves the title's
// AniList entry from the database (the query itself carries only TMDb and IMDb ids) and asks SeaDex for it.
func TestCLISearchResolvesAniList(t *testing.T) {
	dir, db := decideFixture(t, "192.0.2.1")
	rest := filepath.Join(dir, "rest.yaml")
	b, err := os.ReadFile(rest)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	s = s[:strings.Index(s, "\negress:")] + "\n"
	s = strings.Replace(s, "indexers:\n", "indexers:\n  sd:\n    kind: seadex\n    url: http://seadex:80\n    privacy: public\n", 1)
	if err := os.WriteFile(rest, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	versions := filepath.Join(dir, "versions.yaml")
	b, err = os.ReadFile(versions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versions, []byte(strings.Replace(string(b), "indexers: [public, multi]", "indexers: [sd]", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO title_external_id VALUES(7,'anilist','11')`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("filter"))
		mu.Unlock()
		io.WriteString(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
	}))
	defer srv.Close()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "seadex:80" {
			addr = srv.Listener.Addr().String()
		} else if !strings.HasPrefix(addr, "127.0.0.1:") {
			return nil, fmt.Errorf("unexpected address %s", addr)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	args := []string{"-policy", dir, "-db", db, "-arr-snapshot", filepath.Join("..", "..", "internal", "decide", "testdata", "arr"), "-title", "7", "-version", "movie-remux"}
	for name, run := range map[string]func() error{
		"decide -search": func() error {
			return runDecide(commandSettingsArgs(t, append(append([]string{}, args...), "-search")), io.Discard, dial)
		},
		"router dry-run": func() error {
			return runRouter(commandSettingsArgs(t, append([]string{"dry-run"}, args...)), &bytes.Buffer{}, dial)
		},
	} {
		mu.Lock()
		asked = nil
		mu.Unlock()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mu.Lock()
		got := strings.Join(asked, " ")
		mu.Unlock()
		if !strings.Contains(got, "alID=11") {
			t.Fatalf("%s did not ask SeaDex for the stored AniList entry: %q", name, got)
		}
	}
}
