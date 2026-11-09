package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
)

func trashRoot(t *testing.T) string {
	root := os.Getenv("THEATARR_TRASH") // a TRaSH-Guides checkout
	if _, err := os.Stat(filepath.Join(root, "metadata.json")); root == "" || err != nil {
		t.Skip("no TRaSH checkout (THEATARR_TRASH)")
	}
	return root
}

func itemNames(items []Item) string {
	var parts []string
	for _, it := range items {
		s := it.String()
		if it.Allowed {
			s += "*"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

// TestTrashProfiles builds every TRaSH quality profile and checks it is a valid arr profile.
func TestTrashProfiles(t *testing.T) {
	root := trashRoot(t)
	n := 0
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		tr, err := format.LoadTrash(root, fl)
		if err != nil {
			t.Fatal(err)
		}
		for id, tp := range tr.Profiles {
			p, _, err := FromTrash(tr, id, format.ProfileOptions{DefaultGroups: true})
			if err != nil {
				t.Errorf("%s %s: %v", fl, tp.Name, err)
				continue
			}
			for _, prob := range p.Validate() {
				t.Errorf("%s %s: %s", fl, tp.Name, prob)
			}
			if tp.Cutoff != "" && !strings.EqualFold(cutoffName(p), tp.Cutoff) {
				t.Errorf("%s %s: cutoff %q, guide %q", fl, tp.Name, cutoffName(p), tp.Cutoff)
			}
			n++
		}
	}
	t.Logf("%d TRaSH profiles", n)
}

func cutoffName(p *Profile) string {
	it := p.CutoffItem()
	if it.Quality != nil {
		return it.Quality.Name
	}
	return it.Name
}

func TestTrashProfileRemuxWeb2160p(t *testing.T) {
	root := trashRoot(t)
	tr, err := format.LoadTrash(root, format.Radarr)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := FromTrash(tr, "fd161a61e3ab826d3a22d53f935696dd", format.ProfileOptions{})
	if err != nil {
		t.Skip(err)
	}
	got := itemNames(p.Items)
	want := "Unknown | WORKPRINT | CAM | TELESYNC | TELECINE | REGIONAL | DVDSCR | SDTV | DVD | DVD-R | " +
		"WEB 480p (WEBDL-480p, WEBRip-480p) | Bluray-480p | Bluray-576p | HDTV-720p | WEB 720p (WEBDL-720p, WEBRip-720p) | " +
		"Bluray-720p | HDTV-1080p | WEB 1080p (WEBDL-1080p, WEBRip-1080p) | Bluray-1080p | Remux-1080p | HDTV-2160p | " +
		"Bluray-2160p | BR-DISK | Raw-HD | WEB 2160p (WEBDL-2160p, WEBRip-2160p)* | Remux-2160p*"
	if got != want {
		t.Errorf("items\n got %s\nwant %s", got, want)
	}
	if p.Cutoff != 31 || !p.UpgradeAllowed || p.Language != format.LanguageOriginal || p.CutoffFormatScore != 10000 {
		t.Errorf("cutoff %d upgrade %v language %d cutoff score %d", p.Cutoff, p.UpgradeAllowed, p.Language, p.CutoffFormatScore)
	}
	if g := p.Items[len(p.Items)-2]; g.ID != 1003 {
		t.Errorf("WEB 2160p group id %d, want 1003 (the arr default)", g.ID)
	}
}

func TestTrashSizes(t *testing.T) {
	root := trashRoot(t)
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		ts, err := LoadTrashSizes(root, fl)
		if err != nil {
			t.Fatal(err)
		}
		if len(ts.Files) == 0 {
			t.Fatalf("%s: no quality-size files", fl)
		}
		for id, f := range ts.Files {
			if _, err := ts.Definitions(id); err != nil {
				t.Errorf("%s %s: %v", fl, f.Type, err)
			}
		}
	}
	ts, _ := LoadTrashSizes(root, format.Radarr)
	movie, ok := ts.ByType("movie")
	if !ok {
		t.Fatal("no movie sizes")
	}
	d, _ := ts.Definitions(movie.TrashID)
	hd := d.Get(MustQuality(format.Radarr, "HDTV-720p"))
	if hd.MinSize == nil || *hd.MinSize != 17.1 || hd.MaxSize != nil || hd.PreferredSize != nil {
		t.Errorf("HDTV-720p %+v: want min 17.1, max and preferred unlimited (2000/1999 are the limits)", hd)
	}
}
