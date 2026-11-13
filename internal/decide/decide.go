package decide

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/adapt"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

// Release is the release under decision: the indexer result, plus what the arr's ReleaseInfo carries that
// indexer.Release does not.
type Release struct {
	indexer.Release
	// Usenet marks a usenet release (history parity: arrs may have grabbed from usenet too). Default torrent.
	Usenet bool
	// IndexerFlags, when set, are the arr's IndexerFlags bits as recorded (history); otherwise they are computed from
	// the torznab tags and volume factors in Flags and Extras.
	IndexerFlags *int
	// IndexerPriority is the arr's indexer priority (1 best, 25 the arr default). 0 counts as 25.
	IndexerPriority int
	Manual          *ManualOverrides
}

// Title is the title a release is decided for: what the arr's Movie (or Series) supplies.
type Title struct {
	cutoff        *cutoffCache // the loaded title's curator cutoff results (LoadSeriesVersion)
	ID            int64
	Kind          policy.Kind
	Name          string
	Year          int
	SecondaryYear int
	// OriginalTitle and Aliases (alternative titles, translations) are matched like the arr's clean titles.
	OriginalTitle string
	Aliases       []string
	IMDb          string // tt…
	TMDb          int
	// Runtime in minutes, 0 unknown (movie size checks wait for metadata).
	Runtime int
	// OriginalLanguage by arr name ("English") or ISO 639-1 code ("en"); empty is Unknown.
	OriginalLanguage string
	// OriginalLanguageResolved is set when a series had no stored original language and OriginalLanguage was
	// resolved at load (metadata, else English): its held files with no known language then take it, as an untagged
	// release does.
	OriginalLanguageResolved bool
	Anime                    bool

	// Series (Sonarr's Series): the TVDB id, the series type (standard, daily, anime; "" is standard), whether the
	// series has scene numbering, whether it is monitored, its scene mappings, and its episodes with the version's
	// file of each. Runtime above is the series runtime.
	TVDB              int
	SeriesType        string
	UseSceneNumbering bool
	Monitored         bool
	SceneMappings     []SceneMapping
	Episodes          []Episode
	// AnimeMap is the franchise mappings into the series (anime.go); nil: Sonarr's mapping only.
	AnimeMap *animemap.Series
}

// Episode is an episode of a series in TVDB numbering, with its numbers in the other schemes the arr has.
type Episode struct {
	LastGrab *profile.ImportedGrab `json:"-"`
	ID       int64
	Season   int
	Number   int
	Absolute int // 0 none
	// Scene numbers (TheXEM through the arr), nil when the episode has none.
	SceneSeason   *int
	SceneNumber   *int
	SceneAbsolute *int
	Title         string
	AirDate       string // YYYY-MM-DD, "" unknown
	AirDateUTC    *time.Time
	Runtime       int
	Monitored     bool
	// File is the version's file of the episode (nil: none); FileID groups the episodes of one file.
	File   *ExistingFile
	FileID int64
}

// SceneMapping is one of the arr's scene mappings of a series (Sonarr's alternateTitles).
type SceneMapping struct {
	Title             string
	SeasonNumber      *int
	SceneSeasonNumber *int
	SceneOrigin       string
	Comment           string
}

// Version is the version a release is decided for.
type Version struct {
	ID     int64
	Key    string
	Policy *policy.VersionPolicy
	// Naming is the stored arr naming configuration, when naming.from_arr is used.
	Naming *naming.Config
}

// ExistingFile is the version's current file, from an active link or an arr file.
type ExistingFile struct {
	SeaDexAlternatives []indexer.SeaDexStanding `json:"-"`
	LastGrab           *profile.ImportedGrab    `json:"-"`
	// seaDexEpisodes are a series file's (season, number) episodes, which scope its curator alternatives.
	seaDexEpisodes   [][2]int
	seaDexNoIdentity bool // incoming release has no identity or a conflicting one; never persisted for held files
	// Formats and Score retain the import decision for links; comparisons re-score from the saved file inputs.
	Formats  []FormatScore `json:",omitempty"`
	Score    *int          `json:",omitempty"`
	Quality  string
	Revision int
	Real     int
	IsRepack bool `json:",omitempty"`
	// The release title is taken as the arr does: SceneName, else the file name of OriginalFilePath, else of
	// RelativePath.
	SceneName        string
	OriginalFilePath string
	RelativePath     string
	ReleaseGroup     string
	Edition          string
	Languages        []string
	Size             int64
	IndexerFlags     int
	// InfoHash is the file's torrent (the registry content), lower-case hex, "" when unknown. A file from the same
	// torrent as the release takes the release's SeaDex standing.
	InfoHash string
	// SeaDex is the file's SeaDex standing when the caller knows it (ours: SeaDexSpecification).
	SeaDex format.SeaDexRank
	// SeaDexPreference preserves the configured curator preference separately from actual quality.
	SeaDexPreference int `json:",omitempty"`
	SeaDexEntry      int `json:",omitempty"`
	// Series files: Sonarr's release type of the file (singleEpisode, multiEpisode, seasonPack) and the episodes it
	// holds.
	ReleaseType string  `json:",omitempty"`
	EpisodeIDs  []int64 `json:",omitempty"`
	// Added is when the arr added the file (zero: unknown); callers use it to tell the file a grab saw.
	Added time.Time `json:",omitzero"`
	// ReleaseHash is the file's own release hash (Sonarr's EpisodeFile.ReleaseHash: an anime file's [CRC32]).
	ReleaseHash string `json:",omitempty"`
}

// Rejection is one reason a release is refused. Reason is the arr's DownloadRejectionReason name, or ours for the
// rules the arrs do not have (Ours is set then).
type Rejection struct {
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Spec      string `json:"spec,omitempty"`
	Temporary bool   `json:"temporary,omitempty"`
	Ours      bool   `json:"ours,omitempty"`
}

func (r Rejection) String() string { return "[" + r.Reason + "] " + r.Message }

// FormatScore is one matched format and its score in the profile (0 when the profile does not score it).
type FormatScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Existing summarises the version's current file as the decision saw it.
type Existing struct {
	Quality string `json:"quality"`
	// ProfileRank is the file quality's rank in the profile, as for Decision.ProfileRank.
	ProfileRank int           `json:"profile_rank"`
	Formats     []FormatScore `json:"formats"`
	Score       int           `json:"score"`
}

// Decision is the outcome for one release and one version.
type Decision struct {
	Release   string `json:"release"`
	Indexer   string `json:"indexer,omitempty"`
	TitleID   int64  `json:"title_id,omitempty"`
	Version   string `json:"version"`
	VersionID int64  `json:"version_id,omitempty"`
	Profile   string `json:"profile"`

	Accepted   bool        `json:"accepted"`
	Rejections []Rejection `json:"rejections,omitempty"`

	// Parse and mapping.
	Parsed       bool      `json:"parsed"`
	MovieTitles  []string  `json:"movie_titles,omitempty"`
	Year         int       `json:"year,omitempty"`
	ReleaseGroup string    `json:"release_group,omitempty"`
	Edition      string    `json:"edition,omitempty"`
	MatchType    MatchType `json:"match,omitempty"`
	Languages    []string  `json:"languages,omitempty"`
	// LanguagesAssumed: neither the name nor the indexer gave a language, so Languages is the title's original
	// language (the arrs' fallback).
	LanguagesAssumed bool `json:"languages_assumed,omitempty"`
	// PreferSeaDex is the version's prefer_seadex: SeaDex standing ranks before quality.
	PreferSeaDex bool `json:"prefer_seadex,omitempty"`

	Quality  string           `json:"quality"`
	Revision profile.Revision `json:"revision"`
	// ProfileRank is the quality's place among the profile's allowed qualities, best first (1 is the best; the
	// qualities of one group share a rank, as the comparer ties them), 0 when the profile does not allow it.
	// ProfileRanks is the number of ranks. (QualityRank() counts every item from the lowest, for the router.)
	ProfileRank  int           `json:"profile_rank"`
	ProfileRanks int           `json:"profile_ranks"`
	Formats      []FormatScore `json:"formats"`
	Score        int           `json:"score"`

	// Series: the parsed series title, season and episode numbers, release type, and the episodes the release
	// maps to (as S01E02), of which Missing have no file in the version.
	SeriesTitle string   `json:"series_title,omitempty"`
	Season      *int     `json:"season,omitempty"`
	Numbers     []int    `json:"episode_numbers,omitempty"`
	Absolute    []int    `json:"absolute_numbers,omitempty"`
	ReleaseType string   `json:"release_type,omitempty"`
	Episodes    []string `json:"episodes,omitempty"`
	EpisodeIDs  []int64  `json:"episode_ids,omitempty"`
	Missing     int      `json:"missing,omitempty"`
	// Anime (franchise mappings, anime.go): how the episodes were mapped (franchise, alt_cut:<name>, sonarr), the
	// franchise entry, and the episodes that used Sonarr's numbering because their franchise mapping is not
	// importable (conflict, rejected).
	AnimeMapping  string          `json:"anime_mapping,omitempty"`
	AnimeEntry    string          `json:"anime_entry,omitempty"`
	AnimeFallback []AnimeFallback `json:"anime_fallback,omitempty"`

	Existing *Existing `json:"existing,omitempty"`
	// Upgrade is whether an accepted release replaces the existing file; UpgradeReason says why or why not.
	Upgrade       bool   `json:"upgrade"`
	UpgradeReason string `json:"upgrade_reason,omitempty"`

	subject *profile.Subject
	engine  *profile.Engine
}

// Decide decides one release for one version of a title, against the version's profile and its existing file
// (nil when it has none). It is pure: no network, no database, the same result for the same inputs (and clock,
// which only release ages and air dates read). The error is for inputs it cannot judge (unknown version profile).
//
// For a series version, existing is not read: each episode's file is on t.Episodes. Decide runs the RSS path (no
// search criteria); DecideSearch passes the search.
func (e *Engine) Decide(ctx context.Context, rel Release, t Title, v Version, existing *ExistingFile) (*Decision, error) {
	return e.decide(ctx, rel, t, v, existing, nil)
}

// DecideSearch decides a release found by a search for some of a series' episodes (Sonarr's GetSearchDecision with
// its SearchCriteriaBase). A nil search is the RSS path, as Decide.
func (e *Engine) DecideSearch(ctx context.Context, rel Release, t Title, v Version, search *SeriesSearch) (*Decision, error) {
	return e.decide(ctx, rel, t, v, nil, search)
}

func (e *Engine) decide(ctx context.Context, rel Release, t Title, v Version, existing *ExistingFile,
	search *SeriesSearch) (*Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.Policy == nil {
		v.Policy = e.Policy.Versions[v.Key]
	}
	if v.Policy == nil {
		return nil, fmt.Errorf("decide: unknown version %q", v.Key)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, e.Policy, v.Policy), t.ID)
	if err := SeaDexError(ctx); err != nil {
		return nil, fmt.Errorf("decide: SeaDex standing: %w", err)
	}
	if v.Policy.PreferSeaDex {
		rel.Release = RefreshSeaDexRelease(ctx, rel.Release)
	}
	fl := flavourOf(v.Policy.Kind)
	p := e.Profile(v.Policy.Profile, fl)
	if p == nil {
		return nil, fmt.Errorf("decide: version %q: profile %q was not built", v.Key, v.Policy.Profile)
	}
	p = withVersionRules(p, v.Policy)
	d := &Decision{Release: rel.Title, Indexer: rel.Indexer, TitleID: t.ID, Version: v.Key, VersionID: v.ID,
		Profile: p.Key, PreferSeaDex: v.Policy.PreferSeaDex, engine: p.Engine}
	if fl == format.Sonarr {
		if err := e.decideSeries(ctx, d, p, rel, &t, v, search); err != nil {
			return nil, err
		}
	} else if err := e.decideMovie(ctx, d, p, rel, &t, v, existing); err != nil {
		return nil, err
	}
	d.ProfileRank, d.ProfileRanks = profileRank(p.Profile, fl, d.Quality)
	if d.Existing != nil {
		d.Existing.ProfileRank, _ = profileRank(p.Profile, fl, d.Existing.Quality)
	}
	// Prefer-on only: an unidentified native quality waits for evidence (a prefer-off version rejects Unknown as
	// any other release, permanently, as before).
	if d.Quality == "Unknown" && rel.Kind == "seadex" && v.Policy.PreferSeaDex {
		for i := range d.Rejections {
			if d.Rejections[i].Reason == string(profile.ReasonQualityNotWanted) {
				d.Rejections[i].Reason = "SeaDexQualityPending"
				d.Rejections[i].Temporary = true
			}
		}
	}
	// An unidentified native quality is held even where the profile allows Unknown.
	if d.Quality == "Unknown" && rel.Kind == "seadex" && v.Policy.PreferSeaDex {
		found := false
		for _, r := range d.Rejections {
			found = found || r.Reason == "SeaDexQualityPending"
		}
		if !found {
			d.Rejections = append(d.Rejections, Rejection{Reason: "SeaDexQualityPending", Temporary: true, Message: "Waiting for native SeaDex quality evidence"})
		}
	}
	d.Accepted = len(d.Rejections) == 0
	return d, nil
}

// profileRank is the named quality's rank among the profile's allowed items, best first (0 when not allowed), and
// the number of allowed items.
func profileRank(pr *profile.Profile, fl format.Flavour, name string) (rank, of int) {
	at := -1
	if q, ok := profile.QualityByName(fl, name); ok && len(pr.Items) > 0 {
		if i := pr.GetIndex(q.ID, false).Index; pr.Items[i].Allowed && slices.ContainsFunc(pr.Items[i].Qualities(),
			func(x profile.Quality) bool { return x.ID == q.ID }) {
			at = i
		}
	}
	for i := len(pr.Items) - 1; i >= 0; i-- {
		if pr.Items[i].Allowed {
			of++
			if i == at {
				rank = of
			}
		}
	}
	return rank, of
}

// withVersionRules applies the version's pack rule (every series version takes a pack that upgrades or fills any
// episode) and curator preference to a copy of the shared profile engine.
func withVersionRules(p *Profile, v *policy.VersionPolicy) *Profile {
	anyRule := v.Kind == policy.KindSeries
	if anyRule == p.Engine.Config.AcceptReleaseAnyUpgradable && v.PreferSeaDex == p.Engine.Config.PreferSeaDex {
		return p
	}
	eng := *p.Engine
	eng.Config.AcceptReleaseAnyUpgradable = anyRule
	eng.Config.PreferSeaDex = v.PreferSeaDex
	q := *p
	q.baseEngine = p.Engine
	q.Engine = &eng
	return &q
}

// Library, when set, lets indexer ids map to other titles of ours (see Library).
func (e *Engine) SetLibrary(l Library) { e.library = l }

// Library returns the shared title lookup currently used by the engine.
func (e *Engine) Library() Library { return e.library }

func (e *Engine) decideMovie(ctx context.Context, d *Decision, p *Profile, rel Release, t *Title, v Version, existing *ExistingFile) error {
	fl := format.Radarr
	parsed := ParseMovieRelease(ctx, rel.Title)
	if rel.Manual != nil && parsed == nil {
		parsed = ParseMovieRelease(ctx, fmt.Sprintf("%s.%d", t.Name, t.Year))
	}
	if parsed == nil || parsed.PrimaryMovieTitle() == nil || strings.TrimSpace(*parsed.PrimaryMovieTitle()) == "" {
		// DownloadDecisionMaker with search criteria: the quality is still parsed for display.
		q := radarr.ParseQuality(rel.Title)
		d.Quality, d.Revision = q.Quality.Name, profile.Revision{Version: q.Revision.Version, Real: q.Revision.Real}
		d.Rejections = append(d.Rejections, Rejection{Reason: "UnableToParse", Message: "Unable to parse release"})
		d.Rejections = append(d.Rejections, e.ourRules(rel, v)...)
		return nil
	}
	if rel.Manual != nil && rel.Manual.ReleaseGroup != nil {
		parsed.ReleaseGroup = rel.Manual.ReleaseGroup
	}
	d.Parsed = true
	d.MovieTitles, d.Year, d.Edition = parsed.MovieTitles, parsed.Year, parsed.Edition
	if parsed.ReleaseGroup != nil {
		d.ReleaseGroup = *parsed.ReleaseGroup
	}
	q, ok := profile.QualityByID(fl, parsed.Quality.Quality.ID)
	if !ok {
		return fmt.Errorf("decide: parsed quality %d is not a radarr quality", parsed.Quality.Quality.ID)
	}
	qm := profile.QualityModel{Quality: q, Revision: profile.Revision{Version: parsed.Quality.Revision.Version,
		Real: parsed.Quality.Revision.Real, IsRepack: parsed.Quality.Revision.IsRepack}}
	qm = rel.Manual.quality(fl, qm)
	qm.SeaDexPreference = indexer.SeaDexPreference(&rel.Release)
	d.Quality, d.Revision = qm.Quality.Name, qm.Revision

	m := findMovie(parsed, releaseIMDb(rel.IMDb), rel.TMDb, t, e.library)
	if rel.Manual != nil {
		m = &movieMatch{t, MatchTitle}
	}
	if m == nil {
		d.Rejections = append(d.Rejections, Rejection{Reason: "UnknownMovie",
			Message: "Unknown Movie. Unable to match to correct movie using release title."})
		d.Rejections = append(d.Rejections, e.ourRules(rel, v)...)
		return nil
	}
	d.MatchType = m.typ

	original := languageID(fl, t.OriginalLanguage)
	flags := indexerFlags(fl, &rel)
	ar := &adapt.Release{Title: rel.Title, Size: rel.Size, IndexerFlags: flags,
		Languages:      languageIDs(fl, rel.Languages),
		MultiLanguages: languageIDs(fl, indexerMulti(e.Policy, rel.Indexer))}
	// IndexerBase.CleanupReleases: a MULTi release the indexer gave no languages for gets the Multi Languages.
	if len(ar.Languages) == 0 && len(ar.MultiLanguages) > 0 && radarr.HasMultipleLanguages(rel.Title) {
		ar.Languages = append([]int{}, ar.MultiLanguages...)
	}
	in, err := adapt.RadarrRelease(parsed, &adapt.Movie{Title: t.Name, Year: t.Year, OriginalLanguage: original}, ar)
	if err != nil {
		return err
	}
	rel.Manual.input(in, qm)
	in.SeaDex = SeaDexRank(&rel.Release)
	// The format input reads the mapped movie; a release mapped to another title is scored as the arr would only
	// against that title's profile, which is not ours to judge: it is rejected below as the wrong movie.
	matched, calcErr := format.Calculate(p.Formats, in)
	_ = calcErr // a regex timeout counts as not satisfied; the arr has no timeout
	d.Formats = formatScores(p.Profile, matched)
	d.Score = p.Profile.FormatScore(matched)
	for _, l := range in.Languages {
		d.Languages = append(d.Languages, fl.LanguageName(l))
	}
	d.LanguagesAssumed = in.LanguagesAssumed

	media := &profile.Media{Title: t.Name, Runtime: t.Runtime, OriginalLanguage: original, Anime: t.Anime}
	var file *profile.File
	if existing != nil {
		x := resolveSeaDex(ctx, existing)
		if x.SeaDex == format.SeaDexNone && x.InfoHash != "" && x.InfoHash == rel.InfoHash {
			x.SeaDex = in.SeaDex
		}
		if SameTorrent(&x, &ExistingFile{InfoHash: rel.InfoHash, SceneName: rel.Title}) {
			x.SeaDexPreference = qm.SeaDexPreference
		}
		file, err = existingFile(fl, &x, t, original, p)
		if err != nil {
			return err
		}
		media.MovieFile = file
		d.Existing = &Existing{Quality: file.Quality.Quality.Name, Formats: formatScores(p.Profile, file.Formats),
			Score: p.Profile.FormatScore(file.Formats)}
	}
	prio := rel.IndexerPriority
	if prio == 0 {
		prio = 25
	}
	r := &profile.Release{Title: rel.Title, Size: rel.Size, Quality: qm, Languages: in.Languages, Formats: matched,
		IndexerPriority: prio, IndexerFlags: flags, PublishDate: rel.PublishDate, Protocol: profile.ProtocolTorrent,
		Indexer: rel.Indexer, MinimumSeeders: e.minimumSeeders(rel.Indexer), InfoHash: rel.InfoHash}
	if parsed.ReleaseGroup != nil {
		r.ReleaseGroup = *parsed.ReleaseGroup
	}
	if rel.Usenet {
		r.Protocol = profile.ProtocolUsenet
	} else {
		if rel.Seeders >= 0 {
			s := rel.Seeders
			r.Seeders = &s
		}
		if rel.Peers >= 0 {
			pe := rel.Peers
			r.Peers = &pe
		}
	}
	s := &profile.Subject{Profile: p.Profile, Media: media, Release: r, Cached: e.cacheStanding(v.Policy, &rel.Release)}
	if ctx.Value(searchContextKey{}) != nil {
		s.Search = &profile.Search{}
	}
	s.Score()
	d.subject = s

	def := p.Engine.Definitions.Get(qm.Quality)
	// Prefer-on only: an unknown runtime holds size-limited releases instead of Radarr's 110-minute assumption.
	unknownRuntime := v.Policy.PreferSeaDex && t.Runtime <= 0 && rel.Size > 0 && (def.MinSize != nil && *def.MinSize > 0 || def.MaxSize != nil && *def.MaxSize > 0)
	var group0, disk []Rejection
	for _, rj := range p.Engine.Evaluate(s) {
		x := Rejection{Reason: string(rj.Reason), Message: rj.Message, Spec: rj.Spec, Temporary: rj.Type == profile.Temporary}
		if unknownRuntime && rj.Spec == "AcceptableSizeSpecification" {
			continue
		}
		if sp, ok := p.Engine.Spec(rj.Spec); ok && sp.Priority == profile.PriorityDisk {
			disk = append(disk, x)
		} else {
			group0 = append(group0, x)
		}
	}
	// Movie (Search/MovieSpecification) and our own rules (listed first) are default priority: they join the first group, and a
	// rejection there means the disk group never ran.
	if m.title != t && (t.ID == 0 || m.title.ID != t.ID) {
		group0 = append([]Rejection{{Reason: "WrongMovie", Message: "Wrong movie", Spec: "MovieSpecification"}}, group0...)
	}
	group0 = append(e.ourRules(rel, v), group0...)
	if len(group0) > 0 {
		d.Rejections = group0
	} else {
		d.Rejections = disk
	}

	if unknownRuntime {
		d.Rejections = append(d.Rejections, Rejection{Reason: "UnknownRuntime", Message: "Movie runtime is unknown; waiting for metadata before checking release size", Spec: "MovieRuntimeReady", Temporary: true})
	}
	if file != nil {
		d.Upgrade, d.UpgradeReason = upgradeOf(p, file, qm, matched, d.Rejections)
		if len(d.Rejections) > 0 && d.Upgrade {
			d.Upgrade, d.UpgradeReason = false, "the release is rejected; it would upgrade the file ("+d.UpgradeReason+")"
		}
	}
	return nil
}

func indexerMulti(pol *policy.Policy, key string) []string {
	if ix := pol.Indexers[key]; ix != nil {
		return ix.MultiLanguages
	}
	return nil
}

// ourRules are the checks the arrs do not have: the version's own indexer list. Which indexers feed a version is
// the user's choice; an indexer's privacy is a label only. A release a person added by hand (policy.ManualIndexer)
// came from no indexer and is not held to the list.
func (e *Engine) ourRules(rel Release, v Version) []Rejection {
	var out []Rejection
	if rel.Indexer != "" && !policy.IsManualAdd(rel.Indexer) && len(v.Policy.Indexers) > 0 && !slices.Contains(v.Policy.Indexers, rel.Indexer) {
		out = append(out, Rejection{Reason: "IndexerNotInVersion", Ours: true, Message: fmt.Sprintf(
			"Indexer %s is not one of version %s's indexers (%s)", rel.Indexer, v.Key, strings.Join(v.Policy.Indexers, ", "))})
	}
	return out
}

// upgradeOf says whether the release upgrades the existing file and why: the upgrade specifications' rejection when
// one refused it, else UpgradableSpecification's view.
func upgradeOf(p *Profile, file *profile.File, qm profile.QualityModel, formats []*format.Format, rejections []Rejection) (bool, string) {
	for _, r := range rejections {
		if r.Spec == "UpgradeDiskSpecification" || r.Spec == "UpgradeAllowedSpecification" {
			return false, r.Message
		}
	}
	e, pr := p.Engine, p.Profile
	if reason := e.IsUpgradable(pr, file.Quality, file.Formats, qm, formats); reason != profile.UpgradeNone {
		return false, "not an upgrade: " + reason.String()
	}
	cur, nw := pr.FormatScore(file.Formats), pr.FormatScore(formats)
	preferred, _ := profile.SeaDexOrder(e.Config.PreferSeaDex, qm.SeaDexPreference, file.Quality.SeaDexPreference)
	switch {
	case preferred > 0:
		return true, fmt.Sprintf("preferred SeaDex release; actual quality %s over %s", qm.Quality.Name, file.Quality.Quality.Name)
	case pr.CompareQualities(qm.Quality, file.Quality.Quality, false) > 0:
		return true, fmt.Sprintf("better quality: %s over %s", qm.Quality.Name, file.Quality.Quality.Name)
	case qm.Revision.CompareTo(file.Quality.Revision) > 0:
		return true, fmt.Sprintf("better revision: %s over %s", qm.Revision, file.Quality.Revision)
	default:
		return true, fmt.Sprintf("better format score: %d over %d", nw, cur)
	}
}

func formatScores(p *profile.Profile, matched []*format.Format) []FormatScore {
	out := make([]FormatScore, 0, len(matched))
	for _, f := range matched {
		s := 0
		for _, it := range p.FormatItems {
			if it.Format == f.ID {
				s += it.Score
			}
		}
		out = append(out, FormatScore{Name: f.Name, Score: s})
	}
	return out
}

// existingFile is the version's current file with its formats, as CustomFormatCalculationService.ParseCustomFormat
// (MovieFile, Movie) computes them.
func existingFile(fl format.Flavour, x *ExistingFile, t *Title, original int, p *Profile) (*profile.File, error) {
	q, ok := profile.QualityByName(fl, x.Quality)
	if !ok {
		return nil, fmt.Errorf("decide: existing file quality %q is not a %s quality", x.Quality, fl)
	}
	rev := profile.Revision{Version: x.Revision, Real: x.Real, IsRepack: x.IsRepack}
	if rev.Version == 0 {
		rev.Version = 1
	}
	title := ""
	switch {
	case strings.TrimSpace(x.SceneName) != "":
		title = x.SceneName
	case strings.TrimSpace(x.OriginalFilePath) != "":
		title = path.Base(x.OriginalFilePath)
	case strings.TrimSpace(x.RelativePath) != "":
		title = path.Base(x.RelativePath)
	}
	in := &format.Input{Flavour: fl, ReleaseTitle: format.Str(radarr.SimplifyReleaseTitle(title)), Source: q.Source,
		Resolution: q.Resolution, Modifier: q.Modifier, Languages: languageIDs(fl, x.Languages), Size: x.Size,
		IndexerFlags: x.IndexerFlags, Year: t.Year, Media: &format.Media{Year: t.Year, OriginalLanguage: original},
		SeaDex: x.SeaDex}
	if in.Languages == nil {
		in.Languages = []int{}
	}
	if x.RelativePath != "" {
		in.Filename = format.Str(path.Base(x.RelativePath))
	}
	if x.ReleaseGroup != "" {
		in.ReleaseGroup = format.Str(x.ReleaseGroup)
	}
	if x.Edition != "" {
		in.Edition = format.Str(x.Edition)
	}
	formats, _ := format.Calculate(p.Formats, in)
	f := &profile.File{Quality: profile.QualityModel{Quality: q, Revision: rev, SeaDexPreference: fileSeaDexPreference(x, fl)}, Formats: formats,
		Added: x.Added, LastGrab: x.LastGrab}
	if x.ReleaseGroup != "" {
		f.ReleaseGroup = x.ReleaseGroup
	}
	return f, nil
}

// languageID resolves a language by arr name, ISO 639 code or ISO English name; Unknown when none fits.
func languageID(fl format.Flavour, s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return format.LanguageUnknown
	}
	if id, ok := fl.LanguageID(s); ok {
		return id
	}
	if iso := radarr.FindIsoLanguage(s); iso != nil && fl == format.Radarr {
		return iso.Language.ID
	}
	if iso := radarr.FindIsoLanguageByName(s); iso != nil && fl == format.Radarr {
		return iso.Language.ID
	}
	return format.LanguageUnknown
}

// languageIDs resolves names as IsoLanguages.FindByName does for torznab attributes: unknown names are dropped.
func languageIDs(fl format.Flavour, names []string) []int {
	var out []int
	for _, n := range names {
		if id := languageID(fl, n); id != format.LanguageUnknown {
			out = append(out, id)
		}
	}
	return out
}

// SeaDexRank is a release's SeaDex standing from its flags: seadex-best is best, seadex alone an alternative. The
// seadex indexer sets them; indexer.MarkSeaDex copies them to the same torrent found elsewhere.
func SeaDexRank(r *indexer.Release) format.SeaDexRank {
	rank := format.SeaDexNone
	for _, f := range r.Flags {
		switch f {
		case indexer.FlagSeaDexBest:
			return format.SeaDexBest
		case indexer.FlagSeaDex:
			rank = format.SeaDexAlt
		}
	}
	return rank
}

func fileSeaDexPreference(x *ExistingFile, fl format.Flavour) int {
	if x.SeaDexPreference > 0 {
		return x.SeaDexPreference
	}
	if x.SeaDex != format.SeaDexNone {
		return int(x.SeaDex)
	}

	return 0
}

// indexerFlags is TorznabRssParser.GetFlags over our release: volume factors from Extras, internal and scene from
// the tags. A recorded value wins.
func indexerFlags(fl format.Flavour, r *Release) int {
	if r.Manual != nil && r.Manual.IndexerFlags != nil {
		return *r.Manual.IndexerFlags
	}
	if r.IndexerFlags != nil {
		return *r.IndexerFlags
	}
	val := func(name string) int {
		if fl == format.Sonarr { // Sonarr's IndexerFlags have no G_ prefix
			name = strings.TrimPrefix(name, "G_")
		}
		v, _ := fl.IndexerFlagsValue(name)
		return v
	}
	flags := 0
	factor := func(k string) float64 {
		if s, ok := r.Extras[k]; ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 32); err == nil {
				return float64(float32(f))
			}
		}
		return 1
	}
	switch factor("downloadvolumefactor") {
	case 0.5:
		flags |= val("G_Halfleech")
	case 0.75:
		flags |= val("G_Freeleech25")
	case 0.25:
		flags |= val("G_Freeleech75")
	case 0:
		flags |= val("G_Freeleech")
	}
	if factor("uploadvolumefactor") == 2 {
		flags |= val("G_DoubleUpload")
	}
	for _, t := range r.Flags {
		switch strings.ToLower(t) {
		case "internal":
			flags |= val("G_Internal")
		case "scene":
			flags |= val("G_Scene")
		}
	}
	return flags
}
