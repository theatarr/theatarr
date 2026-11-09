package decide

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/theatarr/theatarr/internal/decide/adapt"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

// FileCutoffNotMet is UpgradableSpecification.CutoffNotMet for a file the version holds: the file's quality is below
// the profile's cutoff, or its custom-format score is below the cutoff score (the minimum score when the profile
// allows no upgrades). The formats are calculated from the file's inputs exactly as an upgrade decision scores the
// existing file (existingFile, seriesFile), so a version is wanted for an upgrade exactly when a release could
// upgrade it. Curator eligibility also uses runtime and episode metadata.
func (e *Engine) FileCutoffNotMet(t *Title, v Version, x *ExistingFile) (bool, error) {
	if v.Policy == nil {
		v.Policy = e.Policy.Versions[v.Key]
	}
	if v.Policy == nil {
		return false, fmt.Errorf("decide: unknown version %q", v.Key)
	}
	fl := flavourOf(v.Policy.Kind)
	p := e.Profile(v.Policy.Profile, fl)
	if p == nil || p.Profile == nil || p.Engine == nil {
		return false, fmt.Errorf("decide: version %q: profile %q was not built", v.Key, v.Policy.Profile)
	}
	p = withVersionRules(p, v.Policy)
	original := languageID(fl, t.OriginalLanguage)
	var f *profile.File
	var err error
	if fl == format.Sonarr {
		f, err = seriesFile(x, t, original, p)
	} else {
		f, err = existingFile(fl, x, t, original, p)
	}
	if err != nil {
		return false, err
	}
	if e.curatedUpgradeAvailable(t, v.Policy, p, x) {
		return true, nil
	}
	return p.Engine.CutoffNotMet(p.Profile, f.Quality, f.Formats, nil), nil
}

// QualityCutoffNotMet is UpgradableSpecification.QualityCutoffNotMet for a file the version holds: its quality is below
// the profile's cutoff (the first allowed quality when the profile allows no upgrades). The format score is not
// looked at: this is the arrs' Cutoff Unmet list (Radarr MovieCutoffService, Sonarr EpisodeCutoffService) and their
// files' qualityCutoffNotMet, not the upgrade rule (FileCutoffNotMet). Optional title metadata enables
// curator-only size eligibility; without it runtime is unknown and only ordinary cutoff applies.
func (e *Engine) QualityCutoffNotMet(v Version, x *ExistingFile, titles ...*Title) (bool, error) {
	if v.Policy == nil {
		v.Policy = e.Policy.Versions[v.Key]
	}
	if v.Policy == nil {
		return false, fmt.Errorf("decide: unknown version %q", v.Key)
	}
	fl := flavourOf(v.Policy.Kind)
	p := e.Profile(v.Policy.Profile, fl)
	if p == nil || p.Profile == nil || p.Engine == nil {
		return false, fmt.Errorf("decide: version %q: profile %q was not built", v.Key, v.Policy.Profile)
	}
	q, ok := profile.QualityByName(fl, x.Quality)
	if !ok {
		return false, fmt.Errorf("decide: file quality %q is not a %s quality", x.Quality, fl)
	}
	var t *Title
	if len(titles) > 0 {
		t = titles[0]
	}
	if e.curatedUpgradeAvailable(t, v.Policy, withVersionRules(p, v.Policy), x) {
		return true, nil
	}
	return p.Engine.QualityCutoffNotMet(p.Profile, profile.QualityModel{Quality: q}, nil), nil
}

// CuratorWanted says whether a curated alternative makes the held file wanted (curatedUpgradeAvailable). A
// series alternative is a season pack, so acquisition searches its season even for one wanted episode.
func (e *Engine) CuratorWanted(t *Title, v Version, x *ExistingFile) bool {
	if v.Policy == nil {
		v.Policy = e.Policy.Versions[v.Key]
	}
	if v.Policy == nil {
		return false
	}
	p := e.Profile(v.Policy.Profile, flavourOf(v.Policy.Kind))
	if p == nil || p.Profile == nil || p.Engine == nil {
		return false
	}
	return e.curatedUpgradeAvailable(t, v.Policy, withVersionRules(p, v.Policy), x)
}

// curatedUpgradeAvailable: a curator search is wanted for a held file only where a better
// curated torrent of the file's own entry and season would pass the decider's checks for that search: an allowed
// quality, the size limits with a known runtime, the format score minimum and language (on the very release the
// search would return, built from the stored facts), upgrade permission, coverage of the file's episodes, and no
// live cache evidence that every provider of the version lacks it. Anything unknown is not curator-wanted; ordinary
// cutoff searches are unchanged.
func (e *Engine) curatedUpgradeAvailable(title *Title, v *policy.VersionPolicy, p *Profile, x *ExistingFile) bool {
	if title == nil || !v.PreferSeaDex || !p.Profile.UpgradeAllowed || v.UpgradeRule() == policy.UpgradesOff {
		return false
	}
	current := fileSeaDexPreference(x, p.Profile.Flavour)
	season := 0 // the season a search for the file asks: its lowest
	for _, ep := range x.seaDexEpisodes {
		if ep[0] > 0 && (season == 0 || ep[0] < season) {
			season = ep[0]
		}
	}
	for _, t := range x.SeaDexAlternatives {
		if t.Preference <= current || t.Quality == "Unknown" || t.Size <= 0 {
			continue
		}
		quality := t.Quality
		if p.Profile.Flavour == format.Radarr && strings.HasPrefix(quality, "Bluray-") && strings.HasSuffix(quality, " Remux") {
			quality = "Remux-" + strings.TrimSuffix(strings.TrimPrefix(quality, "Bluray-"), " Remux")
		}
		for _, q := range p.Profile.AllowedQualities() {
			if q.ID != 0 && q.Name == quality {
				candidate := cutoffAlternativeIn(title, v, p, t, q, season, ResolveFile)
				if !candidate.eligible {
					continue
				}
				if p.Profile.Flavour == format.Sonarr {
					if len(x.seaDexEpisodes) == 0 {
						continue
					}
					covered := true
					for _, ep := range x.seaDexEpisodes {
						covered = covered && candidate.episodes[ep]
					}
					if !covered {
						continue
					}
				}
				if !e.curatedReleaseAllowed(title, v, p, t, candidate) || e.knownUnavailable(v, t) {
					continue
				}
				return true
			}
		}
	}
	return false
}

// curatedReleaseAllowed runs the decider's format and language checks on the release a curator search would return
// (candidate.release): its custom formats calculated as the decider does, against the profile's minimum format score
// and (Radarr) wanted language. A release that does not parse is not allowed.
func (e *Engine) curatedReleaseAllowed(title *Title, v *policy.VersionPolicy, p *Profile, t indexer.SeaDexStanding,
	candidate cutoffCandidate) bool {
	rel := candidate.release
	fl := p.Profile.Flavour
	original := languageID(fl, title.OriginalLanguage)
	flags := indexerFlags(fl, &Release{Release: rel})
	ar := &adapt.Release{Title: rel.Title, Size: rel.Size, IndexerFlags: flags, Languages: languageIDs(fl, rel.Languages)}
	ctx := context.Background()
	var in *format.Input
	var err error
	if fl == format.Sonarr {
		ver, verr := sonarr.ParseVersion(v.Sonarr)
		parsed := ParseSeriesRelease(ctx, ver, rel.Title)
		if verr != nil || parsed == nil {
			return false
		}
		var eps []adapt.Episode
		for i := range title.Episodes {
			if ep := &title.Episodes[i]; candidate.episodes[[2]int{ep.Season, ep.Number}] {
				eps = append(eps, adapt.Episode{Title: ep.Title})
			}
		}
		in, err = adapt.SonarrRelease(parsed, &adapt.Series{Year: title.Year, OriginalLanguage: original}, eps, ar)
	} else {
		parsed := ParseMovieRelease(ctx, rel.Title)
		if parsed == nil {
			return false
		}
		in, err = adapt.RadarrRelease(parsed, &adapt.Movie{Title: title.Name, Year: title.Year, OriginalLanguage: original}, ar)
	}
	if err != nil {
		return false
	}
	in.SeaDex = SeaDexRank(&rel)
	matched, _ := format.Calculate(p.Formats, in)
	subject := &profile.Subject{Profile: p.Profile, Media: &profile.Media{Title: title.Name, OriginalLanguage: original},
		Release: &profile.Release{Title: rel.Title, Size: rel.Size, Languages: in.Languages, Formats: matched}}
	subject.Score()
	for _, name := range []string{"CustomFormatAllowedbyProfile", "Language"} {
		if sp, ok := p.Engine.Spec(name); ok && !p.Engine.Run(sp, subject).Accepted {
			return false
		}
	}
	return true
}

// knownUnavailable says the operation's live cache evidence (provider_cache, read with the standing) shows that no
// provider of a debrid version could supply the torrent: a miss on every one, or uncached on every one while the
// router adds cached torrents only. No evidence is not unavailability.
func (e *Engine) knownUnavailable(v *policy.VersionPolicy, t indexer.SeaDexStanding) bool {
	if v.Storage != policy.StorageDebrid || len(v.Providers) == 0 || len(t.Cache) == 0 {
		return false
	}
	cachedOnly := e.Policy.RouterSettings().CachedOnly
	for _, pk := range v.Providers {
		if st := t.Cache[pk]; st != "miss" && !(st == "uncached" && cachedOnly) {
			return false
		}
	}
	return true
}

// cutoffCache keeps both successful and unknown alternatives of one loaded title, so every feature is resolved at
// most once per pass. LoadSeriesVersion gives each load its own (a reload starts empty; value copies of the title
// share it); it has its own lock, so no title field is written during a pass.
type cutoffCache struct {
	mu sync.Mutex
	m  map[cutoffKey]cutoffCandidate
}

// cutoffInit only allocates the cache of a title not loaded by LoadSeriesVersion; it is never held during a pass.
var cutoffInit sync.Mutex

type cutoffKey struct {
	profile  *profile.Profile
	engine   *profile.Engine
	mapping  *animemap.Series
	episodes *Episode
	name     string
	runtime  int
	version  *policy.VersionPolicy
	facts    any
	hash     string
	entry    int
	size     int64
	quality  string
	season   int
}
type cutoffCandidate struct {
	eligible bool
	episodes map[[2]int]bool
	release  indexer.Release // what a curator search would return for it (indexer.SeaDexCandidateRelease)
}

// cutoffAlternative is cutoffAlternativeIn for the entry's own lowest regular season.
func cutoffAlternative(title *Title, v *policy.VersionPolicy, p *Profile, t indexer.SeaDexStanding, q profile.Quality,
	resolve func(*Title, string, FileInput) (FileEpisodes, error)) cutoffCandidate {
	return cutoffAlternativeIn(title, v, p, t, q, 0, resolve)
}

// cutoffAlternativeIn checks one curated torrent's size and (series) episode coverage on the release a search for
// season would return for it: the same synthetic title (indexer.SeaDexCandidateRelease) for the curator search, the
// filename mapping and the format checks. Season 0 is the entry's lowest regular season.
func cutoffAlternativeIn(title *Title, v *policy.VersionPolicy, p *Profile, t indexer.SeaDexStanding, q profile.Quality,
	season int, resolve func(*Title, string, FileInput) (FileEpisodes, error)) (result cutoffCandidate) {
	if p.Profile.Flavour == format.Sonarr {
		cutoffInit.Lock()
		if title.cutoff == nil {
			title.cutoff = &cutoffCache{} // a title not loaded by LoadSeriesVersion
		}
		c := title.cutoff
		cutoffInit.Unlock()
		c.mu.Lock()
		defer c.mu.Unlock()
		engine := p.Engine
		if p.baseEngine != nil {
			engine = p.baseEngine
		}
		var episodes *Episode
		if len(title.Episodes) > 0 {
			episodes = &title.Episodes[0]
		}
		key := cutoffKey{profile: p.Profile, engine: engine, mapping: title.AnimeMap, episodes: episodes,
			name: title.Name, runtime: title.Runtime, version: v, facts: t.Facts, hash: t.InfoHash, entry: t.Entry, size: t.Size, quality: q.Name,
			season: season}
		if cached, ok := c.m[key]; ok {
			return cached
		}
		if c.m == nil {
			c.m = map[cutoffKey]cutoffCandidate{}
		}
		defer func() { c.m[key] = result }()
	}
	subject := &profile.Subject{Profile: p.Profile, Media: &profile.Media{Title: title.Name, Runtime: title.Runtime, Pilot: pilot(title)},
		Release: &profile.Release{Size: t.Size, Quality: profile.QualityModel{Quality: q}}}
	typ, ok := indexer.QueryMovie, false
	if p.Profile.Flavour == format.Sonarr {
		typ = indexer.QuerySeries
		names := t.FeatureNames()
		if len(names) == 0 {
			return
		}
		ids := map[int64]bool{}
		var mapping *EpisodeMap
		// SeaDex's synthetic release uses the catalog title and the searched season (the entry's lowest regular
		// season when none is given). Pass that title and the unique entry mapping just as import does, so alternate
		// cuts win over generic filenames.
		if en, mapped := seaDexEntry(title.AnimeMap, t.Entry); mapped {
			if en == nil || en.Key == "" {
				return
			}
			mapping = &EpisodeMap{How: AnimeMapFranchise, Entry: en.Key}
			if season == 0 {
				for _, se := range en.Regular() {
					if season == 0 || se[0] < season {
						season = se[0]
					}
				}
			}
		}
		if result.release, ok = indexer.SeaDexCandidateRelease(t, typ, title.Name, season, title.Year); !ok {
			return
		}
		release := result.release.Title
		for _, name := range names {
			mapped, err := resolve(title, v.Sonarr, FileInput{Path: name, Release: release, Mapping: mapping, OtherVideoFiles: len(names) > 1})
			if err != nil || len(mapped.EpisodeIDs) == 0 {
				return
			}
			for _, id := range mapped.EpisodeIDs {
				ids[id] = true
			}
		}
		result.episodes = map[[2]int]bool{}
		for i := range title.Episodes {
			ep := &title.Episodes[i]
			if ids[ep.ID] {
				if ep.Runtime <= 0 && title.Runtime <= 0 {
					return
				}
				subject.Episodes = append(subject.Episodes, profileEpisode(ep))
				result.episodes[[2]int{ep.Season, ep.Number}] = true
			}
		}
		if len(subject.Episodes) == 0 {
			return
		}
	} else if title.Runtime <= 0 {
		return
	} else if result.release, ok = indexer.SeaDexCandidateRelease(t, typ, title.Name, 0, title.Year); !ok {
		return
	}
	size, _ := p.Engine.Spec("AcceptableSize")
	if !p.Engine.Run(size, subject).Accepted {
		return
	}
	if maximum, ok := p.Engine.Spec("MaximumSize"); ok && !p.Engine.Run(maximum, subject).Accepted {
		return
	}
	result.eligible = true
	return
}
