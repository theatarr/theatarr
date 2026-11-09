package profile

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Cases follow ProperSpecificationFixture, TorrentSeedingSpecificationFixture and
// AlreadyImportedSpecificationFixture in Sonarr's DecisionEngineTests (Radarr has the same conditions).
func TestProper(t *testing.T) {
	now := time.Date(2026, 10, 27, 16, 0, 0, 0, time.FixedZone("local", 2*3600))
	boundary := time.Date(2026, 10, 20, 0, 0, 0, 0, now.Location())
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		for _, tc := range []struct {
			name     string
			added    time.Time
			mode     ProperDownloadTypes
			search   bool
			revision int
			reason   Reason
		}{
			{"old", boundary.Add(-time.Nanosecond), PreferAndUpgrade, false, 2, ReasonProperForOldFile},
			{"boundary", boundary, PreferAndUpgrade, false, 2, ""},
			{"recent", now, PreferAndUpgrade, false, 2, ""},
			{"disabled", now, DoNotUpgrade, false, 2, ReasonPropersDisabled},
			{"not preferred", boundary, DoNotPrefer, false, 2, ""},
			{"search", boundary.Add(-time.Hour), DoNotUpgrade, true, 2, ""},
			{"not revision upgrade", boundary.Add(-time.Hour), DoNotUpgrade, false, 1, ""},
		} {
			t.Run(string(fl)+"/"+tc.name, func(t *testing.T) {
				e := &Engine{Flavour: fl, Config: Config{DownloadPropersAndRepacks: tc.mode}, Now: func() time.Time { return now }}
				q := NewQualityModel(MustQuality(fl, "WEBDL-1080p"))
				f := &File{Quality: q, Added: tc.added}
				s := &Subject{Media: &Media{MovieFile: f}, Episodes: []*Episode{{File: f}}, Release: &Release{Quality: q}}
				s.Release.Quality.Revision.Version = tc.revision
				if tc.search {
					s.Search = &Search{}
				}
				sp, _ := e.Spec("Proper")
				got := e.Run(sp, s)
				if got.Reason != tc.reason {
					t.Fatalf("got %+v, want %s", got, tc.reason)
				}
			})
		}
	}
}

func TestProperChecksEveryHeldFile(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	e := &Engine{Flavour: format.Sonarr, Now: func() time.Time { return now }}
	q := NewQualityModel(MustQuality(format.Sonarr, "WEBDL-1080p"))
	sp, _ := e.Spec("Proper")
	for _, old := range []int{0, 1} {
		s := &Subject{Episodes: []*Episode{{File: &File{Quality: q, Added: now}}, {File: &File{Quality: q, Added: now}}, {}}, Release: &Release{Quality: q}}
		s.Release.Quality.Revision.Version = 2
		s.Episodes[old].File.Added = now.AddDate(0, 0, -30)
		if d := e.Run(sp, s); d.Reason != ReasonProperForOldFile {
			t.Fatalf("old file %d: %+v", old, d)
		}
		// A revision on a different quality is not a revision upgrade of either file.
		s.Release.Quality.Quality = MustQuality(format.Sonarr, "Bluray-1080p")
		if d := e.Run(sp, s); !d.Accepted {
			t.Fatalf("better quality: %+v", d)
		}
	}
}

func TestTorrentSeeding(t *testing.T) {
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		for _, search := range []bool{false, true} {
			for _, tc := range []struct {
				name     string
				seeders  *int
				minimum  int
				indexer  string
				protocol Protocol
				reject   bool
			}{
				{"below", new(0), 1, "public", ProtocolTorrent, true},
				{"equal", new(1), 1, "public", ProtocolTorrent, false},
				{"above", new(1000), 1, "public", ProtocolTorrent, false},
				{"unknown", nil, 1, "public", ProtocolTorrent, false},
				{"unknown sentinel", new(-1), 1, "public", ProtocolTorrent, false},
				{"disabled", new(0), 0, "public", ProtocolTorrent, false},
				{"no indexer", new(0), 1, "", ProtocolTorrent, false},
				{"usenet", new(0), 1, "public", ProtocolUsenet, false},
			} {
				t.Run(string(fl)+"/"+tc.name, func(t *testing.T) {
					e := &Engine{Flavour: fl}
					s := &Subject{Release: &Release{Seeders: tc.seeders, MinimumSeeders: tc.minimum, Indexer: tc.indexer, Protocol: tc.protocol}}
					if search {
						s.Search = &Search{}
					}
					sp, _ := e.Spec("TorrentSeeding")
					d := e.Run(sp, s)
					if (d.Reason == ReasonMinimumSeeders) != tc.reject {
						t.Fatalf("search %v: %+v", search, d)
					}
				})
			}
		}
	}
}

func TestAlreadyImported(t *testing.T) {
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		for _, tc := range []struct {
			name   string
			change func(*Engine, *Subject, *ImportedGrab)
			reason Reason
		}{
			{"same hash", func(e *Engine, s *Subject, g *ImportedGrab) {}, ReasonAlreadyImportedSameHash},
			{"same name without hash", func(e *Engine, s *Subject, g *ImportedGrab) { s.Release.InfoHash = ""; s.Release.Title = "RELEASE" }, ReasonAlreadyImportedSameName},
			{"different hash same name", func(e *Engine, s *Subject, g *ImportedGrab) { s.Release.InfoHash = "other" }, ReasonAlreadyImportedSameName},
			{"different release", func(e *Engine, s *Subject, g *ImportedGrab) { s.Release.InfoHash = "other"; s.Release.Title = "other" }, ""},
			{"no import", func(e *Engine, s *Subject, g *ImportedGrab) { g.Imported = false }, ""},
			{"equal quality", func(e *Engine, s *Subject, g *ImportedGrab) { g.ImportedQuality = g.GrabbedQuality }, ""},
			{"different revision", func(e *Engine, s *Subject, g *ImportedGrab) {
				g.ImportedQuality = g.GrabbedQuality
				g.ImportedQuality.Revision.Version++
			}, ReasonAlreadyImportedSameHash},
			{"cdh disabled", func(e *Engine, s *Subject, g *ImportedGrab) { e.Config.CompletedDownloadHandlingDisabled = true }, ""},
			{"no file", func(e *Engine, s *Subject, g *ImportedGrab) { s.Media.MovieFile = nil; s.Episodes[0].File = nil }, ""},
			{"no grab", func(e *Engine, s *Subject, g *ImportedGrab) {
				s.Media.MovieFile.LastGrab = nil
				s.Episodes[0].LastGrab = nil
			}, ""},
			{"usenet title", func(e *Engine, s *Subject, g *ImportedGrab) { s.Release.Protocol = ProtocolUsenet }, ReasonAlreadyImportedSameName},
		} {
			t.Run(string(fl)+"/"+tc.name, func(t *testing.T) {
				e := &Engine{Flavour: fl}
				g := &ImportedGrab{DownloadID: "HASH", SourceTitle: "release", Imported: true, GrabbedQuality: NewQualityModel(MustQuality(fl, "WEBDL-1080p")), ImportedQuality: NewQualityModel(MustQuality(fl, "HDTV-720p"))}
				f := &File{LastGrab: g}
				s := &Subject{Media: &Media{MovieFile: f}, Episodes: []*Episode{{File: f, LastGrab: g}}, Release: &Release{Protocol: ProtocolTorrent, InfoHash: "hash", Title: "release"}}
				tc.change(e, s, g)
				sp, _ := e.Spec("AlreadyImported")
				got := e.Run(sp, s)
				if got.Reason != tc.reason {
					t.Fatalf("%+v, want %s", got, tc.reason)
				}
			})
		}
	}
}

// EpisodeImport/Specifications/EpisodeTitleSpecificationFixture: the hold ends inclusively at 48 hours.
func TestEpisodeTitle(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, title, mode string
		air               *time.Time
		required          bool
		bulk              int
		reason            Reason
	}{
		{"tba", "TBA", "", new(now), true, 0, ReasonTitleTba},
		{"empty", " \t", "", new(now), true, 0, ReasonTitleMissing},
		{"unknown date", "TBA", "", nil, true, 0, ReasonTitleTba},
		{"future", "TBA", "", new(now.Add(time.Hour)), true, 0, ReasonTitleTba},
		{"boundary", "TBA", "", new(now.Add(-48 * time.Hour)), true, 0, ""},
		{"before boundary", "TBA", "", new(now.Add(-48*time.Hour + time.Nanosecond)), true, 0, ReasonTitleTba},
		{"real title", "Pilot", "", new(now), true, 0, ""},
		{"case sensitive", "tba", "", new(now), true, 0, ""},
		{"unused token", "TBA", "", new(now), false, 0, ""},
		{"never", "TBA", "Never", nil, true, 0, ""},
		{"bulk three", "TBA", "BulkSeasonReleases", new(now), true, 3, ""},
		{"bulk four", "TBA", "BulkSeasonReleases", new(now), true, 4, ReasonTitleTba},
		{"bulk unknown date", "TBA", "BulkSeasonReleases", nil, true, 0, ReasonTitleTba},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{Flavour: format.Sonarr, Config: Config{EpisodeTitleRequired: tc.mode}, Now: func() time.Time { return now }}
			s := &Subject{Media: &Media{EpisodeTitleRequired: tc.required}, Episodes: []*Episode{{Title: tc.title, AirDateUtc: tc.air, SeasonNumber: 2, EpisodeNumber: 1}}}
			for i := 0; i < tc.bulk; i++ {
				s.Media.Episodes = append(s.Media.Episodes, &Episode{SeasonNumber: 1, AirDateUtc: tc.air})
			}
			if d := EpisodeTitleCheck(e, s); d.Reason != tc.reason {
				t.Fatalf("%+v, want %s", d, tc.reason)
			}
		})
	}
}

func TestEpisodeTitleChecksEveryMappedEpisode(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	e := &Engine{Flavour: format.Sonarr, Config: Config{EpisodeTitleRequired: "BulkSeasonReleases"}, Now: func() time.Time { return now }}
	first := &Episode{Title: "Pilot", AirDateUtc: new(now), SeasonNumber: 1, EpisodeNumber: 1}
	second := &Episode{Title: "TBA", AirDateUtc: new(now.Add(time.Hour)), SeasonNumber: 1, EpisodeNumber: 2}
	s := &Subject{Media: &Media{EpisodeTitleRequired: true, Episodes: []*Episode{first, second}}, Episodes: []*Episode{first, second}}
	if d := EpisodeTitleCheck(e, s); d.Reason != ReasonTitleTba {
		t.Fatalf("different air dates must not skip the hold: %+v", d)
	}
	second.AirDateUtc = nil
	if d := EpisodeTitleCheck(e, s); !d.Accepted {
		t.Fatalf("bulk exemption allows unknown dates with fewer than four episodes: %+v", d)
	}
	e.Config.EpisodeTitleRequired = "Always"
	if d := EpisodeTitleCheck(e, s); d.Reason != ReasonTitleTba {
		t.Fatalf("Always must check the second mapped episode: %+v", d)
	}
}
