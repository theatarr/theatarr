package profile

// Ports of Sonarr src/NzbDrone.Core/DecisionEngine/Specifications/{RssSync/ProperSpecification,
// TorrentSeedingSpecification,AlreadyImportedSpecification}.cs @ cab419a (Radarr @ a96bf7c has the same rules),
// and Sonarr MediaFiles/EpisodeImport/Specifications/EpisodeTitleSpecification.cs @ cab419a.

import (
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

const (
	ReasonPropersDisabled         Reason = "PropersDisabled"
	ReasonProperForOldFile        Reason = "ProperForOldFile"
	ReasonMinimumSeeders          Reason = "MinimumSeeders"
	ReasonAlreadyImportedSameHash Reason = "AlreadyImportedSameHash"
	ReasonAlreadyImportedSameName Reason = "AlreadyImportedSameName"
	ReasonTitleMissing            Reason = "TitleMissing"
	ReasonTitleTba                Reason = "TitleTba"
)

// ProperSpecification.cs:27-60: only RSS restricts revision upgrades to recently added files.
func proper(e *Engine, s *Subject) Decision {
	if s.Search != nil || e.Config.DownloadPropersAndRepacks == DoNotPrefer {
		return Accept()
	}
	now := e.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, f := range e.files(s) {
		if !isRevisionUpgrade(f.Quality, s.Release.Quality) {
			continue
		}
		if e.Config.DownloadPropersAndRepacks == DoNotUpgrade {
			return Reject(ReasonPropersDisabled, "Proper downloading is disabled")
		}
		if f.Added.Before(today.AddDate(0, 0, -7)) {
			return Reject(ReasonProperForOldFile, "Proper for old file")
		}
	}
	return Accept()
}

// TorrentSeedingSpecification.cs:23-56: every search type, known torrent indexers and seed counts only.
func torrentSeeding(e *Engine, s *Subject) Decision {
	r := s.Release
	if r.Protocol == ProtocolTorrent && r.Indexer != "" && r.Seeders != nil && *r.Seeders >= 0 && *r.Seeders < r.MinimumSeeders {
		return Reject(ReasonMinimumSeeders, "Not enough seeders: {0}. Minimum seeders: {1}", *r.Seeders, r.MinimumSeeders)
	}
	return Accept()
}

// AlreadyImportedSpecification.cs:30-95 (Radarr:29-95): only the latest grab, with a matching import of a
// different quality, for an item that still has a file. Name matching also applies when a hash is present.
func alreadyImported(e *Engine, s *Subject) Decision {
	if e.Config.CompletedDownloadHandlingDisabled {
		return Accept()
	}
	var grabs []*ImportedGrab
	if e.Flavour == format.Radarr {
		if s.Media != nil && s.Media.MovieFile != nil {
			grabs = append(grabs, s.Media.MovieFile.LastGrab)
		}
	} else {
		for _, ep := range s.Episodes {
			if ep.File != nil {
				grabs = append(grabs, ep.LastGrab)
			}
		}
	}
	for _, g := range grabs {
		if g == nil || !g.Imported || (g.GrabbedQuality.Quality.ID == g.ImportedQuality.Quality.ID && g.GrabbedQuality.Revision.Equal(g.ImportedQuality.Revision)) {
			continue
		}
		r := s.Release
		if r.Protocol == ProtocolTorrent && r.InfoHash != "" && strings.ToUpper(r.InfoHash) == g.DownloadID {
			return Reject(ReasonAlreadyImportedSameHash, "Has same torrent hash as a grabbed and imported release")
		}
		if strings.EqualFold(r.Title, g.SourceTitle) {
			return Reject(ReasonAlreadyImportedSameName, "Has same release name as a grabbed and imported release")
		}
	}
	return Accept()
}

// EpisodeTitleSpecification.cs:39-93 (v5 has the same conditions): called by library import; it is not a grab rule.
// The BulkSeasonReleases lookup deliberately uses EpisodeNumber as the season argument, as Sonarr line 55 does.
func episodeTitle(e *Engine, s *Subject) Decision {
	if e == nil {
		e = &Engine{}
	}
	if strings.EqualFold(e.Config.EpisodeTitleRequired, "Never") || s.Media == nil || !s.Media.EpisodeTitleRequired || len(s.Episodes) == 0 {
		return Accept()
	}
	first := s.Episodes[0]
	if strings.EqualFold(e.Config.EpisodeTitleRequired, "BulkSeasonReleases") && first.AirDateUtc != nil {
		sameDay := true
		for _, ep := range s.Episodes {
			if ep.AirDateUtc != nil && !ep.AirDateUtc.Equal(*first.AirDateUtc) {
				sameDay = false
			}
		}
		count := 0
		for _, ep := range s.Media.Episodes {
			if ep.SeasonNumber == first.EpisodeNumber && (ep.AirDateUtc == nil || ep.AirDateUtc.Equal(*first.AirDateUtc)) {
				count++
			}
		}
		if sameDay && count < 4 {
			return Accept()
		}
	}
	for _, ep := range s.Episodes {
		// Sonarr's Before is inclusive (DateTimeExtensions.cs:27-30).
		now := time.Now()
		if e != nil {
			now = e.now()
		}
		if ep.AirDateUtc != nil && !ep.AirDateUtc.After(now.UTC().Add(-48*time.Hour)) {
			continue
		}
		if strings.TrimSpace(ep.Title) == "" {
			return Reject(ReasonTitleMissing, "Episode does not have a title and recently aired")
		}
		if ep.Title == "TBA" {
			return Reject(ReasonTitleTba, "Episode has a TBA title and recently aired")
		}
	}
	return Accept()
}

// EpisodeTitleCheck exposes the import specification without registering it as a grab-time rule.
func EpisodeTitleCheck(e *Engine, s *Subject) Decision {
	if e == nil {
		e = &Engine{}
	}
	return episodeTitle(e, s)
}
