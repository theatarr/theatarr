package decide

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/adapt"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// decideSeries is Sonarr's DownloadDecisionMaker.GetDecisions for one release and one series version: parse (with
// the special-episode fallback), map to the series and its episodes (series.go), aggregate languages, compute the
// formats and score, and run the specifications with every mapped episode's file.
func (e *Engine) decideSeries(ctx context.Context, d *Decision, p *Profile, rel Release, t *Title, v Version, search *SeriesSearch) error {
	fl := format.Sonarr
	ver, err := sonarr.ParseVersion(v.Policy.Sonarr)
	if err != nil {
		return fmt.Errorf("decide: version %s: %w", v.Key, err)
	}
	s := newSeries(t, ver)
	s.parseTitle = func(title string) *sonarr.ParsedEpisodeInfo { return ParseSeriesRelease(ctx, ver, title) }
	parsed := s.parseTitle(rel.Title)
	if parsed == nil || parsed.IsPossibleSpecialEpisode() {
		if sp := s.parseSpecialEpisodeTitle(parsed, rel.Title, rel.TVDB, rel.IMDb, search); sp != nil {
			parsed = sp
		}
	}
	if parsed == nil {
		// v5: a release named by a season's own title (an anime season pack), ParseSeasonTitle (nil in v4).
		parsed = parseSeriesRelease(ctx, ver, rel.Title, true)
	}
	if rel.Manual != nil {
		if parsed == nil {
			parsed = s.parseTitle(t.Name + ".S01E01")
		}
		if parsed != nil {
			parsed.SeriesTitle = t.Name
			if rel.Manual.ReleaseGroup != nil {
				parsed.ReleaseGroup = rel.Manual.ReleaseGroup
			}
		}
	}
	if parsed == nil || strings.TrimSpace(parsed.SeriesTitle) == "" {
		q := ver.ParseQuality(rel.Title)
		d.Quality, d.Revision = q.Quality.Name, profile.Revision{Version: q.Revision.Version, Real: q.Revision.Real}
		d.Rejections = append(e.ourRules(rel, v), Rejection{Reason: "UnableToParse", Message: "Unable to parse release"})
		return nil
	}
	d.Parsed = true
	d.SeriesTitle = parsed.SeriesTitle
	if season, ok := decisionSeason(parsed); ok {
		d.Season = &season
	}
	d.Numbers, d.Absolute = parsed.EpisodeNumbers, parsed.AbsoluteEpisodeNumbers
	d.ReleaseType = parsed.ReleaseType().String()
	if parsed.ReleaseGroup != nil {
		d.ReleaseGroup = *parsed.ReleaseGroup
	}
	q, ok := profile.QualityByID(fl, parsed.Quality.Quality.ID)
	if !ok {
		return fmt.Errorf("decide: parsed quality %d is not a sonarr quality", parsed.Quality.Quality.ID)
	}
	qm := profile.QualityModel{Quality: q, Revision: profile.Revision{Version: parsed.Quality.Revision.Version,
		Real: parsed.Quality.Revision.Real, IsRepack: parsed.Quality.Revision.IsRepack}}
	qm = rel.Manual.quality(fl, qm)
	qm.SeaDexPreference = indexer.SeaDexPreference(&rel.Release)
	d.Quality, d.Revision = qm.Quality.Name, qm.Revision

	r, err := s.mapRelease(parsed, rel.TVDB, rel.IMDb, search)
	if err != nil {
		d.Rejections = append(e.ourRules(rel, v), Rejection{Reason: "Error", Message: "Unexpected error processing release"})
		return nil
	}
	if rel.Manual != nil {
		r.matched, r.matchType, r.episodes = true, MatchTitle, nil
		for i := range t.Episodes {
			for _, id := range rel.Manual.Episodes {
				if t.Episodes[i].ID == id {
					r.episodes = append(r.episodes, &t.Episodes[i])
				}
			}
		}
	}
	if !r.matched && t.AnimeMap.Named(ver.CleanSeriesTitle(parsed.SeriesTitle), rel.Title, ver.CleanSeriesTitle) {
		r.matched, r.matchType, r.episodes = true, MatchAlias, nil // a franchise entry's name or an alternate cut
	}
	if !r.matched {
		d.Rejections = append(e.ourRules(rel, v), Rejection{Reason: string(profile.ReasonUnknownSeries), Message: "Unknown Series"})
		return nil
	}
	d.MatchType = r.matchType
	var animeRejections []Rejection
	if rel.Manual == nil {
		animeRejections, err = s.applyAnimeMap(d, parsed, rel, r, t.AnimeMap)
	}
	if err != nil {
		d.Rejections = append(e.ourRules(rel, v), Rejection{Reason: "Error", Message: "Unexpected error processing release"})
		return nil
	}
	if len(r.episodes) == 0 {
		d.Rejections = append(e.ourRules(rel, v), Rejection{Reason: string(profile.ReasonUnknownEpisode),
			Message: "Unable to identify correct episode(s) using release name and scene mappings"})
		return nil
	}
	for _, ep := range r.episodes {
		d.Episodes = append(d.Episodes, fmt.Sprintf("S%02dE%02d", ep.Season, ep.Number))
		d.EpisodeIDs = append(d.EpisodeIDs, ep.ID)
		if ep.File == nil {
			d.Missing++
		}
	}

	original := languageID(fl, t.OriginalLanguage)
	flags := indexerFlags(fl, &rel)
	ar := &adapt.Release{Title: rel.Title, Size: rel.Size, IndexerFlags: flags,
		Languages:      languageIDs(fl, rel.Languages),
		MultiLanguages: languageIDs(fl, indexerMulti(e.Policy, rel.Indexer))}
	if len(ar.Languages) == 0 && len(ar.MultiLanguages) > 0 && ver.HasMultipleLanguages(rel.Title) {
		ar.Languages = append([]int{}, ar.MultiLanguages...)
	}
	var epTitles []adapt.Episode
	for _, ep := range r.episodes {
		epTitles = append(epTitles, adapt.Episode{Title: ep.Title})
	}
	in, err := adapt.SonarrRelease(parsed, &adapt.Series{Year: t.Year, OriginalLanguage: original}, epTitles, ar)
	if err != nil {
		return err
	}
	rel.Manual.input(in, qm)
	in.SeaDex = SeaDexRank(&rel.Release)
	matched, _ := format.Calculate(p.Formats, in)
	d.Formats = formatScores(p.Profile, matched)
	d.Score = p.Profile.FormatScore(matched)
	for _, l := range in.Languages {
		d.Languages = append(d.Languages, fl.LanguageName(l))
	}
	d.LanguagesAssumed = in.LanguagesAssumed

	media := &profile.Media{Title: t.Name, Runtime: t.Runtime, OriginalLanguage: original, Anime: s.typ == sonarr.SeriesTypeAnime,
		Monitored: t.Monitored, SeriesType: s.typ.String(), Pilot: pilot(t)}
	for i := range t.Episodes {
		media.Episodes = append(media.Episodes, profileEpisode(&t.Episodes[i]))
	}
	media.EpisodeTitleRequired = e.requiresEpisodeTitle(t, v, r.episodes)
	files := map[int64]*profile.File{}
	var episodes []*profile.Episode
	var firstFile *profile.File
	for _, ep := range r.episodes {
		pe := profileEpisode(ep)
		if ep.File != nil {
			key := ep.FileID
			f := files[key]
			if f == nil || key == 0 {
				x := resolveSeaDex(ctx, ep.File)
				if x.SeaDex == format.SeaDexNone && x.InfoHash != "" && x.InfoHash == rel.InfoHash {
					x.SeaDex = in.SeaDex
				}
				if SameTorrent(&x, &ExistingFile{InfoHash: rel.InfoHash, SceneName: rel.Title}) {
					x.SeaDexPreference = qm.SeaDexPreference
				}
				if f, err = seriesFile(&x, t, original, p); err != nil {
					return err
				}
				if key != 0 {
					files[key] = f
				}
			}
			pe.File = f
			if firstFile == nil {
				firstFile = f
			}
		}
		episodes = append(episodes, pe)
	}
	if firstFile != nil {
		d.Existing = &Existing{Quality: firstFile.Quality.Quality.Name, Formats: formatScores(p.Profile, firstFile.Formats),
			Score: p.Profile.FormatScore(firstFile.Formats)}
	}
	prio := rel.IndexerPriority
	if prio == 0 {
		prio = 25
	}
	pr := &profile.Release{Title: rel.Title, Size: rel.Size, Quality: qm, Languages: in.Languages, Formats: matched,
		Indexer: rel.Indexer, MinimumSeeders: e.minimumSeeders(rel.Indexer), InfoHash: rel.InfoHash,
		IndexerPriority: prio, IndexerFlags: flags, PublishDate: rel.PublishDate, Protocol: profile.ProtocolTorrent,
		FullSeason: parsed.FullSeason, Special: parsed.Special, SeasonNumber: parsed.SeasonNumber,
		EpisodeNumbers: parsed.EpisodeNumbers, MultiSeason: parsed.IsMultiSeason, SplitEpisode: parsed.IsSplitEpisode}
	if parsed.ReleaseGroup != nil {
		pr.ReleaseGroup = *parsed.ReleaseGroup
	}
	if parsed.Parser == sonarr.V5 {
		pr.V5, pr.SeasonNumbers = true, parsed.SeasonNumbers
		if !r.noMappedSeason {
			x := r.mappedSeason
			pr.MappedSeasonNumber = &x
		}
	}
	if rel.Usenet {
		pr.Protocol = profile.ProtocolUsenet
	} else {
		if rel.Seeders >= 0 {
			x := rel.Seeders
			pr.Seeders = &x
		}
		if rel.Peers >= 0 {
			x := rel.Peers
			pr.Peers = &x
		}
	}
	subj := &profile.Subject{Profile: p.Profile, Media: media, Release: pr, Episodes: episodes}
	if search != nil {
		subj.Search = profileSearch(search, s)
	}
	if r.sceneMapping != nil {
		subj.SceneMapping = &profile.SceneMapping{SceneOrigin: r.sceneMapping.SceneOrigin, Comment: r.sceneMapping.Comment}
	}
	subj.Score()
	d.subject = subj

	var group0, disk []Rejection
	for _, rj := range p.Engine.Evaluate(subj) {
		x := Rejection{Reason: string(rj.Reason), Message: rj.Message, Spec: rj.Spec, Temporary: rj.Type == profile.Temporary}
		if sp, ok := p.Engine.Spec(rj.Spec); ok && sp.Priority == profile.PriorityDisk {
			disk = append(disk, x)
		} else {
			group0 = append(group0, x)
		}
	}
	group0 = append(append(e.ourRules(rel, v), animeRejections...), group0...)
	if len(group0) > 0 {
		d.Rejections = group0
	} else {
		d.Rejections = disk
	}

	// Upgrade: the release upgrades every file it replaces (UpgradeDisk rejects it otherwise); the reason is the
	// first file's. A release whose episodes have no file fills them. Under packs: any_upgradable (and threshold, for
	// a full-season pack) an accepted pack may keep some files: the reason is the first upgraded file's, or the
	// episodes it fills.
	if firstFile != nil {
		file, kept := firstFile, 0
		rule := ""
		switch {
		case p.Engine.Config.AcceptReleaseAnyUpgradable:
			rule = policy.PacksAnyUpgradable
		case p.Engine.Config.SeasonPackUpgradeThreshold > 0 && pr.FullSeason:
			rule = policy.PacksThreshold
		}
		if rule != "" && len(d.Rejections) == 0 {
			file, kept = firstUpgraded(p, episodes, qm, matched)
		}
		if file == nil {
			d.Upgrade, d.UpgradeReason = true, fmt.Sprintf("fills %d episode(s) with no file", d.Missing)
		} else {
			d.Upgrade, d.UpgradeReason = upgradeOf(p, file, qm, matched, d.Rejections)
			if len(d.Rejections) > 0 && d.Upgrade {
				d.Upgrade, d.UpgradeReason = false, "the release is rejected; it would upgrade the file ("+d.UpgradeReason+")"
			}
			if d.Upgrade && d.Missing > 0 {
				d.UpgradeReason += fmt.Sprintf("; fills %d episode(s) with no file", d.Missing)
			}
		}
		if d.Upgrade && kept > 0 {
			d.UpgradeReason += fmt.Sprintf("; %d episode(s) keep their file (packs: %s)", kept, rule)
		}
	}
	return nil
}

// decisionSeason is the season the decision reports: the parsed season; for a v5 parse the first of SeasonNumbers,
// none when it has none (a season-title release).
func decisionSeason(p *sonarr.ParsedEpisodeInfo) (int, bool) {
	if p.Parser == sonarr.V5 {
		if len(p.SeasonNumbers) == 0 {
			return 0, false
		}
		return p.SeasonNumbers[0], true
	}
	return p.SeasonNumber, true
}

// firstUpgraded is the first episode file the release upgrades (cutoff not met and an upgrade, as UpgradeDisk
// judges each file), nil when it upgrades none; kept counts the episodes whose file it does not upgrade.
func firstUpgraded(p *Profile, episodes []*profile.Episode, qm profile.QualityModel, formats []*format.Format) (*profile.File, int) {
	var first *profile.File
	kept := 0
	for _, ep := range episodes {
		f := ep.File
		if f == nil {
			continue
		}
		if p.Engine.CutoffNotMet(p.Profile, f.Quality, f.Formats, &qm) &&
			p.Engine.IsUpgradable(p.Profile, f.Quality, f.Formats, qm, formats) == profile.UpgradeNone {
			if first == nil {
				first = f
			}
			continue
		}
		kept++
	}
	return first, kept
}

func profileEpisode(ep *Episode) *profile.Episode {
	return &profile.Episode{ID: ep.ID, Monitored: ep.Monitored, SeasonNumber: ep.Season, EpisodeNumber: ep.Number,
		Runtime: ep.Runtime, AirDateUtc: ep.AirDateUTC, Title: ep.Title, LastGrab: ep.LastGrab}
}

// pilot is the first episode of the lowest season above 0 (Sonarr's AcceptableSize fallback when the series runtime
// is 0), nil when the series has no such season.
func pilot(t *Title) *profile.Episode {
	var best *Episode
	for i := range t.Episodes {
		e := &t.Episodes[i]
		if e.Season <= 0 {
			continue
		}
		if best == nil || e.Season < best.Season || e.Season == best.Season && e.Number < best.Number {
			best = e
		}
	}
	if best == nil {
		return nil
	}
	return profileEpisode(best)
}

// profileSearch is the search criteria the specifications read, with the requested episodes.
func profileSearch(sc *SeriesSearch, s *series) *profile.Search {
	ps := &profile.Search{Kind: sc.Kind, SeasonNumber: sc.Season, EpisodeNumber: sc.Episode,
		MonitoredEpisodesOnly: sc.MonitoredOnly, Interactive: sc.Interactive}
	for _, e := range s.searchEpisodes(sc) {
		ps.Episodes = append(ps.Episodes, profileEpisode(e))
	}
	sort.SliceStable(ps.Episodes, func(i, j int) bool { return ps.Episodes[i].ID < ps.Episodes[j].ID })
	return ps
}

// seriesFile is an episode file with its formats, as CustomFormatCalculationService.ParseCustomFormat(EpisodeFile,
// Series) computes them: release title from the scene name, else the original file name, else the relative path's
// file name; the file's quality, languages, release group, size, indexer flags and release type.
func seriesFile(x *ExistingFile, t *Title, original int, p *Profile) (*profile.File, error) {
	fl := format.Sonarr
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
	in := &format.Input{Flavour: fl, ReleaseTitle: format.Str(title), Source: q.Source, Resolution: q.Resolution,
		Languages: languageIDs(fl, x.Languages), Size: x.Size, IndexerFlags: x.IndexerFlags,
		Media: &format.Media{Year: t.Year, OriginalLanguage: original}, SeaDex: x.SeaDex}
	if in.Languages == nil {
		in.Languages = []int{}
	}
	if x.ReleaseType != "" {
		if rt, err := format.ReleaseTypeValue(x.ReleaseType); err == nil {
			in.ReleaseType = rt
		}
	}
	if x.RelativePath != "" {
		in.Filename = format.Str(path.Base(x.RelativePath))
	}
	if x.ReleaseGroup != "" {
		in.ReleaseGroup = format.Str(x.ReleaseGroup)
	}
	formats, _ := format.Calculate(p.Formats, in)
	f := &profile.File{Quality: profile.QualityModel{Quality: q, Revision: rev, SeaDexPreference: fileSeaDexPreference(x, fl)}, Formats: formats, ReleaseGroup: x.ReleaseGroup,
		EpisodeIDs: x.EpisodeIDs, Added: x.Added}
	return f, nil
}
