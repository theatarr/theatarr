package decide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

const gb = int64(1) << 30

func testEngine(t *testing.T) *Engine {
	t.Helper()
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	e, err := New(pol, Options{Arr: SnapshotDir("testdata/arr"),
		Now: func() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return e
}

var dune = Title{ID: 7, Kind: policy.KindMovie, Name: "Dune: Part Two", Year: 2024, IMDb: "tt15239678", TMDb: 693134,
	Runtime: 166, OriginalLanguage: "en"}

func rel(title, idx string, size int64) Release {
	return Release{Release: indexer.Release{Indexer: idx, Kind: "torznab", Title: title, Size: size, Seeders: 10,
		Peers: 12, Privacy: policy.PrivacyPublic}}
}

func reasons(d *Decision) string {
	var s []string
	for _, r := range d.Rejections {
		s = append(s, r.Reason)
	}
	return strings.Join(s, ",")
}

func formatNames(fs []FormatScore) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Name)
	}
	return strings.Join(s, ",")
}

func TestBuildPolicyProfile(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	p := e.Profile("remux-2160p", format.Radarr)
	if p == nil || p.Source != "policy" {
		t.Fatalf("remux-2160p not built: %+v", p)
	}
	allowed := map[string]bool{}
	for _, q := range p.Profile.AllowedQualities() {
		allowed[q.Name] = true
	}
	if len(allowed) != 3 || !allowed["Remux-2160p"] || !allowed["WEBDL-2160p"] || !allowed["WEBRip-2160p"] {
		t.Errorf("allowed qualities %v", allowed)
	}
	if c := p.Profile.CutoffItem(); c == nil || c.Quality == nil || c.Quality.Name != "Remux-2160p" {
		t.Errorf("cutoff %+v", c)
	}
	if !p.Profile.UpgradeAllowed || p.Profile.CutoffFormatScore != 3000 || p.Profile.MinUpgradeFormatScore != 1 ||
		p.Profile.Language != format.LanguageAny {
		t.Errorf("profile settings %+v", p.Profile)
	}
	if len(p.Formats) != 6 || len(p.Profile.FormatItems) != 6 {
		t.Errorf("%d formats, %d items", len(p.Formats), len(p.Profile.FormatItems))
	}
	for key, want := range map[string]int{"original": format.LanguageOriginal, "french": 2} {
		if p := e.Profile(key, format.Radarr); p == nil || p.Profile.Language != want {
			t.Errorf("profile %s language: %+v, want %d", key, p, want)
		}
	}
	// Unlisted qualities are still in the profile, not allowed (the arr lists every quality).
	if n := len(p.Profile.Items); n < 10 {
		t.Errorf("only %d items", n)
	}
	def := p.Engine.Definitions.Get(mustQuality(t, "Remux-2160p"))
	if def.MinSize == nil || *def.MinSize != 35 || def.MaxSize == nil || *def.MaxSize != 400 || def.PreferredSize != nil {
		t.Errorf("Remux-2160p definition %+v", def)
	}
}

func TestBuildFromArr(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	p := e.Profile("shadow", format.Radarr)
	if p == nil || p.Source != "from_arr radarr_4k_debrid/Remux + WEB 2160p" {
		t.Fatalf("shadow not built: %+v", p)
	}
	if p.Profile.Cutoff != 31 || p.Profile.Language != format.LanguageOriginal || p.Profile.CutoffFormatScore != 10000 {
		t.Errorf("profile %+v", p.Profile)
	}
	if len(p.Formats) != 11 {
		t.Errorf("%d formats", len(p.Formats))
	}
	def := p.Engine.Definitions.Get(mustQuality(t, "Remux-2160p"))
	if def.MinSize == nil || *def.MinSize != 187.4 {
		t.Errorf("Remux-2160p definition from the arr: %+v", def)
	}
}

func TestBuildErrors(t *testing.T) {
	t.Parallel()
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	pol.Profiles["shadow"].FromArr = "radarr_4k_debrid/Nope"
	pol.Formats["Seadex"] = &policy.FormatRef{File: "formats/seadex.json"}
	pol.Profiles["remux-2160p"].Formats["Seadex"] = 5000
	pol.Dir = t.TempDir()
	_, err = New(pol, Options{Arr: SnapshotDir("testdata/arr")})
	var pe *policy.Errors
	if !errors.As(err, &pe) {
		t.Fatalf("want *policy.Errors, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		`profiles[remux-2160p].formats[Seadex]: open `,
		`profiles[shadow].from_arr: radarr_4k_debrid has no profile "Nope" (it has Remux + WEB 2160p)`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestBuildReusesUnchangedProfilesWhenOtherInputsChange(t *testing.T) {
	t.Parallel()
	previous := testEngine(t)
	pol, err := previous.Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	pol.Profiles["remux-2160p"].MinScore++
	arrUnavailable := ArrSourceFunc(func(string) (*ArrSnapshot, error) {
		return nil, errors.New("arr unavailable")
	})
	next, err := New(pol, Options{Arr: arrUnavailable, Previous: previous})
	if err != nil {
		t.Fatalf("unrelated from_arr profile depended on arr availability: %v", err)
	}
	if next.Profile("shadow", format.Radarr) != previous.Profile("shadow", format.Radarr) {
		t.Fatal("unchanged from_arr profile was rebuilt")
	}
	if next.Profile("remux-2160p", format.Radarr) == previous.Profile("remux-2160p", format.Radarr) {
		t.Fatal("changed profile was reused")
	}

	budgetOnly, err := previous.Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if budgetOnly.Budgets == nil {
		budgetOnly.Budgets = map[string]*policy.Budget{}
	}
	budgetOnly.Budgets["reload-test"] = &policy.Budget{Requests: 1}
	budgetReload, err := New(budgetOnly, Options{Arr: arrUnavailable, Previous: previous})
	if err != nil || budgetReload.Profile("shadow", format.Radarr) != previous.Profile("shadow", format.Radarr) {
		t.Fatalf("budget-only reload rebuilt an arr profile: %v", err)
	}
}

func TestBuildUnknownImplementation(t *testing.T) {
	t.Parallel()
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	pol.Dir = t.TempDir()
	os.WriteFile(filepath.Join(pol.Dir, "broken.json"), []byte(`{"name": "Broken", "specifications": [
		{"name": "x", "implementation": "NoSuchSpecification", "fields": {"value": "x"}}]}`), 0o644)
	pol.Formats = map[string]*policy.FormatRef{"Broken": {File: "broken.json"}}
	for _, p := range pol.Profiles {
		p.Formats = nil
	}
	e, err := New(pol, Options{Arr: SnapshotDir("testdata/arr")})
	if err != nil {
		t.Fatalf("an unscored broken format should only warn: %v", err)
	}
	if len(e.Warnings) == 0 {
		t.Errorf("no warnings")
	}
	for _, w := range e.Warnings { // one per policy profile that leaves it out
		if !strings.Contains(w, "formats[Broken]") {
			t.Errorf("warnings %v", e.Warnings)
		}
	}
	pol.Profiles["remux-2160p"].Formats = map[string]int{"Broken": 5000}
	if _, err := New(pol, Options{Arr: SnapshotDir("testdata/arr")}); err == nil ||
		!strings.Contains(err.Error(), "profiles[remux-2160p].formats[Broken]") {
		t.Errorf("scored broken format: %v", err)
	}
}

// The example policy's Seadex format (our SeaDexSpecification) builds and scores SeaDex's best release; a file from
// the same torrent scores the same, so it is no upgrade over itself.
func TestSeaDexFormat(t *testing.T) {
	t.Parallel()
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	pol.Dir = "../../examples/policy"
	pol.Formats = map[string]*policy.FormatRef{"Seadex": {File: "formats/seadex.json"}}
	for _, p := range pol.Profiles {
		p.Formats = nil
	}
	pol.Profiles["remux-2160p"].Formats = map[string]int{"Seadex": 5000}
	e, err := New(pol, Options{Arr: SnapshotDir("testdata/arr"),
		Now: func() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(e.Warnings) != 0 {
		t.Errorf("warnings %v", e.Warnings)
	}
	const hash = "0123456789abcdef0123456789abcdef01234567"
	rels := []indexer.Release{
		{Indexer: "public", Kind: "torznab", Title: "Dune.Part.Two.2024.2160p.BluRay.REMUX.HEVC-GRP", Size: 60 * gb,
			Seeders: 10, Peers: 10, Privacy: policy.PrivacyPublic, InfoHash: hash},
		{Indexer: "seadex", Kind: "seadex", Title: "Dune Part Two (2024)", InfoHash: hash,
			Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest}},
		{Indexer: "public", Kind: "torznab", Title: "Dune.Part.Two.2024.2160p.BluRay.REMUX.HEVC-OTHER", Size: 60 * gb,
			Seeders: 10, Peers: 10, Privacy: policy.PrivacyPublic, InfoHash: "1123456789abcdef0123456789abcdef01234567"},
	}
	if n := indexer.MarkSeaDex(rels); n != 1 {
		t.Errorf("marked %d", n)
	}
	ctx := context.Background()
	v := Version{Key: "movie-remux"}
	d, err := e.Decide(ctx, Release{Release: rels[0]}, dune, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Score != 5000 || formatNames(d.Formats) != "Seadex" {
		t.Errorf("SeaDex best: score %d formats %v %v", d.Score, d.Formats, d.Rejections)
	}
	other, err := e.Decide(ctx, Release{Release: rels[2]}, dune, v, nil)
	if err != nil || other.Score != 0 {
		t.Fatalf("other release: %v %v", other, err)
	}
	file := &ExistingFile{Quality: "Remux-2160p", Revision: 1, SceneName: rels[0].Title, Size: 60 * gb, InfoHash: hash}
	d, err = e.Decide(ctx, Release{Release: rels[0]}, dune, v, file)
	if err != nil {
		t.Fatal(err)
	}
	if d.Existing == nil || d.Existing.Score != 5000 || d.Accepted {
		t.Errorf("same torrent on disk: existing %+v accepted %v", d.Existing, d.Accepted)
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	ctx := context.Background()
	cases := []struct {
		name     string
		version  string
		rel      Release
		title    *Title
		want     string // rejection reasons, "" for accepted
		quality  string
		formats  string
		score    int
		match    MatchType
		langs    string
		mutateFn func(*Release)
	}{
		{name: "remux tier 01 with atmos", version: "movie-remux",
			rel:     rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb),
			quality: "Remux-2160p", formats: "Remux Tier 01,TrueHD ATMOS", score: 2450, match: MatchTitle, langs: "English"},
		{name: "web tier 01", version: "movie-remux",
			rel:     rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", "public", 25*gb),
			quality: "WEBDL-2160p", formats: "TrueHD ATMOS,WEB Tier 01", score: 2200},
		{name: "quality not in the profile", version: "movie-remux",
			rel:  rel("Dune.Part.Two.2024.1080p.BluRay.x264-GRP", "public", 12*gb),
			want: "QualityNotWanted", quality: "Bluray-1080p"},
		{name: "LQ group under the minimum score", version: "movie-remux",
			rel:  rel("Dune.Part.Two.2024.2160p.WEBRip.x265-YTS", "public", 8*gb),
			want: "CustomFormatMinimumScore", quality: "WEBRip-2160p", formats: "LQ", score: -10000},
		{name: "below the minimum size", version: "movie-remux",
			rel:  rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-FraMeSToR", "public", 2*gb),
			want: "BelowMinimumSize", quality: "Remux-2160p"},
		{name: "another movie", version: "movie-remux",
			rel:  rel("Some.Other.Movie.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 20*gb),
			want: "UnknownMovie", quality: "WEBDL-2160p"},
		{name: "wrong year", version: "movie-remux",
			rel:  rel("Dune.Part.Two.2019.2160p.WEB-DL.DDP5.1-FLUX", "public", 20*gb),
			want: "UnknownMovie"},
		{name: "indexer imdb id maps despite the title", version: "movie-remux",
			rel:      rel("Dune.2.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 20*gb),
			mutateFn: func(r *Release) { r.IMDb = "tt15239678" },
			quality:  "WEBDL-2160p", match: MatchID, formats: "WEB Tier 01", score: 1700},
		{name: "unparseable", version: "movie-remux", rel: rel("dune two", "public", 20*gb), want: "UnableToParse"},
		{name: "private-labelled release on a debrid version (privacy is a label)", version: "movie-remux",
			rel:      rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			mutateFn: func(r *Release) { r.Privacy = policy.PrivacyPrivate },
			quality:  "WEBDL-2160p", formats: "WEB Tier 01", score: 1700},
		{name: "private release on a seedbox version", version: "movie-seedbox",
			rel:      rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "tracker", 25*gb),
			mutateFn: func(r *Release) { r.Privacy = policy.PrivacyPrivate },
			quality:  "WEBDL-2160p"},
		{name: "indexer outside the version", version: "movie-seedbox",
			rel:  rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "multi", 25*gb),
			want: "IndexerNotInVersion"},
		{name: "a release added by hand is not held to the version's indexers", version: "movie-seedbox",
			rel:     rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", policy.ManualIndexer, 25*gb),
			quality: "WEBDL-2160p"},
		{name: "a release added by hand: no size, seeders unknown", version: "movie-remux",
			rel: rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-FraMeSToR", policy.ManualIndexer, 0),
			mutateFn: func(r *Release) {
				r.Kind, r.Privacy, r.Seeders, r.Peers = "", "", -1, -1
			},
			quality: "Remux-2160p"},
		{name: "MULTi from an indexer with multi languages", version: "movie-remux",
			rel:   rel("Dune.Part.Two.2024.MULTi.2160p.WEB-DL.DDP5.1-FLUX", "multi", 25*gb),
			langs: "French,English", formats: "French audio,WEB Tier 01", score: 1710},
		{name: "MULTi from an indexer without", version: "movie-remux",
			rel:   rel("Dune.Part.Two.2024.MULTi.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			langs: "English", formats: "WEB Tier 01", score: 1700},
		{name: "indexer languages win", version: "movie-remux",
			rel:      rel("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			mutateFn: func(r *Release) { r.Languages = []string{"French", "Klingon"} },
			langs:    "French", formats: "French audio,WEB Tier 01", score: 1710},
		{name: "freeleech from the volume factor", version: "movie-remux",
			rel:      rel("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			mutateFn: func(r *Release) { r.Extras = map[string]string{"downloadvolumefactor": "0"} },
			formats:  "Freeleech,WEB Tier 01", score: 1701},
		{name: "from_arr profile", version: "movie-shadow",
			rel:     rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb),
			quality: "Remux-2160p", formats: "HDR,Remux Tier 01,TrueHD ATMOS", score: 500 + 1950 + 5000},
		{name: "from_arr profile wants the original language", version: "movie-shadow",
			rel:   rel("Dune.Part.Two.2024.ENGLISH.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb),
			title: &Title{ID: 7, Name: "Dune: Part Two", Year: 2024, Runtime: 166, OriginalLanguage: "fr"},
			want:  "WantedLanguage", langs: "English"},
		{name: "policy language original: the original language", version: "movie-original",
			rel:   rel("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			langs: "English"},
		{name: "policy language original: another language", version: "movie-original",
			rel:   rel("Dune.Part.Two.2024.ENGLISH.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			title: &Title{ID: 7, Name: "Dune: Part Two", Year: 2024, Runtime: 166, OriginalLanguage: "ja"},
			want:  "WantedLanguage", langs: "English"},
		{name: "policy language original: a release without languages takes the original (AggregateLanguages)",
			version: "movie-original",
			rel:     rel("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			title:   &Title{ID: 7, Name: "Dune: Part Two", Year: 2024, Runtime: 166, OriginalLanguage: "ja"},
			langs:   "Japanese"},
		{name: "policy language French", version: "movie-french",
			rel:  rel("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			want: "WantedLanguage", langs: "English"},
		{name: "policy language French: a French release", version: "movie-french",
			rel:   rel("Dune.Part.Two.2024.FRENCH.2160p.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
			langs: "French"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.rel
			if c.mutateFn != nil {
				c.mutateFn(&r)
			}
			ti := dune
			if c.title != nil {
				ti = *c.title
			}
			d, err := e.Decide(ctx, r, ti, Version{Key: c.version}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := reasons(d); got != c.want {
				t.Errorf("rejections %q (%v), want %q", got, d.Rejections, c.want)
			}
			if d.Accepted != (c.want == "") {
				t.Errorf("accepted %v", d.Accepted)
			}
			if c.quality != "" && d.Quality != c.quality {
				t.Errorf("quality %s, want %s", d.Quality, c.quality)
			}
			if c.formats != "" && formatNames(d.Formats) != c.formats {
				t.Errorf("formats %s, want %s", formatNames(d.Formats), c.formats)
			}
			if c.formats != "" && d.Score != c.score {
				t.Errorf("score %d, want %d (%+v)", d.Score, c.score, d.Formats)
			}
			if c.match != "" && d.MatchType != c.match {
				t.Errorf("match %q, want %q", d.MatchType, c.match)
			}
			if c.langs != "" && strings.Join(d.Languages, ",") != c.langs {
				t.Errorf("languages %v, want %s", d.Languages, c.langs)
			}
		})
	}
}

func TestDecideUpgrade(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	ctx := context.Background()
	webFile := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX",
		Added: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), ReleaseGroup: "FLUX", Languages: []string{"English"}, Size: 25 * gb, RelativePath: "Dune Part Two (2024) [WEBDL-2160p]-FLUX.mkv"}
	remuxFile := &ExistingFile{Quality: "Remux-2160p", Revision: 1,
		SceneName:    "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR",
		ReleaseGroup: "FraMeSToR", Languages: []string{"English"}, Size: 70 * gb}
	plainRemux := &ExistingFile{Quality: "Remux-2160p", Revision: 1, SceneName: "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX-GRP",
		ReleaseGroup: "GRP", Languages: []string{"English"}, Size: 60 * gb}
	remux := rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb)
	web := rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb)
	repack := rel("Dune.Part.Two.2024.REPACK.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb)
	cases := []struct {
		name     string
		rel      Release
		file     *ExistingFile
		want     string
		upgrade  bool
		why      string
		existing int
	}{
		{"remux over a web file", remux, webFile, "", true, "better quality: Remux-2160p over WEBDL-2160p", 1700},
		{"web under a remux file", web, remuxFile, "DiskHigherPreference", false,
			"Existing file on disk is of equal or higher preference: Remux-2160p v1", 2450},
		{"same quality, better score", remux, plainRemux, "", true, "better format score: 2450 over 0", 0},
		{"same release again", remux, remuxFile, "DiskCustomFormatScore", false,
			"Existing file on disk has a equal or higher Custom Format score: 2450", 2450},
		{"repack of the same group", repack, webFile, "", true, "better revision: v2 over v1", 1700},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := e.Decide(ctx, c.rel, dune, Version{Key: "movie-remux"}, c.file)
			if err != nil {
				t.Fatal(err)
			}
			if got := reasons(d); got != c.want {
				t.Errorf("rejections %q %v, want %q", got, d.Rejections, c.want)
			}
			if d.Upgrade != c.upgrade || d.UpgradeReason != c.why {
				t.Errorf("upgrade %v %q, want %v %q", d.Upgrade, d.UpgradeReason, c.upgrade, c.why)
			}
			if d.Existing == nil || d.Existing.Score != c.existing {
				t.Errorf("existing %+v, want score %d", d.Existing, c.existing)
			}
		})
	}
}

func TestRank(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	ctx := context.Background()
	var ds []*Decision
	for _, r := range []Release{
		rel("Dune.Part.Two.2024.1080p.BluRay.x264-GRP", "public", 12*gb),
		rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb),
		rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-GRP", "public", 60*gb),
		rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb),
		rel("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-NTb", "public", 20*gb),
		rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-TINY", "public", 1*gb),
	} {
		d, err := e.Decide(ctx, r, dune, Version{Key: "movie-remux"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ds = append(ds, d)
	}
	var got []string
	for _, d := range Rank(ds) {
		got = append(got, d.Release[strings.LastIndex(d.Release, "-")+1:])
	}
	// Remux first (quality), FraMeSToR over GRP (score); WEB by score then peers/size; then the rejected ones, also
	// best first: the undersized remux before the 1080p it follows in the input.
	if want := "FraMeSToR,GRP,FLUX,NTb,TINY,GRP"; strings.Join(got, ",") != want {
		t.Errorf("rank %s, want %s", strings.Join(got, ","), want)
	}
}

func TestIndexerFlags(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		extras map[string]string
		tags   []string
		want   int
	}{
		{nil, nil, 0},
		{map[string]string{"downloadvolumefactor": "0"}, nil, 1},
		{map[string]string{"downloadvolumefactor": "0.5", "uploadvolumefactor": "2"}, nil, 2 | 4},
		{map[string]string{"downloadvolumefactor": "0.25"}, []string{"Internal", "scene", "freeleech"}, 256 | 32 | 128},
		{map[string]string{"downloadvolumefactor": "0.75"}, nil, 512},
	} {
		r := Release{Release: indexer.Release{Extras: c.extras, Flags: c.tags}}
		if got := indexerFlags(format.Radarr, &r); got != c.want {
			t.Errorf("%v %v: %d, want %d", c.extras, c.tags, got, c.want)
		}
	}
	v := 5
	if got := indexerFlags(format.Radarr, &Release{IndexerFlags: &v}); got != 5 {
		t.Errorf("recorded flags: %d", got)
	}
}

func TestMovieMapping(t *testing.T) {
	t.Parallel()
	alien3 := Title{ID: 1, Name: "Alien³", Year: 1992, Aliases: []string{"Alien 3"}}
	rocky := Title{ID: 2, Name: "Rocky II", Year: 1979}
	other := &Title{ID: 3, Name: "Other", Year: 2024, IMDb: "tt1234567"}
	lib := libMap{"tt1234567": other}
	e := testEngine(t)
	for _, c := range []struct {
		name  string
		title Title
		rel   Release
		lib   Library
		want  string
	}{
		{"alias", alien3, rel("Alien.3.1992.2160p.WEB-DL-FLUX", "public", 20*gb), nil, ""},
		{"roman numeral", rocky, rel("Rocky.2.1979.2160p.WEB-DL-FLUX", "public", 20*gb), nil, ""},
		{"secondary year", Title{ID: 4, Name: "Movie", Year: 2023, SecondaryYear: 2024},
			rel("Movie.2024.2160p.WEB-DL-FLUX", "public", 20*gb), nil, ""},
		{"id of another title in the library", rocky,
			func() Release {
				r := rel("Rocky.2.1979.2160p.WEB-DL-FLUX", "public", 20*gb)
				r.IMDb = "tt1234567"
				return r
			}(),
			// The IMDb id names "Other" (2024), whose year does not fit 1979: the id path fails and the title
			// matches the searched movie, as in the arr.
			lib, ""},
	} {
		e.library = c.lib
		d, err := e.Decide(context.Background(), c.rel, c.title, Version{Key: "movie-remux"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := reasons(d); got != c.want {
			t.Errorf("%s: %q %v", c.name, got, d.Rejections)
		}
	}
	// An id that fits another title maps there: wrong movie.
	e.library = lib
	r := rel("Other.2024.2160p.WEB-DL-FLUX", "public", 20*gb)
	r.IMDb = "tt1234567"
	d, err := e.Decide(context.Background(), r, dune, Version{Key: "movie-remux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reasons(d), "WrongMovie") {
		t.Errorf("wrong movie: %v", d.Rejections)
	}
	e.library = nil
}

type libMap map[string]*Title

func (l libMap) ByIMDb(id string) *Title { return l[id] }
func (l libMap) ByTMDb(int) *Title       { return nil }

func mustQuality(t *testing.T, name string) profile.Quality {
	t.Helper()
	q, ok := profile.QualityByName(format.Radarr, name)
	if !ok {
		t.Fatalf("no quality %s", name)
	}
	return q
}

func TestProfileRank(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	ctx := context.Background()
	webFile := &ExistingFile{Quality: "WEBRip-2160p", Revision: 1, SceneName: "Dune.Part.Two.2024.2160p.WEBRip.x265-GRP",
		ReleaseGroup: "GRP", Languages: []string{"English"}, Size: 20 * gb}
	cases := []struct {
		name          string
		rel           Release
		file          *ExistingFile
		rank, of, cur int
	}{
		// remux-2160p allows Remux-2160p over the WEB 2160p group: two ranks, the group's qualities share one.
		{"remux", rel("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "public", 60*gb), webFile, 1, 2, 2},
		{"web in the group", rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb), webFile, 2, 2, 2},
		{"not allowed", rel("Dune.Part.Two.2024.1080p.BluRay.x264-GRP", "public", 10*gb), nil, 0, 2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := e.Decide(ctx, c.rel, dune, Version{Key: "movie-remux"}, c.file)
			if err != nil {
				t.Fatal(err)
			}
			if d.ProfileRank != c.rank || d.ProfileRanks != c.of {
				t.Errorf("%s: rank %d of %d, want %d of %d", d.Quality, d.ProfileRank, d.ProfileRanks, c.rank, c.of)
			}
			if c.file != nil && (d.Existing == nil || d.Existing.ProfileRank != c.cur) {
				t.Errorf("existing %+v, want rank %d", d.Existing, c.cur)
			}
		})
	}
}
