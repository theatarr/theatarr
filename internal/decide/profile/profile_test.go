package profile

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

func TestSizeSuffix(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {1, "1.0 B"}, {1023, "1,023.0 B"}, {1024, "1.0 KB"}, {1280, "1.3 KB"}, {1536, "1.5 KB"},
		{2 << 30, "2.0 GB"}, {1288490188, "1.2 GB"}, {-1024, "-1.0 KB"}, {1 << 40, "1.0 TB"},
	} {
		if got := SizeSuffix(c.in); got != c.want {
			t.Errorf("SizeSuffix(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMegabytesAndRound(t *testing.T) {
	if Megabytes(1.5) != 1572864 || Megabytes(17.1) != 17930650 {
		t.Errorf("Megabytes: %d %d", Megabytes(1.5), Megabytes(17.1))
	}
	const mb = 1024 * 1024
	for _, c := range []struct {
		fl   format.Flavour
		n    int64
		want int64
	}{
		{format.Radarr, 300 * mb, 200 * mb}, {format.Sonarr, 300 * mb, 200 * mb},
		{format.Radarr, -300 * mb, -200 * mb}, {format.Sonarr, -300 * mb, -400 * mb},
		{format.Sonarr, -400 * mb, -400 * mb},
	} {
		if got := roundTo(c.fl, c.n, 200*mb); got != c.want {
			t.Errorf("%s roundTo(%d) = %d, want %d", c.fl, c.n/mb, got/mb, c.want/mb)
		}
	}
}

func TestDefaultsCoverQualities(t *testing.T) {
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		d := DefaultDefinitions(fl)
		if len(d.All()) != len(Qualities(fl)) {
			t.Errorf("%s: %d definitions for %d qualities", fl, len(d.All()), len(Qualities(fl)))
		}
	}
	// The Sonarr parser port carries the weights for its own comparisons; they must agree.
	if w := Weight(format.Sonarr, MustQuality(format.Sonarr, "Bluray-1080p Remux")); w != 14 {
		t.Errorf("weight %d", w)
	}
}

func radarrSubject(t *testing.T) (*Engine, *Subject) {
	t.Helper()
	fl := format.Radarr
	web := DefaultProfile(fl, "Test", nil, []Quality{MustQuality(fl, "WEBDL-2160p"), MustQuality(fl, "WEBRip-2160p"),
		MustQuality(fl, "Remux-2160p")}, nil)
	web.UpgradeAllowed = true
	web.Cutoff = MustQuality(fl, "Remux-2160p").ID
	e := &Engine{Flavour: fl, Definitions: DefaultDefinitions(fl)}
	s := &Subject{Profile: web, Media: &Media{Title: "A Movie", Runtime: 100}, Release: &Release{
		Title: "A.Movie.2020.2160p.WEB-DL.DDP5.1-GRP", Size: 1 << 30, Quality: NewQualityModel(MustQuality(fl, "WEBDL-2160p")),
	}}
	return e, s
}

func TestMessages(t *testing.T) {
	e, s := radarrSubject(t)
	def := *e.Definitions.Get(s.Release.Quality.Quality)
	min := 20.0
	def.MinSize = &min
	e.Definitions.Set(def)
	sp, _ := e.Spec("AcceptableSize")
	d := e.Run(sp, s)
	if want := "1.0 GB is smaller than minimum allowed 2.0 GB (for A Movie)"; d.Message != want || d.Reason != ReasonBelowMinimumSize {
		t.Errorf("got %s %q, want %q", d.Reason, d.Message, want)
	}

	e, s = radarrSubject(t)
	s.Media.MovieFile = &File{Quality: NewQualityModel(MustQuality(format.Radarr, "Bluray-2160p"))}
	sp, _ = e.Spec("UpgradeDisk")
	d = e.Run(sp, s)
	if want := "Existing file on disk is of equal or higher preference: Bluray-2160p v1"; d.Message != want {
		t.Errorf("got %q, want %q", d.Message, want)
	}
	s.Profile.UpgradeAllowed = false
	d = e.Run(sp, s)
	if want := "Existing file meets cutoff: WEB 2160p (WEBDL-2160p, WEBRip-2160p) []"; d.Message != want || d.Reason != ReasonDiskCutoffMet {
		t.Errorf("got %s %q, want %q", d.Reason, d.Message, want)
	}

	// Several rejections come back together, in specification order.
	e, s = radarrSubject(t)
	s.Release.Title = "A.Movie.sample"
	s.Release.Size = 1 << 20
	s.Release.Quality = NewQualityModel(MustQuality(format.Radarr, "DVD"))
	s.Profile.Language = format.LanguageAny
	rs := e.Evaluate(s)
	if len(rs) != 2 || rs[0].Spec != "NotSampleSpecification" || rs[1].Reason != ReasonQualityNotWanted ||
		rs[1].Message != "DVD is not wanted in profile" {
		t.Errorf("rejections %v", rs)
	}
}

func TestSonarrRuntimeMessage(t *testing.T) {
	fl := format.Sonarr
	e := &Engine{Flavour: fl}
	p := DefaultProfile(fl, "Any", nil, Qualities(fl), nil)
	s := &Subject{Profile: p, Media: &Media{Runtime: 60}, Release: &Release{Size: 1 << 30,
		Quality: NewQualityModel(MustQuality(fl, "HDTV-720p"))}, Episodes: []*Episode{{SeasonNumber: 1, EpisodeNumber: 1}, {SeasonNumber: 1, EpisodeNumber: 2}}}
	sp, _ := e.Spec("AcceptableSize")
	d := e.Run(sp, s)
	// HDTV-720p: min 3 MB/min; 120 min -> 360 MB, max 125 -> 15,000 MB.
	if d.Accepted != true {
		t.Fatalf("%v", d)
	}
	s.Release.Size = 100 << 20
	d = e.Run(sp, s)
	if want := "100.0 MB is smaller than minimum allowed 360.0 MB (for 2x 120min)"; d.Message != want {
		t.Errorf("got %q, want %q", d.Message, want)
	}
	// No runtime and no pilot: the arr throws, which becomes a DecisionError.
	s.Media.Runtime = 0
	d = e.Run(sp, s)
	if d.Reason != ReasonDecisionError {
		t.Errorf("got %v", d)
	}
	// Within a day of the pilot the series counts as 45 minutes.
	now := time.Now()
	s.Media.Pilot = &Episode{SeasonNumber: 1, AirDateUtc: &now}
	at := now.Add(24 * time.Hour)
	s.Episodes[1].AirDateUtc = &at // Before is <=
	d = e.Run(sp, s)
	if want := "100.0 MB is smaller than minimum allowed 270.0 MB (for 2x 90min)"; d.Message != want {
		t.Errorf("got %q, want %q", d.Message, want)
	}
}

func TestRadarrAgeCompare(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	e, a := radarrSubject(t)
	e.Now = func() time.Time { return now }
	_, b := radarrSubject(t)
	b.Profile = a.Profile
	for _, s := range []*Subject{a, b} {
		s.Release.Protocol = ProtocolUsenet
	}
	a.Release.PublishDate = now.Add(-10 * 24 * time.Hour)  // log10(10) = 1 -> -1
	b.Release.PublishDate = now.Add(-400 * 24 * time.Hour) // log10(400) = 2.6 -> -3
	if c := e.Compare(a, b); c <= 0 {
		t.Errorf("Radarr: newer usenet release should win, got %d", c)
	}
	e.Flavour = format.Sonarr // Sonarr scores every age above a week the same
	a.Profile.Flavour, b.Profile.Flavour = format.Sonarr, format.Sonarr
	if c := e.compareAge(a, b); c != 0 {
		t.Errorf("Sonarr: got %d", c)
	}
}
