package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// TestCorpusSnapshots imports every instance's profile, definition and format snapshot from the corpus
// (THEATARR_CORPUS) and checks the round trip.
func TestCorpusSnapshots(t *testing.T) {
	dir := os.Getenv("THEATARR_CORPUS")
	if dir == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	b, err := os.ReadFile(filepath.Join(dir, "instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	var insts []struct {
		Name    string `json:"name"`
		Flavour string `json:"flavour"`
	}
	if err := json.Unmarshal(b, &insts); err != nil {
		t.Fatal(err)
	}
	for _, in := range insts {
		snap := filepath.Join(dir, in.Name, "snapshot")
		read := func(n string) []byte {
			b, err := os.ReadFile(filepath.Join(snap, n+".json"))
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		r, err := CheckSnapshot(format.Flavour(in.Flavour), read("qualityprofile"), read("qualitydefinition"), read("customformat"))
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		for _, d := range r.ProfileRoundTrip {
			t.Errorf("%s: profile round trip: %s", in.Name, d)
		}
		if r.DefsRoundTrip != "" {
			t.Errorf("%s: definitions round trip: %s", in.Name, r.DefsRoundTrip)
		}
		for _, p := range r.Problems {
			t.Logf("%s: %s", in.Name, p)
		}
		t.Logf("%s: %d profiles, %d formats, missing formats %d", in.Name, len(r.Profiles), len(r.Formats), len(r.MissingFormats))
	}
}

// TestCorpusDefaultProfiles compares QualityProfileService's default profiles (created by the arr on first start)
// with ours. Profiles a user edited since differ; the test logs those and fails only when none match.
func TestCorpusDefaultProfiles(t *testing.T) {
	dir := os.Getenv("THEATARR_CORPUS")
	if dir == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	defaults := map[format.Flavour]map[string][]string{
		format.Radarr: {
			"Any": {"Bluray-480p", "WORKPRINT", "CAM", "TELESYNC", "TELECINE", "DVDSCR", "REGIONAL", "SDTV", "DVD", "DVD-R",
				"HDTV-720p", "HDTV-1080p", "HDTV-2160p", "WEBDL-480p", "WEBRip-480p", "WEBDL-720p", "WEBRip-720p",
				"WEBDL-1080p", "WEBRip-1080p", "WEBDL-2160p", "WEBRip-2160p", "Bluray-480p", "Bluray-576p", "Bluray-720p",
				"Bluray-1080p", "Bluray-2160p", "Remux-1080p", "Remux-2160p", "BR-DISK"},
			"SD": {"Bluray-480p", "WORKPRINT", "CAM", "TELESYNC", "TELECINE", "DVDSCR", "REGIONAL", "SDTV", "DVD",
				"WEBDL-480p", "WEBRip-480p", "Bluray-480p", "Bluray-576p"},
			"HD-720p":         {"Bluray-720p", "HDTV-720p", "WEBDL-720p", "WEBRip-720p", "Bluray-720p"},
			"HD-1080p":        {"Bluray-1080p", "HDTV-1080p", "WEBDL-1080p", "WEBRip-1080p", "Bluray-1080p", "Remux-1080p"},
			"Ultra-HD":        {"Remux-2160p", "HDTV-2160p", "WEBDL-2160p", "WEBRip-2160p", "Bluray-2160p", "Remux-2160p"},
			"HD - 720p/1080p": {"Bluray-720p", "HDTV-720p", "HDTV-1080p", "WEBDL-720p", "WEBRip-720p", "WEBDL-1080p", "WEBRip-1080p", "Bluray-720p", "Bluray-1080p", "Remux-1080p"},
		},
	}
	same, total := 0, 0
	for _, in := range []string{"radarr_4k", "radarr_4k_web", "radarr_4k_debrid", "radarr_anime"} {
		b, err := os.ReadFile(filepath.Join(dir, in, "snapshot", "qualityprofile.json"))
		if err != nil {
			t.Skip(err)
		}
		ps, err := ParseArrProfiles(format.Radarr, b)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			spec, ok := defaults[format.Radarr][p.Name]
			if !ok {
				continue
			}
			cut := MustQuality(format.Radarr, spec[0])
			var allowed []Quality
			for _, n := range spec[1:] {
				allowed = append(allowed, MustQuality(format.Radarr, n))
			}
			d := DefaultProfile(format.Radarr, p.Name, &cut, allowed, nil)
			total++
			if itemNames(d.Items) == itemNames(p.Items) && d.Cutoff == p.Cutoff {
				same++
				continue
			}
			t.Logf("%s %s differs (edited?):\n ours %s cutoff %d\n arr  %s cutoff %d", in, p.Name, itemNames(d.Items), d.Cutoff, itemNames(p.Items), p.Cutoff)
		}
	}
	t.Logf("%d of %d default profiles match", same, total)
	if total > 0 && same == 0 {
		t.Error("no default profile matches")
	}
}
