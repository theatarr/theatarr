package importarr

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnsupportedSelection(t *testing.T) {
	t.Parallel()
	tags := map[int]string{3: "anime", 4: "1-someone"}
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	delays := []arrDelayProfile{
		// The default: usenet delay and bypass switches do not matter with no torrent delay.
		{ID: 1, EnableUsenet: true, EnableTorrent: true, UsenetDelay: 120, BypassIfHighestQuality: true},
		{ID: 3, EnableTorrent: false, EnableUsenet: true, Tags: []int{4}},
		{ID: 2, EnableTorrent: true, TorrentDelay: 60, BypassIfHighestQuality: true, BypassIfAboveCustomFormatScore: true,
			MinimumCustomFormatScore: 2000, Tags: []int{3, 9}},
	}
	releases := []arrReleaseProfile{
		{ID: 1, Name: "off", Enabled: false, Required: raw(`["x265"]`)},
		{ID: 2, Name: "empty", Enabled: true, Required: raw(`[]`), Ignored: raw(`null`)},
		{ID: 3, Name: "no-cam", Enabled: true, Required: raw(`"2160p, "`), Ignored: raw(`["CAM","TS","/\\bHC\\b/i"]`),
			IndexerID: 5},
	}
	got := unsupportedSelection(delays, releases, tags)
	want := []string{
		"delay profile 2 (tags 9,anime; torrent delay 60 min, bypass if highest quality, bypass at score 2000) is not " +
			"supported: Theatarr grabs without delay",
		"delay profile 3 (tags 1-someone; torrents disabled) is not supported: Theatarr grabs without delay",
		`release profile 3 "no-cam" (all titles, indexer 5; 1 must contain, 3 must not contain) is not supported: ` +
			"Theatarr applies no release profile terms",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// An import run reads both lists and carries the lines as warnings; the default delay profile gives none (the other
// import tests run with it).
func TestImportSelectionWarnings(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	fake.releaseProfiles = []map[string]any{{"id": 1, "name": "x", "enabled": true, "required": []string{},
		"ignored": []string{"CAM"}, "indexerId": 0, "tags": []int{1}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	sum := runImport(t, im, Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux",
		Library: "/mnt/movies-a"}, srv)
	if len(sum.Warnings) != 1 || !strings.HasPrefix(sum.Warnings[0], `release profile 1 "x" (tags best-available;`) {
		t.Errorf("warnings %v", sum.Warnings)
	}
	var stored string
	if err := st.Reader().QueryRow(`SELECT summary FROM import_run`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "Theatarr applies no release profile terms") {
		t.Errorf("import_run summary %s", stored)
	}
}
