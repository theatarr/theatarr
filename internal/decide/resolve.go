package decide

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
)

// Episode resolution for an imported file: port of Sonarr
// src/NzbDrone.Core/MediaFiles/EpisodeImport/Aggregation/Aggregators/AggregateEpisodes.cs @ cab419a (GetEpisodes,
// GetBestEpisodeInfo, PreferOtherEpisodeInfo, GetSpecialEpisodeInfo) over the same mapping the decision runs
// (series.go: ParsingService.GetEpisodes with the series given, so scene numbering, daily air dates, absolute numbers
// and specials resolve as at grab time), then the anime seam (anime.go). The library calls it for every video file
// of a series release, so a file maps to the episodes its release was accepted for.
//
// Deviations:
//   - The franchise seam runs first, as in the decision. The alternate cut is matched on the file's name, then on
//     the release title; the decision's mapping (EpisodeMap, stored on the download) is the fallback when neither
//     names an entry or a cut, so a file named only "01" inside an entry's batch still maps through that entry.
//   - An entry whose mapping is not importable is skipped (Sonarr's numbering applies); library/animerelink.go runs
//     this again for a series once review accepts its mapping.

// EpisodeMap is how a decision mapped a series release when it was not Sonarr's own mapping: through a franchise
// entry (How AnimeMapFranchise, Entry the entry key) or an alternate cut (How "alt_cut:<name>"). The router stores it
// on the download (download.episode_map), and the import maps the release's files the same way.
type EpisodeMap struct {
	How   string `json:"how"`
	Entry string `json:"entry,omitempty"`
}

// EpisodeMapping is the decision's EpisodeMap, nil for Sonarr's mapping (or a movie).
func (d *Decision) EpisodeMapping() *EpisodeMap {
	if d == nil || d.AnimeMapping == "" || d.AnimeMapping == AnimeMapSonarr {
		return nil
	}
	return &EpisodeMap{How: d.AnimeMapping, Entry: d.AnimeEntry}
}

// FileInput is one video file of a series release, as the import sees it.
type FileInput struct {
	// Path is the file's path inside the release folder (sub-folders included).
	Path string
	// Release is the release title (Sonarr's download client item title); Folder the release folder's name.
	Release, Folder string
	// OtherVideoFiles is set when the release has more video files than this one (a pack).
	OtherVideoFiles bool
	// Mapping is the decision's EpisodeMap, when it had one.
	Mapping *EpisodeMap
}

// FileEpisodes is what ResolveFile found: the episode ids (of Title.Episodes), in order, and how: the parse used
// ("file", "folder", "release", "special") and, for the anime seam, its mapping ("franchise:<entry>",
// "alt_cut:<name>").
type FileEpisodes struct {
	EpisodeIDs []int64
	How        string
	// Refused is an authoritative mapping failure. Callers must not try canonical numbering.
	Refused string
}

// ResolveFile maps one file of a release to episodes of the series t (loaded with LoadSeriesVersion), with the
// version's Sonarr parser ("" is v4). No episodes is not an error: the file is unmatched.
func ResolveFile(t *Title, sonarrVersion string, in FileInput) (FileEpisodes, error) {
	ver, err := sonarr.ParseVersion(sonarrVersion)
	if err != nil {
		return FileEpisodes{}, fmt.Errorf("decide: %w", err)
	}
	s := newSeries(t, ver)
	s.known = true
	p, how := s.bestFileInfo(in)
	if p == nil {
		return FileEpisodes{}, nil
	}
	if !sonarr.ValidateForSeriesType(p, s.typ) {
		return FileEpisodes{}, nil
	}
	if t.AnimeMap != nil {
		eps, mapped, ok, err := s.fileFranchise(p, in, t.AnimeMap)
		if err != nil {
			return FileEpisodes{}, err
		}
		if ok {
			res := FileEpisodes{EpisodeIDs: episodeIDs(eps), How: how + "+" + mapped}
			if len(eps) == 0 {
				res.Refused = "no episode in " + mapped + " for the file number"
			}
			return res, nil
		}
	}
	eps, err := s.fileEpisodes(p)
	if err != nil {
		return FileEpisodes{}, err
	}
	if len(eps) == 0 && p.IsPossibleSpecialEpisode() {
		if sp := s.parseSpecialFor(p, fileTitle(in.Path)); sp != nil {
			if eps, err = s.fileEpisodes(sp); err != nil {
				return FileEpisodes{}, err
			}
			how = "special"
		}
	}
	return FileEpisodes{EpisodeIDs: episodeIDs(eps), How: how}, nil
}

// fileEpisodes is ParsingService.GetEpisodes(parsed, series, sceneSource: true): Map with the series given.
func (s *series) fileEpisodes(p *sonarr.ParsedEpisodeInfo) ([]*Episode, error) {
	r, err := s.mapRelease(p, 0, "", nil)
	if err != nil {
		return nil, err
	}
	return r.episodes, nil
}

// bestFileInfo is GetBestEpisodeInfo: the file's parse (its path: file name, then folders), unless it is the only
// video file and its name is not a scene title, when the release title's parse (else the folder's) is preferred
// unless it is a full season or absolute where the file's is not; with no parse at all, the special-episode title.
func (s *series) bestFileInfo(in FileInput) (*sonarr.ParsedEpisodeInfo, string) {
	file := s.ver.ParsePath(in.Path)
	how := "file"
	if !in.OtherVideoFiles && !sonarr.IsSceneTitle(fileTitle(in.Path)) {
		var release, folder *sonarr.ParsedEpisodeInfo
		if in.Release != "" {
			release = s.ver.ParseTitle(in.Release)
		}
		if in.Folder != "" {
			folder = s.ver.ParseTitle(in.Folder)
		}
		switch {
		case release != nil && !release.FullSeason && preferOther(file, release):
			file, how = release, "release"
		case folder != nil && !folder.FullSeason && preferOther(file, folder):
			file, how = folder, "folder"
		}
	}
	if file == nil {
		return s.parseSpecialFor(nil, fileTitle(in.Path)), "special"
	}
	return file, how
}

// preferOther is PreferOtherEpisodeInfo.
func preferOther(file, other *sonarr.ParsedEpisodeInfo) bool {
	if file == nil {
		return true
	}
	// When the file's episode info is not absolute prefer it over a parsed episode info that is absolute
	return !(!file.IsAbsoluteNumbering() && other.IsAbsoluteNumbering())
}

// fileFranchise runs the anime seam for a file: the alternate cut its name (or else the release title) names, the
// franchise entry its series title names, else the decision's mapping. ok is false when none applies.
func (s *series) fileFranchise(p *sonarr.ParsedEpisodeInfo, in FileInput, am *animemap.Series) (eps []*Episode, how string, ok bool, err error) {
	// Stored entry/cut numbers are local absolute numbers. Explicit seasons and specials retain Sonarr's meaning.
	if !p.IsAbsoluteNumbering() || len(p.EpisodeNumbers) > 0 || p.Special || p.FullSeason ||
		p.IsSeasonExtra || len(p.SpecialAbsoluteEpisodeNumbers) > 0 || fileSpecialRE.MatchString(fileTitle(in.Path)) {
		return nil, "", false, nil
	}
	name := fileTitle(in.Path)
	for _, title := range []string{name, in.Release} {
		if title == "" {
			continue
		}
		for _, c := range am.AltCuts {
			if c.Matches(title) {
				eps, how, _, ok, _, err = s.franchiseEpisodes(p, title, am)
				return eps, how, ok, err
			}
		}
	}
	if eps, how, entry, ok, _, err := s.franchiseEpisodes(p, name, am); err != nil || ok {
		return eps, how + ":" + entry, ok, err
	}
	m := in.Mapping
	if m == nil {
		return nil, "", false, nil
	}
	nums := p.AbsoluteEpisodeNumbers
	if len(nums) == 0 || p.FullSeason {
		return nil, "", false, nil
	}
	if cut, isCut := strings.CutPrefix(m.How, AnimeMapAltCut+":"); isCut {
		for _, c := range am.AltCuts {
			if c.Name != cut {
				continue
			}
			for _, x := range nums {
				if len(c.Episodes[x]) == 0 {
					return nil, m.How, true, nil
				}
				for _, se := range c.Episodes[x] {
					e, err := s.find(se[0], se[1])
					if err != nil || e == nil {
						return nil, m.How, true, err
					}
					eps = append(eps, e)
				}
			}
			return eps, m.How, true, nil
		}
		return nil, m.How, true, nil
	}
	if m.How != AnimeMapFranchise {
		return nil, "", false, nil
	}
	for _, en := range am.Entries {
		if en.Key != m.Entry {
			continue
		}
		if !en.Importable() {
			return nil, "", false, nil // no longer importable since the grab: Sonarr's numbering
		}
		reg := en.Regular()
		for _, x := range nums {
			if x < 1 || x > len(reg) {
				return nil, "", false, nil
			}
			e, err := s.find(reg[x-1][0], reg[x-1][1])
			if err != nil || e == nil {
				return nil, "", false, err
			}
			eps = append(eps, e)
		}
		return eps, AnimeMapFranchise + ":" + en.Key, true, nil
	}
	return nil, "", false, nil
}

var fileSpecialRE = regexp.MustCompile(`(?i)\b(?:OVA|OAD|specials?)\b`)

// fileTitle is the file's name without its extension.
func fileTitle(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func episodeIDs(eps []*Episode) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, e := range eps {
		if !seen[e.ID] {
			seen[e.ID] = true
			out = append(out, e.ID)
		}
	}
	return out
}

// QualityRank is the release quality's place in the deciding profile (1 the profile's lowest item, higher is better;
// qualities in one group rank the same), so releases decided by one profile can be compared later without the
// engine. 0 when unknown (a rejected or unscored decision).
func (d *Decision) QualityRank() int {
	if d == nil || d.subject == nil || d.subject.Profile == nil || d.subject.Release == nil {
		return 0
	}
	return d.subject.Profile.GetIndex(d.subject.Release.Quality.Quality.ID, false).Index + 1
}
