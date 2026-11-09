package importarr

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/router"
)

func TestFollowerBlocklistReconciliation(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	im := &Importer{Store: st, Now: fixedNow}
	ctx := context.Background()
	hash := strings.Repeat("a", 40)
	write := func(instance string, blocks []BlockedRelease) {
		t.Helper()
		if _, err := im.Write(ctx, Source{Instance: instance, Version: "source", Followers: []string{"theatarr"}},
			&Snapshot{Instance: instance, Flavour: "radarr", Blocklist: blocks}); err != nil {
			t.Fatal(err)
		}
	}
	blocks := []BlockedRelease{{InfoHash: hash, SourceTitle: "Shared.Release"}, {SourceTitle: "Hashless.Release"}}
	write("a", blocks)
	write("b", blocks)
	write("a", nil)
	if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist WHERE source = 'arr:b'`); n != 2 {
		t.Errorf("removing instance a cleared instance b's blocks: %d", n)
	}
	// A local block of the same hash must survive the arr dropping its entry.
	if _, err := st.Writer().Exec(`UPDATE router_blocklist SET source = 'library', reason = 'sample' WHERE info_hash = ?`, hash); err != nil {
		t.Fatal(err)
	}
	write("b", nil)
	if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist`); n != 1 {
		t.Errorf("remaining blocks %d, want only the local block", n)
	}
	if reason := one[string](t, st, `SELECT reason FROM router_blocklist`); reason != "sample" {
		t.Errorf("local reason %q", reason)
	}
	write("a", blocks)
	if reason := one[string](t, st, `SELECT reason FROM router_blocklist WHERE info_hash = ?`, hash); reason != "sample" {
		t.Errorf("import replaced local block: %q", reason)
	}
}

func TestImportFollowerBlocklist(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/blocklist.json")
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	// Cross the page boundary; repeated rows must not create duplicate blocks.
	for len(records) < 251 {
		records = append(records, records[0])
	}
	for _, flavour := range []string{"radarr", "sonarr"} {
		t.Run(flavour, func(t *testing.T) {
			st := openStore(t)
			im := &Importer{Store: st, Now: fixedNow}
			var srv *httptest.Server
			var requests *[]string
			if flavour == "radarr" {
				f := &fakeRadarr{blocklist: records}
				srv, requests = httptest.NewServer(f), &f.requests
			} else {
				f := &fakeSonarr{blocklist: records}
				srv, requests = httptest.NewServer(f), &f.requests
			}
			defer srv.Close()
			src := Source{Instance: flavour, Flavour: flavour, Version: "source", Followers: []string{"theatarr"}}
			runImport(t, im, src, srv)
			if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'theatarr'`); n != 3 {
				t.Errorf("follower blocks %d, want 3", n)
			}
			if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'source'`); n != 0 {
				t.Errorf("source blocks %d", n)
			}
			if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist WHERE info_hash = 'abcdef0123456789abcdef0123456789abcdef01'
				AND reason = ?`, "arr blocklist ("+flavour+")"); n != 1 {
				t.Error("arr hash/reason missing")
			}
			sum := runImport(t, im, src, srv)
			if sum.Changes.Total() != 0 {
				t.Errorf("reimport changed %s", sum.Changes)
			}
			pages := 0
			for _, r := range *requests {
				if strings.HasPrefix(r, "blocklist?") {
					pages++
				}
			}
			if pages != 4 {
				t.Errorf("blocklist GET pages %d, want 4", pages)
			}
		})
	}
}

func TestFollowerBlocklistRecoversTorrentHashFromHistory(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	f := newFakeSonarr("/mnt/shows")
	pack := f.history[0]["sourceTitle"].(string)
	f.blocklist = []map[string]any{{"id": 9, "seriesId": 1, "sourceTitle": pack, "protocol": "torrent"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	runImport(t, im, Source{Instance: "sonarr", Flavour: "sonarr", Version: "source", Followers: []string{"theatarr"}}, srv)
	hash := strings.ToLower(hashS1)
	if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'theatarr' AND info_hash = ?`, hash); n != 1 {
		t.Fatalf("history hash was not mirrored: %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'theatarr' AND info_hash = ?`, router.BlocklistKey("", pack)); n != 0 {
		t.Fatalf("hashful blocklist kept a title fallback: %d", n)
	}
}
