package format

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestArrImport(t *testing.T) {
	b, _ := os.ReadFile("testdata/arr_customformat_radarr.json")
	fs, err := ParseArrFormats(Radarr, b)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile("testdata/arr_qualityprofile_radarr.json")
	ps, err := ParseArrProfiles(Radarr, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 || len(ps) != 1 {
		t.Fatalf("got %d formats, %d profiles", len(fs), len(ps))
	}
	size := fs[2].Specs[0].Condition.(*Size)
	if size.Min != 40 || size.Max != 100.5 {
		t.Errorf("size = %+v", size)
	}
	if l := fs[2].Specs[2].Condition.(*Language); l.Value != -2 || l.ExceptLanguage {
		t.Errorf("language = %+v", l)
	}

	in := &Input{Flavour: Radarr, ReleaseTitle: Str("A.Movie.1994.2160p.BluRay.REMUX.TrueHD.7.1.Atmos-FraMeSToR"),
		ReleaseGroup: Str("FraMeSToR"), Edition: Str(""), Source: 9, Resolution: 2160, Modifier: 5,
		Languages: []int{8}, Size: 60 << 30, Year: 1994, Media: &Media{Year: 1994, OriginalLanguage: 8}}
	got, err := Calculate(fs, in)
	if err != nil {
		t.Fatal(err)
	}
	// The lone optional Indexer Flag condition fails, so its group and the format fail.
	if n := names(got); n != "Remux Tier 01|TrueHD ATMOS" {
		t.Fatalf("matched %s", n)
	}
	in.IndexerFlags = 1 | 128
	got, _ = Calculate(fs, in)
	if n := names(got); n != "Big Old English|Remux Tier 01|TrueHD ATMOS" {
		t.Fatalf("matched %s", n)
	}
	if s := ps[0].Score(got); s != 6947 {
		t.Errorf("score %d", s)
	}

	bad := []byte(`[{"id":1,"name":"x","specifications":[{"name":"e","implementation":"EditionSpecification","fields":[{"name":"value","value":"a"}]}]}]`)
	if _, err := ParseArrFormats(Sonarr, bad); err == nil {
		t.Error("Edition accepted for sonarr")
	}
	bad = []byte(`[{"id":1,"name":"x","specifications":[{"name":"e","implementation":"ReleaseTitleSpecification","fields":[{"name":"value","value":"(a"}]}]}]`)
	if _, err := ParseArrFormats(Radarr, bad); err == nil {
		t.Error("bad regex accepted")
	}
}

func TestTrashImport(t *testing.T) {
	tr, err := LoadTrash("testdata/trash", Sonarr)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Formats) != 3 || len(tr.Profiles) != 1 || len(tr.Groups) != 1 {
		t.Fatalf("loaded %d formats, %d profiles, %d groups", len(tr.Formats), len(tr.Profiles), len(tr.Groups))
	}
	p, fs, err := tr.Profile("bbbb0000000000000000000000000001", ProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scores := map[string]int{}
	for _, it := range p.FormatItems {
		scores[it.Name] = it.Score
	}
	// anime-sonarr set: WEB Tier has 0 there, Not Original 101.
	if len(fs) != 2 || scores["WEB Tier"] != 0 || scores["Not Original"] != 101 {
		t.Errorf("profile items %v", scores)
	}
	p, fs, err = tr.Profile("bbbb0000000000000000000000000001", ProfileOptions{ScoreSet: "x", DefaultGroups: true})
	if err != nil {
		t.Fatal(err)
	}
	scores = map[string]int{}
	for _, it := range p.FormatItems {
		scores[it.Name] = it.Score
	}
	// Unknown set falls back to default; Not Original has no default.
	if len(fs) != 3 || scores["WEB Tier"] != 1700 || scores["Not Original"] != 0 || scores["Season Pack"] != 10 {
		t.Errorf("profile items %v", scores)
	}
	in := &Input{Flavour: Sonarr, ReleaseTitle: Str("Show.S01.1080p.WEB-DL-FLUX"), ReleaseGroup: Str("FLUX"),
		Source: 3, Resolution: 1080, Languages: []int{1}, ReleaseType: 3, Media: &Media{OriginalLanguage: 8}}
	got, _ := Calculate(fs, in)
	if n := names(got); n != "Not Original|Season Pack|WEB Tier" {
		t.Errorf("matched %s", n)
	}
	if s := p.Score(got); s != 1710 {
		t.Errorf("score %d", s)
	}
}

// TestTrashGuides loads the whole TRaSH checkout when it is present: every format of both flavours must import and
// every regex must compile.
func TestTrashGuides(t *testing.T) {
	root := os.Getenv("THEATARR_TRASH") // a TRaSH-Guides checkout
	if _, err := os.Stat(filepath.Join(root, "metadata.json")); root == "" || err != nil {
		t.Skip("no TRaSH checkout (THEATARR_TRASH)")
	}
	for _, fl := range []Flavour{Radarr, Sonarr} {
		tr, err := LoadTrash(root, fl)
		if err != nil {
			t.Fatal(err)
		}
		for id := range tr.Profiles {
			if _, _, err := tr.Profile(id, ProfileOptions{DefaultGroups: true}); err != nil {
				t.Error(err)
			}
		}
		t.Logf("%s: %d formats, %d profiles, %d groups", fl, len(tr.Formats), len(tr.Profiles), len(tr.Groups))
	}
}

func TestParseResponseInput(t *testing.T) {
	b, _ := os.ReadFile("testdata/arr_parse_samples.json")
	var samples map[string]ParseResponse
	if err := json.Unmarshal(b, &samples); err != nil {
		t.Fatal(err)
	}
	r := samples["radarr"]
	in, ok, err := r.Input(Radarr)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if *in.ReleaseTitle != "A.Movie.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR" ||
		in.Source != 9 || in.Resolution != 2160 || in.Modifier != 5 || in.Year != 2024 ||
		len(in.Languages) != 1 || in.Languages[0] != 1 || in.Media.OriginalLanguage != 1 || *in.ReleaseGroup != "FraMeSToR" {
		t.Errorf("radarr input %+v", in)
	}
	if r.QualityProfileID() != 7 {
		t.Errorf("profile %d", r.QualityProfileID())
	}
	s := samples["sonarr"]
	in, ok, err = s.Input(Sonarr)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if in.Source != 3 || in.Resolution != 2160 || in.ReleaseType != 1 || in.Modifier != 0 ||
		*in.ReleaseTitle != "The.Last.of.Us.S02E01.2160p.MAX.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX" {
		t.Errorf("sonarr input %+v", in)
	}
}
