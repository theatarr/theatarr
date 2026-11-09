package decide

import (
	"path"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// Per-file import aggregation: port of Sonarr src/NzbDrone.Core/MediaFiles/EpisodeImport/Aggregation (fork
// 9a75b78, the same @ cab419a; v5 a6171f0ca differs only in logging and the media info language list):
// AggregateQuality with its augmenters (file name, folder, download client item, media info, grabbed release name),
// AggregateReleaseGroup, AggregateReleaseHash, AggregateLanguage and SceneNameCalculator; and Radarr's
// MovieImport/Aggregation (AggregateQuality with the modifier, AggregateReleaseGroup, AggregateEdition,
// AggregateLanguage, SceneNameCalculator). A pack's files each get their own quality, group and languages: a
// "2160p REMUX" pack holding a 1080p WEB-DL file imports that file as WEBDL-1080p.
//
// Theatarr's sources: the download client item is the grabbed release title (the title the provider add carried), the
// folder is the release folder on the mount, the grabbed history title is the release title, and the media info is
// the import's probe.

// ImportFile is one video file of a release at import.
type ImportFile struct {
	// Path is the file's path as the arr sees it: the release folder's name, then the path inside it.
	Path string
	// Folder is the release folder's name; Client the download client item's title; Grabbed the grabbed release
	// title (history). "" when unknown.
	Folder, Client, Grabbed string
	// OtherVideoFiles is set when the release has other video files that are not samples.
	OtherVideoFiles bool
	// Media is the file's probe, nil when unread.
	Media *MediaInfo
	// EpisodeTitles are the titles of the file's episodes (series): languages they name are not the file's.
	EpisodeTitles []string
}

// MediaInfo is what the aggregation reads of a probe.
type MediaInfo struct {
	Width, Height  int
	Title          string
	AudioLanguages []string
}

// FileFacts is the aggregated view of one file: what the arr records on its EpisodeFile or MovieFile.
type FileFacts struct {
	Quality      string   `json:"quality"`
	Revision     int      `json:"revision"`
	Real         int      `json:"real,omitempty"`
	IsRepack     bool     `json:"is_repack,omitempty"`
	ReleaseGroup string   `json:"release_group,omitempty"`
	ReleaseHash  string   `json:"release_hash,omitempty"`
	Edition      string   `json:"edition,omitempty"`
	SceneName    string   `json:"scene_name,omitempty"`
	Languages    []string `json:"languages,omitempty"`
}

// confidence is Augmenters/Quality/Confidence.
type confidence int

const (
	confFallback confidence = iota
	confDefault
	confTag
	confMediaInfo
)

func detectionConf(name bool) confidence {
	if name {
		return confTag
	}
	return confFallback
}

// augmentQuality is AugmentQualityResult, for both flavours (modifier: Radarr only).
type augmentQuality struct {
	source, resolution, modifier             int
	sourceConf, resolutionConf, modifierConf confidence
	revision                                 *[2]int // version, real
	revisionConf                             confidence
}

// aggregateQuality is AggregateQuality.Aggregate over the augments in order. It returns source, resolution,
// modifier and revision.
func aggregateQuality(augments []*augmentQuality, unknownSource int) (source, resolution, modifier int, rev [2]int) {
	source, resolution, modifier = unknownSource, 0, 0
	sourceConf, resolutionConf, modifierConf, revConf := confDefault, confDefault, confDefault, confDefault
	rev = [2]int{1, 0}
	for _, a := range augments {
		if a == nil {
			continue
		}
		if source == unknownSource || (a.sourceConf > sourceConf && a.source != unknownSource) {
			source, sourceConf = a.source, a.sourceConf
		}
		if resolution == 0 || (a.resolutionConf > resolutionConf && a.resolution > 0) {
			resolution, resolutionConf = a.resolution, a.resolutionConf
		}
		if a.modifier > modifier || (a.modifierConf > modifierConf && a.modifier != 0) {
			modifier, modifierConf = a.modifier, a.modifierConf
		}
		if a.revision != nil {
			if a.revisionConf > revConf || a.revisionConf == revConf && revisionAbove(*a.revision, rev) {
				rev, revConf = *a.revision, a.revisionConf
			}
		}
	}
	return source, resolution, modifier, rev
}

// revisionAbove is Revision's > operator: real first, then version.
func revisionAbove(a, b [2]int) bool {
	if a[1] != b[1] {
		return a[1] > b[1]
	}
	return a[0] > b[0]
}

// mediaResolution is AugmentQualityFromMediaInfo's size classes.
func mediaResolution(w, h int) int {
	switch {
	case w >= 3200 || h >= 2100:
		return 2160
	case w >= 1800 || h >= 1000:
		return 1080
	case w >= 1200 || h >= 700:
		return 720
	case w >= 1000 || h >= 560:
		return 576
	case w > 0 && h > 0:
		return 480
	}
	return 0
}

func sonarrAugment(m sonarr.QualityModel) *augmentQuality {
	rev := [2]int{m.Revision.Version, m.Revision.Real}
	return &augmentQuality{source: int(m.Quality.Source), sourceConf: detectionConf(m.SourceDetectionSource == sonarr.DetectionName),
		resolution: m.Quality.Resolution, resolutionConf: detectionConf(m.ResolutionDetectionSource == sonarr.DetectionName),
		revision: &rev, revisionConf: detectionConf(m.RevisionDetectionSource == sonarr.DetectionName)}
}

func radarrAugment(m radarr.QualityModel) *augmentQuality {
	rev := [2]int{m.Revision.Version, m.Revision.Real}
	return &augmentQuality{source: int(m.Quality.Source), sourceConf: detectionConf(m.SourceDetectionSource == radarr.DetectionName),
		resolution: m.Quality.Resolution, resolutionConf: detectionConf(m.ResolutionDetectionSource == radarr.DetectionName),
		modifier: int(m.Quality.Modifier), modifierConf: detectionConf(m.ModifierDetectionSource == radarr.DetectionName),
		revision: &rev, revisionConf: detectionConf(m.RevisionDetectionSource == radarr.DetectionName)}
}

// mediaAugment is AugmentQualityFromMediaInfo: the resolution class, and the source (and modifier) when the
// container title names one.
func mediaAugment(m *MediaInfo, parse func(string) (source, modifier int, byName bool)) *augmentQuality {
	if m == nil {
		return nil
	}
	a := &augmentQuality{sourceConf: confDefault, modifierConf: confDefault, resolutionConf: confMediaInfo}
	if t := strings.TrimSpace(m.Title); t != "" {
		if src, mod, ok := parse(t); ok && src != 0 {
			a.source, a.modifier, a.sourceConf, a.modifierConf = src, mod, confMediaInfo, confMediaInfo
		}
	}
	if a.resolution = mediaResolution(m.Width, m.Height); a.resolution == 0 {
		return nil
	}
	return a
}

// aggregateLanguages is AggregateLanguage: the original language, replaced by each augment in order (file name,
// folder, download client item, media info) that names a known language. Nil when no augment named one: the arr
// would record the original language, Theatarr keeps the decision's languages (indexer data included) then.
func aggregateLanguages(augments ...[]string) []string {
	var langs []string
	for _, a := range augments {
		if len(a) == 0 || len(a) == 1 && a[0] == "Unknown" {
			continue
		}
		langs = a
	}
	return langs
}

func sonarrLanguages(p *sonarr.ParsedEpisodeInfo, ver sonarr.Version, episodeTitles []string) []string {
	if p == nil {
		return nil
	}
	drop := map[int]bool{}
	for _, t := range episodeTitles {
		for _, l := range ver.ParseLanguages(t) {
			drop[l.ID] = true
		}
	}
	var out []string
	for _, l := range p.Languages {
		if !drop[l.ID] {
			out = append(out, l.Name)
		}
	}
	return out
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// AggregateSeriesFile aggregates one file of a series release with the version's Sonarr parser ("" is v4).
func AggregateSeriesFile(sonarrVersion string, in ImportFile) (FileFacts, error) {
	ver, err := sonarr.ParseVersion(sonarrVersion)
	if err != nil {
		return FileFacts{}, err
	}
	file := ver.ParsePath(in.Path)
	var folder, client *sonarr.ParsedEpisodeInfo
	if in.Folder != "" {
		folder = ver.ParseTitle(in.Folder)
	}
	if in.Client != "" {
		client = ver.ParseTitle(in.Client)
	}
	quality := func(p *sonarr.ParsedEpisodeInfo) *augmentQuality {
		if p == nil {
			return nil
		}
		return sonarrAugment(p.Quality)
	}
	augments := []*augmentQuality{quality(file), quality(folder), quality(client),
		mediaAugment(in.Media, func(t string) (int, int, bool) {
			m := sonarr.ParseQualityName(t)
			return int(m.Quality.Source), 0, m.SourceDetectionSource == sonarr.DetectionName
		})}
	if in.Grabbed != "" {
		augments = append(augments, sonarrAugment(ver.ParseQuality(in.Grabbed)))
	}
	source, resolution, _, rev := aggregateQuality(augments, int(sonarr.SourceUnknown))
	f := FileFacts{Quality: sonarr.FindBySourceAndResolution(sonarr.QualitySource(source), resolution).Name,
		Revision: rev[0], Real: rev[1]}

	// AggregateReleaseGroup: the client's and the folder's unless a full season, the file's, then theirs anyway.
	group := func(p *sonarr.ParsedEpisodeInfo, skipFullSeason bool) string {
		if p == nil || p.FullSeason && skipFullSeason {
			return ""
		}
		return strOr(p.ReleaseGroup)
	}
	for _, g := range []string{group(client, true), group(folder, true), group(file, false), group(client, false),
		group(folder, false)} {
		if strings.TrimSpace(g) != "" {
			f.ReleaseGroup = g
			break
		}
	}
	// AggregateReleaseHash: the file's, the client's, the folder's; never a full season's.
	for _, p := range []*sonarr.ParsedEpisodeInfo{file, client, folder} {
		if p != nil && !p.FullSeason && strings.TrimSpace(p.ReleaseHash) != "" {
			f.ReleaseHash = p.ReleaseHash
			break
		}
	}
	f.Languages = aggregateLanguages(sonarrLanguages(file, ver, in.EpisodeTitles),
		sonarrLanguages(folder, ver, in.EpisodeTitles), sonarrLanguages(client, ver, in.EpisodeTitles),
		mediaLanguages(in.Media, func(code string) string {
			if l := ver.FindIsoLanguage(code); l != nil {
				return l.Language.Name
			}
			return ""
		}))

	// SceneNameCalculator.GetSceneName.
	switch name := fileStem(in.Path); {
	case !in.OtherVideoFiles && client != nil && !client.FullSeason:
		f.SceneName = sonarr.RemoveFileExtension(client.ReleaseTitle)
	case sonarr.IsSceneTitle(name):
		f.SceneName = name
	case !in.OtherVideoFiles && folder != nil && !folder.FullSeason && strings.TrimSpace(folder.ReleaseTitle) != "" &&
		sonarr.IsSceneTitle(folder.ReleaseTitle):
		f.SceneName = folder.ReleaseTitle
	}
	return f, nil
}

// AggregateMovieFile aggregates a movie release's file (Radarr's MovieImport aggregation).
func AggregateMovieFile(in ImportFile) FileFacts {
	file := radarr.ParseMoviePath(in.Path)
	var folder, client *radarr.ParsedMovieInfo
	if in.Folder != "" {
		folder = radarr.ParseMovieTitle(in.Folder, false)
	}
	if in.Client != "" {
		client = radarr.ParseMovieTitle(in.Client, false)
	}
	quality := func(p *radarr.ParsedMovieInfo) *augmentQuality {
		if p == nil {
			return nil
		}
		return radarrAugment(p.Quality)
	}
	augments := []*augmentQuality{quality(file), quality(folder), quality(client),
		mediaAugment(in.Media, func(t string) (int, int, bool) {
			m := radarr.ParseQualityName(t)
			return int(m.Quality.Source), int(m.Quality.Modifier), m.SourceDetectionSource == radarr.DetectionName
		})}
	if in.Grabbed != "" {
		augments = append(augments, radarrAugment(radarr.ParseQuality(in.Grabbed)))
	}
	source, resolution, modifier, rev := aggregateQuality(augments, int(radarr.SourceUnknown))
	f := FileFacts{Quality: radarr.FindBySourceAndResolution(radarr.QualitySource(source), resolution,
		radarr.Modifier(modifier)).Name, Revision: rev[0], Real: rev[1]}
	// AggregateReleaseGroup and AggregateEdition: the client's, the folder's, the file's.
	for _, p := range []*radarr.ParsedMovieInfo{client, folder, file} {
		if p == nil {
			continue
		}
		if f.ReleaseGroup == "" && strings.TrimSpace(strOr(p.ReleaseGroup)) != "" {
			f.ReleaseGroup = *p.ReleaseGroup
		}
		if f.Edition == "" && strings.TrimSpace(p.Edition) != "" {
			f.Edition = p.Edition
		}
	}
	movieLangs := func(p *radarr.ParsedMovieInfo) []string {
		if p == nil {
			return nil
		}
		var out []string
		for _, l := range p.Languages {
			out = append(out, l.Name)
		}
		return out
	}
	f.Languages = aggregateLanguages(movieLangs(file), movieLangs(folder), movieLangs(client),
		mediaLanguages(in.Media, func(code string) string {
			if l := radarr.FindIsoLanguage(code); l != nil {
				return l.Language.Name
			}
			return ""
		}))
	// SceneNameCalculator.GetSceneName (Radarr's: no full-season case).
	switch name := fileStem(in.Path); {
	case !in.OtherVideoFiles && client != nil:
		f.SceneName = radarr.RemoveFileExtension(client.ReleaseTitle)
	case radarr.IsSceneTitle(name):
		f.SceneName = name
	case !in.OtherVideoFiles && folder != nil && strings.TrimSpace(folder.ReleaseTitle) != "" &&
		radarr.IsSceneTitle(folder.ReleaseTitle):
		f.SceneName = folder.ReleaseTitle
	}
	return f
}

// mediaLanguages is AugmentLanguageFromMediaInfo: the audio streams' languages the ISO table knows.
func mediaLanguages(m *MediaInfo, find func(string) string) []string {
	if m == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, code := range m.AudioLanguages {
		if seen[code] {
			continue
		}
		seen[code] = true
		if name := find(code); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func fileStem(p string) string {
	b := path.Base(p)
	return strings.TrimSuffix(b, path.Ext(b))
}
