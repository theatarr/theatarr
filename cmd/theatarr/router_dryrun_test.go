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
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// Router dry-run sees the stored standing (with this search's answers laid over it) like
// decide -search.
func TestRouterDryRunUsesStoredStanding(t *testing.T) {
	dir, db := decideFixture(t, "192.0.2.1")
	versions := filepath.Join(dir, "versions.yaml")
	b, err := os.ReadFile(versions)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "  movie-remux:\n", "  movie-remux:\n    prefer_seadex: true\n", 1)
	s = strings.Replace(s, "indexers: [public, multi]", "indexers: [public]", 1)
	if err := os.WriteFile(versions, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	rest := filepath.Join(dir, "rest.yaml")
	b, err = os.ReadFile(rest)
	if err != nil {
		t.Fatal(err)
	}
	s = string(b)
	s = s[:strings.Index(s, "\negress:")] + "\n"
	if err := os.WriteFile(rest, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title_external_id VALUES(7,'anilist','11')`,
		`INSERT INTO content(id,info_hash,first_seen_at,last_seen_at) VALUES(1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',0,0)`,
		`UPDATE version_file SET content_id=1 WHERE version_id=3`,
		`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,'{"anilist":11,"torrents":[{"info_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","preference":2,"is_best":true,"facts":{"isBest":true}}]}',0,253402214400000)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			io.WriteString(w, `<caps><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q,imdbid,tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
			return
		}
		io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR</title><guid>candidate</guid><size>40000000000</size><category>2000</category><torznab:attr name="infohash" value="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"/></item></channel></rss>`)
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
	args := []string{"-policy", dir, "-db", db, "-title", "7", "-version", "movie-remux"}
	var decideOut, routerOut bytes.Buffer
	if err := runDecide(commandSettingsArgs(t, append(append([]string{}, args...), "-search")), &decideOut, dial); err != nil {
		t.Fatal(err)
	}
	if err := runRouter(commandSettingsArgs(t, append([]string{"dry-run"}, args...)), &routerOut, dial); err != nil {
		t.Fatal(err)
	}
	t.Logf("decide -search:\n%s\nrouter dry-run:\n%s", decideOut.String(), routerOut.String())
	if !strings.Contains(decideOut.String(), "Existing file on disk is of equal or higher preference") {
		t.Fatalf("control did not reject for held standing: %s", decideOut.String())
	}
	if strings.Contains(routerOut.String(), "1 accepted") {
		t.Fatalf("dry-run ignored held SeaDex standing")
	}
}
