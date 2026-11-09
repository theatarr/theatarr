package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// isAutomatic identifies routes whose candidates describe current acquisition intent, not an explicit grab.
func isAutomatic(source string) bool {
	return source == "job" || source == "wanted" || source == "rss" || source == "research" || source == "offer"
}

func (r *Router) automatic(ctx context.Context, source string, resumed bool) (bool, error) {
	if isAutomatic(source) {
		return true, nil
	}
	// A release push (autobrr, arrview:push:<view>) was decided against the current files when it was pushed; one
	// deferred behind a paced provider is checked again when it resumes, as an RSS route is.
	if strings.HasPrefix(source, "arrview:push:") {
		return resumed, nil
	}
	if !strings.HasPrefix(source, "acquire:") {
		return false, nil
	}
	// A command's run for a version the caller did not name (a title search widened to the title's other active
	// versions, requested = 0) is checked on its first pass too: only the named versions take user-invoked semantics.
	var origin string
	var requested bool
	err := r.st.Writer().QueryRowContext(ctx, `SELECT source, requested FROM acquire_run WHERE id = ?`,
		strings.TrimPrefix(source, "acquire:")).Scan(&origin, &requested)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("router: route source: %w", err)
	}
	return origin == "wanted" || origin == "command" && (resumed || !requested), nil
}

// freshCandidates keeps only candidates that are still wanted by the catalog and current files.
func (r *Router) freshCandidates(ctx context.Context, s *routeState) ([]Candidate, error) {
	var kind string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT t.kind FROM title t JOIN version v ON v.title_id = t.id
		WHERE t.id = ? AND t.monitored = 1 AND v.id = ? AND v.policy_key = ? AND v.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version rv WHERE rv.version_id = v.id)`,
		s.req.TitleID, s.req.VersionID, s.req.Version).Scan(&kind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("router: route intent: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) || s.vp.Mode != policy.ModeActive || kind != string(s.vp.Kind) {
		return r.cancelFresh(ctx, s, "the title or version is no longer monitored and active", "not_wanted")
	}
	if s.vp.Kind == policy.KindMovie {
		available, err := availability.Movie(ctx, r.st.Writer(), r.pol, s.req.TitleID, s.req.VersionID, s.req.Version, r.now())
		if err != nil {
			return nil, fmt.Errorf("router: route availability: %w", err)
		}
		if !available {
			return r.cancelFresh(ctx, s, "the movie has not reached minimum availability", "unavailable")
		}
	}

	// A dead link's search (ReasonDeadLink) is for a missing target: once it is filled again (revived, replaced) the
	// route adds nothing, not even an upgrade of the new file; a series candidate must still cover one of its
	// episodes that is missing.
	dead, deadEps, err := r.deadLinkRoute(ctx, s.req.Source)
	if err != nil {
		return nil, err
	}
	var missing []int64
	if dead {
		if s.vp.Kind == policy.KindSeries {
			if missing, err = r.missingEpisodes(ctx, s.req.VersionID, deadEps); err != nil {
				return nil, err
			}
			if len(missing) == 0 {
				return r.cancelFresh(ctx, s, "the dead link's episodes are no longer missing", "not_wanted")
			}
		} else {
			var state string
			if err := r.st.Writer().QueryRowContext(ctx, `SELECT state FROM version WHERE id = ?`, s.req.VersionID).
				Scan(&state); err != nil {
				return nil, fmt.Errorf("router: route intent: %w", err)
			}
			if state != "wanted" {
				return r.cancelFresh(ctx, s, "the dead link's movie is no longer missing", "not_wanted")
			}
		}
	}

	var info *routeProfile
	profileLoaded := false
	loadProfile := func() (*routeProfile, error) {
		if !profileLoaded {
			info, err = r.routeProfile(ctx, s.vp)
			if err != nil {
				return nil, err
			}
			profileLoaded = true
		}
		return info, nil
	}
	// One snapshot per freshness check, including repeated episodes across candidates. Do not carry file or
	// monitoring state into the next check, which must see intervening imports and monitoring edits.
	var seriesFiles map[int64]itemFiles
	var movieFiles itemFiles
	if s.vp.Kind == policy.KindSeries {
		seriesFiles, err = r.freshEpisodeFiles(ctx, s)
	} else {
		movieFiles, err = r.currentFilesNow(ctx, s, nil)
	}
	if err != nil {
		return nil, err
	}
	var kept []Candidate
	for _, c := range s.req.Candidates {
		if dead && s.vp.Kind == policy.KindSeries && !slices.ContainsFunc(c.Episodes, func(id int64) bool {
			return slices.Contains(missing, id)
		}) {
			r.skipFresh(s, c, "not_wanted", "the release holds none of the dead link's missing episodes")
			continue
		}
		before := len(c.Episodes)
		if s.vp.Kind == policy.KindSeries {
			var episodes []int64
			seen := map[int64]bool{}
			for _, id := range c.Episodes {
				if seen[id] {
					continue
				}
				seen[id] = true
				cur, monitored := seriesFiles[id]
				if !monitored {
					continue
				}
				cur, err = r.scoreCurrentFiles(ctx, s, cur)
				if err != nil {
					return nil, err
				}
				if cur.n > 0 {
					pi, err := loadProfile()
					if err != nil {
						return nil, fmt.Errorf("router: route profile: %w", err)
					}
					better, err := freshUpgrade(pi, s.vp, c, cur)
					if err != nil {
						return nil, err
					}
					if !better {
						continue
					}
				}
				episodes = append(episodes, id)
			}
			c.Episodes = episodes
			if len(episodes) == 0 {
				code, message := "not_wanted", "none of the release's episodes is still monitored and missing"
				if before > 0 {
					code, message = "no_longer_upgrade", "the targeted episodes are no longer wanted or the release does not improve their files"
				}
				r.skipFresh(s, c, code, message)
				continue
			}
			if len(episodes) != before {
				r.skipFresh(s, c, "episodes_unwanted", "removed episodes that are no longer monitored, or whose files are not improved")
			}
		} else {
			cur := movieFiles
			if cur.n > 0 {
				pi, err := loadProfile()
				if err != nil {
					return nil, fmt.Errorf("router: route profile: %w", err)
				}
				better, err := freshUpgrade(pi, s.vp, c, cur)
				if err != nil {
					return nil, err
				}
				if !better {
					r.skipFresh(s, c, "no_longer_upgrade", "the version now has an equal or better file")
					continue
				}
			}
		}
		kept = append(kept, c)
	}

	if s.req.DownloadID != 0 {
		var hash string
		err := r.st.Writer().QueryRowContext(ctx, `SELECT info_hash FROM download WHERE id = ? AND state = 'queued'`,
			s.req.DownloadID).Scan(&hash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("router: checking resumed download: %w", err)
		}
		if err == nil && !slicesContainsHash(kept, hash) {
			if err := r.failDownload(ctx, s.req.DownloadID, "route cancelled: its release is no longer wanted",
				StateQueued); err != nil {
				return nil, err
			}
		}
	}
	if len(kept) == 0 {
		return r.finishFresh(ctx, s, "no release is still wanted by this version")
	}
	return kept, nil
}

// redecide decides the candidates of an automatic route again when the route was queued under another policy
// revision (a deferred RSS or search grab): a candidate the current profile rejects is dropped, the
// others take the current decision's quality, score and formats. The release inputs are the ones stored with the
// route. Without a decision engine the stale candidates are dropped. Manual grabs are never re-decided. A resumed
// series route that needs no re-decision still has its episodes mapped again (remapResumed).
func (r *Router) redecide(ctx context.Context, s *routeState, cands []Candidate) ([]Candidate, error) {
	series := s.vp.Kind == policy.KindSeries
	if !s.vp.PreferSeaDex && (s.req.DecisionRev == "" || s.req.DecisionRev == r.pol.Revision) {
		if series && s.req.DownloadID != 0 {
			return r.remapResumed(ctx, s, cands)
		}
		return cands, nil
	}
	if r.decide == nil || series && r.decideSeries == nil {
		for _, c := range cands {
			r.skipFresh(s, c, "stale_decision", "decided under policy revision "+s.req.DecisionRev+
				"; no decision engine here to decide it again")
		}
		return r.finishFresh(ctx, s, "the route was decided under an older policy")
	}
	db := r.st.Writer()
	t, err := decide.LoadTitle(ctx, db, s.req.TitleID)
	if err != nil {
		return nil, fmt.Errorf("router: re-decide: %w", err)
	}
	v, err := decide.LoadVersion(ctx, db, r.pol, s.req.TitleID, s.req.Version)
	if err != nil {
		return nil, fmt.Errorf("router: re-decide: %w", err)
	}
	var existing *decide.ExistingFile
	if series {
		if err := decide.LoadSeriesVersion(ctx, db, &t, v.ID); err != nil {
			return nil, fmt.Errorf("router: re-decide: %w", err)
		}
	} else if existing, err = decide.LoadExisting(ctx, db, v.ID); err != nil {
		return nil, fmt.Errorf("router: re-decide: %w", err)
	}
	// Cache rows written while the route waited feed the ranking's cache tie-break: stored evidence only, nothing is
	// asked. The decisions read it; the candidates keep the releases they were queued with, so the route orders its
	// provider accounts as before.
	ranked := make([]indexer.Release, len(cands))
	for i := range cands {
		ranked[i] = cands[i].Release
	}
	indexer.NewEvidence(indexer.EvidenceConfig{Store: r.st, Policy: r.pol, Logger: r.log, Now: r.now}).ApplyStored(ctx, ranked)
	var kept []Candidate
	var decisions []*decide.Decision
	// One re-search for the batch's unproven candidates, with all their episodes (a per-candidate re-search would be
	// deduplicated into the first one).
	var unproven []int64
	anyUnproven := false
	byDecision := map[*decide.Decision]Candidate{}
	for i, c := range cands {
		queued := c.Release
		c.Release = ranked[i]
		var d *decide.Decision
		switch {
		case !s.vp.PreferSeaDex && series:
			// A prefer-off version re-decides exactly as before standing existed (only on a revision change).
			ss := offerSeriesSearch(&t, c.Episodes)
			d, err = decideBest(ctx, r.decideSeries, c.Release, t, v, plainCriteria(ss, criteriaQueries(&t, ss, r.now())), ss)
		case !s.vp.PreferSeaDex:
			d, err = r.decide(ctx, decide.Release{Release: c.Release}, t, v, existing)
		case c.Identity < RouteIdentityVersion || c.Origin == "" || series && c.Origin == OriginSearch && len(c.Criteria) == 0:
			// Unproven: queued by an older build (no identity validation or origin), or a series search without its
			// criteria. It is not trusted; the episodes are searched again.
			r.skipFresh(s, c, "search_criteria_unknown", "queued identity, origin or search coverage cannot be proved; re-search required")
			anyUnproven = true
			for _, ep := range c.Episodes {
				if !slices.Contains(unproven, ep) {
					unproven = append(unproven, ep)
				}
			}
			continue
		case series && c.Origin == OriginSearch:
			d, err = decideBest(ctx, r.decideSeries, c.Release, t, v, c.Criteria, offerSeriesSearch(&t, c.Episodes))
		case series:
			d, err = r.decideSeries(ctx, decide.Release{Release: c.Release}, t, v, nil)
		case c.Origin == OriginSearch:
			d, err = r.decide(decide.WithSearch(ctx), decide.Release{Release: c.Release}, t, v, existing)
		default:
			d, err = r.decide(ctx, decide.Release{Release: c.Release}, t, v, existing)
		}
		if err != nil {
			return nil, fmt.Errorf("router: re-decide %s: %w", c.Release.Title, err)
		}
		if !d.Accepted {
			var why []string
			for _, rj := range d.Rejections {
				why = append(why, rj.String())
			}
			r.skipFresh(s, c, "rejected_now", "rejected under the current policy: "+strings.Join(why, "; "))
			continue
		}
		c.Quality, c.Score, c.Formats = d.Quality, d.Score, append([]decide.FormatScore{}, d.Formats...)
		c.EpisodeMap, c.QualityRank, c.UpgradeReason = d.EpisodeMapping(), d.QualityRank(), upgradeReason(d)
		if series && len(d.EpisodeIDs) > 0 && !subset(c.Episodes, d.EpisodeIDs) {
			// The release now maps to other episodes (a scene/XEM mapping changed while it waited): the
			// download is for the new ones, checked for freshness again by the caller.
			c.Episodes = append([]int64(nil), d.EpisodeIDs...)
			s.remapped = true
		}
		c.Release = queued
		decisions = append(decisions, d)
		byDecision[d] = c
	}
	if anyUnproven {
		r.research(ctx, s.req.TitleID, s.req.VersionID, s.req.Version, unproven, "legacy_criteria", s.req.DownloadID)
	}
	if len(decisions) == 0 {
		return r.finishFresh(ctx, s, "no release is accepted under the current policy")
	}
	// Tried in the order the current decisions rank them, as a fresh search would. The queued download
	// this route resumes records the decision it is placed under when its candidate opens it (openDownload).
	for _, d := range decide.Rank(decisions) {
		kept = append(kept, byDecision[d])
	}
	s.redecided = true
	return kept, nil
}

// remapResumed maps a resumed series route's RSS candidates to the episodes their releases map to now, when the
// route is not decided again otherwise (prefer-off, same revision): a scene/XEM mapping may have changed while it
// waited. Only the episodes change, and only on an accepted decision; anything else keeps the
// candidate as queued.
func (r *Router) remapResumed(ctx context.Context, s *routeState, cands []Candidate) ([]Candidate, error) {
	if r.decideSeries == nil {
		return cands, nil
	}
	var t *decide.Title
	var v decide.Version
	out := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Origin == OriginSearch {
			out = append(out, c)
			continue
		}
		if t == nil {
			db := r.st.Writer()
			title, err := decide.LoadTitle(ctx, db, s.req.TitleID)
			if err != nil {
				return nil, fmt.Errorf("router: remap: %w", err)
			}
			if v, err = decide.LoadVersion(ctx, db, r.pol, s.req.TitleID, s.req.Version); err != nil {
				return nil, fmt.Errorf("router: remap: %w", err)
			}
			if err := decide.LoadSeriesVersion(ctx, db, &title, v.ID); err != nil {
				return nil, fmt.Errorf("router: remap: %w", err)
			}
			t = &title
		}
		d, err := r.decideSeries(ctx, decide.Release{Release: c.Release}, *t, v, nil)
		if err != nil {
			return nil, fmt.Errorf("router: remap %s: %w", c.Release.Title, err)
		}
		if d.Accepted && len(d.EpisodeIDs) > 0 && !subset(c.Episodes, d.EpisodeIDs) {
			c.Episodes = append([]int64(nil), d.EpisodeIDs...)
			c.EpisodeMap = d.EpisodeMapping()
			s.remapped = true
		}
		out = append(out, c)
	}
	return out, nil
}

// missingEpisodes keeps the episodes of ids the version still misses (episode_version_state wanted).
func (r *Router) missingEpisodes(ctx context.Context, versionID int64, ids []int64) ([]int64, error) {
	var out []int64
	for _, id := range ids {
		var state string
		err := r.st.Writer().QueryRowContext(ctx, `SELECT state FROM episode_version_state WHERE version_id = ?
			AND episode_id = ?`, versionID, id).Scan(&state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("router: route intent: %w", err)
		}
		if err == nil && state == "wanted" {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r *Router) cancelFresh(ctx context.Context, s *routeState, message, code string) ([]Candidate, error) {
	for _, c := range s.req.Candidates {
		r.skipFresh(s, c, code, message)
	}
	return r.finishFresh(ctx, s, message)
}

func (r *Router) finishFresh(ctx context.Context, s *routeState, message string) ([]Candidate, error) {
	s.res.Outcome = OutcomeCancelled
	s.res.DownloadID = s.req.DownloadID
	s.res.Steps = append(s.res.Steps, Step{Outcome: "cancelled", Code: "route_cancelled", Message: message})
	if s.req.DownloadID != 0 {
		if err := r.failDownload(ctx, s.req.DownloadID, "route cancelled: "+message, StateQueued); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (r *Router) skipFresh(s *routeState, c Candidate, code, message string) {
	s.res.Steps = append(s.res.Steps, Step{Release: c.Release.Title,
		Hash: indexer.NormalizeHash(c.Release.InfoHash), Outcome: "skipped", Code: code, Message: message})
}

// subset reports whether every id of a is in b.
func subset(a, b []int64) bool {
	for _, id := range a {
		if !slices.Contains(b, id) {
			return false
		}
	}
	return true
}

func slicesContainsHash(cands []Candidate, hash string) bool {
	for _, c := range cands {
		if indexer.NormalizeHash(c.Release.InfoHash) == hash {
			return true
		}
	}
	return false
}

type routeProfile struct {
	quality *profile.Profile
	propers profile.ProperDownloadTypes
}

func freshUpgrade(p *routeProfile, vp *policy.VersionPolicy, c Candidate, cur itemFiles) (bool, error) {
	// A prefer-on version does not replace a held file with the same torrent: its standing is the torrent's. A
	// prefer-off version upgrades as before standing existed (one torrent can hold several qualities).
	if vp.PreferSeaDex && cur.file != nil && decide.SameTorrent(cur.file, &decide.ExistingFile{InfoHash: c.Release.InfoHash, SceneName: c.Release.Title}) {
		return false, nil
	}
	if vp.UpgradeRule() == policy.UpgradesOff {
		return false, nil
	}
	quality, ok := profile.QualityByName(p.quality.Flavour, c.Quality)
	if !ok {
		return false, fmt.Errorf("router: route candidate quality %q is unknown", c.Quality)
	}
	current, ok := profile.QualityByName(p.quality.Flavour, cur.quality)
	if !ok {
		return true, nil
	}
	oldScore := 0
	if cur.score != nil {
		oldScore = int(*cur.score)
	}
	newFile := decide.LinkedFile(vp.Kind, vp.Sonarr, decide.Release{Release: c.Release}, c.Quality)
	newModel := profile.QualityModel{Quality: quality, SeaDexPreference: newFile.SeaDexPreference, Revision: profile.Revision{Version: max(newFile.Revision, 1), Real: newFile.Real}}
	currentModel := profile.QualityModel{Quality: current, SeaDexPreference: decide.FileSeaDexPreference(cur.file), Revision: profile.Revision{Version: max(cur.revision, 1), Real: cur.real}}
	return newRouteComparison(p, vp, currentModel, oldScore, newModel, c.Score).upgradable() == profile.UpgradeNone, nil
}

// routeProfile is the version's profile for the freshness and queue checks: the decision engine's compiled profile
// when the router has one (SetProfiles), so both judge with the thresholds the decision did; without
// one (a CLI route, tests) it is built here the same way.
func (r *Router) routeProfile(ctx context.Context, vp *policy.VersionPolicy) (*routeProfile, error) {
	pr := r.pol.Profiles[vp.Profile]
	fl := format.Radarr
	if vp.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	if pr == nil {
		return nil, fmt.Errorf("version profile %q is missing", vp.Profile)
	}
	if r.profiles != nil {
		if p := r.profiles(vp.Profile, fl); p != nil && p.Profile != nil && p.Engine != nil {
			return &routeProfile{quality: p.Profile, propers: p.Engine.Config.DownloadPropersAndRepacks}, nil
		}
	}
	if pr.FromArr != "" {
		instance, name, ok := policy.SplitFromArr(pr.FromArr)
		if !ok {
			return nil, fmt.Errorf("invalid arr profile %q", pr.FromArr)
		}
		var raw string
		if err := r.st.Writer().QueryRowContext(ctx, `SELECT body FROM arr_policy_snapshot WHERE instance = ?`, instance).
			Scan(&raw); err != nil {
			return nil, fmt.Errorf("arr profile %s: %w", instance, err)
		}
		var snapshot map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return nil, err
		}
		profiles, err := profile.ParseArrProfiles(fl, snapshot["qualityprofile"])
		if err != nil {
			return nil, err
		}
		for _, p := range profiles {
			if strings.EqualFold(p.Name, name) {
				proper := profile.PreferAndUpgrade
				var settings struct {
					DownloadPropersAndRepacks string `json:"downloadPropersAndRepacks"`
				}
				if raw := snapshot["config/mediamanagement"]; len(raw) > 0 {
					if err := json.Unmarshal(raw, &settings); err != nil {
						return nil, err
					}
				}
				if settings.DownloadPropersAndRepacks != "" {
					proper, err = profile.ParseProperDownloadTypes(settings.DownloadPropersAndRepacks)
					if err != nil {
						return nil, err
					}
				}
				return &routeProfile{quality: p, propers: proper}, nil
			}
		}
		return nil, fmt.Errorf("arr profile %q is missing from instance %q", name, instance)
	}
	allowed := map[string]bool{}
	for _, q := range pr.Qualities {
		allowed[q.Name] = true
		for _, name := range q.Qualities {
			allowed[name] = true
		}
	}
	order := pr.Qualities
	if len(pr.QualityOrder) > 0 {
		order = pr.QualityOrder
	}
	items := make([]format.TrashQualityItem, 0, len(order))
	for _, q := range order {
		item := format.TrashQualityItem{Name: q.Name, Allowed: allowed[q.Name]}
		if q.Group {
			item.Items = q.Qualities
		}
		items = append(items, item)
	}
	upgradeAllowed := pr.Upgrades == nil || *pr.Upgrades
	quality, err := profile.FromItems(fl, vp.Profile, items, pr.Cutoff, upgradeAllowed)
	if err != nil {
		return nil, err
	}
	// The format thresholds as decide.fromPolicy sets them; the format items are the two scores compared.
	step := 1
	if pr.MinUpgradeStep != nil {
		step = *pr.MinUpgradeStep
	}
	quality.Profile = format.Profile{Name: vp.Profile, Flavour: fl, MinFormatScore: pr.MinScore,
		CutoffFormatScore: pr.CutoffScore, MinUpgradeFormatScore: step}
	proper := profile.PreferAndUpgrade
	switch pr.Propers {
	case policy.PropersDoNotUpgrade:
		proper = profile.DoNotUpgrade
	case policy.PropersDoNotPrefer:
		proper = profile.DoNotPrefer
	}
	return &routeProfile{quality: quality, propers: proper}, nil
}

// currentFilesNow is currentFiles with the first file's custom format score recalculated under the version's running
// profile, as the decision engine compares an existing file: a score stored at import (link.score,
// version_file.custom_format_score) is the old profile's, and after a profile or TRaSH score change it would refuse
// an upgrade the decision accepted. Without the decision engine's profiles (a CLI route, tests) or when the file's
// inputs cannot be scored, the stored score stands.
func (r *Router) currentFilesNow(ctx context.Context, s *routeState, episodes []int64) (itemFiles, error) {
	cur, err := currentFiles(ctx, r.st.Writer(), s.req.TitleID, s.req.VersionID, episodes)
	if err != nil || cur.n == 0 {
		return cur, err
	}
	return r.scoreCurrentFiles(ctx, s, cur)
}

func (r *Router) scoreCurrentFiles(ctx context.Context, s *routeState, cur itemFiles) (itemFiles, error) {
	if cur.n == 0 {
		return cur, nil
	}
	var err error
	if !s.scored {
		s.scored = true
		if s.scores, err = r.fileScores(ctx, s); err != nil {
			r.log.Warn("current files not scored under the running profile; their stored scores are compared",
				"version", s.req.Version, "title", s.req.TitleID, "err", err)
			s.scores = nil
		}
	}
	key := int64(0)
	if len(cur.episodes) > 0 {
		key = cur.episodes[0]
	}
	cur.file = s.files[key]
	if v, ok := s.scores[key]; ok {
		cur.score = &v
	}
	return cur, nil
}

// fileScores scores the version's current files under its running profile: by episode id for a series, under 0 for
// a movie's file. nil without the decision engine's profiles.
func (r *Router) fileScores(ctx context.Context, s *routeState) (map[int64]int64, error) {
	if r.profiles == nil && !s.vp.PreferSeaDex {
		return nil, nil
	}
	fl := format.Radarr
	if s.vp.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	var p *decide.Profile
	if r.profiles != nil {
		p = r.profiles(s.vp.Profile, fl)
	}
	if !s.vp.PreferSeaDex && (p == nil || p.Profile == nil) {
		return nil, nil // as before standing: nothing to score without the running profile
	}
	db := r.st.Writer()
	t, err := decide.LoadTitle(ctx, db, s.req.TitleID)
	if err != nil {
		return nil, err
	}
	v, err := decide.LoadVersion(ctx, db, r.pol, s.req.TitleID, s.req.Version)
	if err != nil {
		return nil, err
	}
	out := map[int64]int64{}
	s.files = map[int64]*decide.ExistingFile{}
	if s.vp.Kind != policy.KindSeries {
		x, err := decide.LoadExisting(ctx, db, v.ID)
		if err != nil || x == nil {
			return out, err
		}
		s.files[0] = x
		if p != nil {
			if err := decide.ScoreFileWith(p, &t, v, x); err != nil {
				return nil, err
			}
		}
		if x.Score != nil {
			out[0] = int64(*x.Score)
		}
		return out, nil
	}
	if err := decide.LoadSeriesVersion(ctx, db, &t, v.ID); err != nil {
		return nil, err
	}
	scored := map[*decide.ExistingFile]bool{}
	for _, ep := range t.Episodes {
		if ep.File == nil {
			continue
		}
		s.files[ep.ID] = ep.File
		if !scored[ep.File] && p != nil {
			scored[ep.File] = true
			if err := decide.ScoreFileWith(p, &t, v, ep.File); err != nil {
				return nil, err
			}
		}
		if ep.File.Score != nil {
			out[ep.ID] = int64(*ep.File.Score)
		}
	}
	return out, nil
}

// freshEpisodeFiles batches the episode intent and current-file reads for one title/version. Active links win
// over arr files; within each source the latest file wins, as currentFiles does. Only monitored, present episodes
// enter the snapshot, including those with no file. Scores are refreshed lazily from the running profile.
func (r *Router) freshEpisodeFiles(ctx context.Context, s *routeState) (map[int64]itemFiles, error) {
	rows, err := r.st.Writer().QueryContext(ctx, `WITH files AS (
  SELECT fe.episode_id, vf.quality, vf.quality_revision AS revision, 0 AS real, vf.custom_format_score AS score,
   COALESCE(vf.added_at, 0) AS at, 0 AS linked, vf.id
  FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
  LEFT JOIN version_source vs ON vs.version_id = vf.version_id
  WHERE vf.version_id = ?1 AND vf.title_id = ?2 AND (vs.instance IS NULL OR vs.instance = vf.instance)
  UNION ALL
  SELECT le.episode_id, COALESCE(l.quality, ''), COALESCE(json_extract(l.file_metadata, '$.Revision'), 0),
   COALESCE(json_extract(l.file_metadata, '$.Real'), 0), l.score, l.created_at, 1, l.id
  FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN link_episode le ON le.link_id = l.id
  WHERE lv.version_id = ?1 AND l.title_id = ?2 AND l.state = 'active'
 ), ranked AS (
  SELECT *, ROW_NUMBER() OVER (PARTITION BY episode_id ORDER BY linked DESC, at DESC, id DESC) AS rank FROM files
 ) SELECT e.id, f.quality, f.revision, f.real, f.score, f.at
 FROM episode e LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = ?1
 LEFT JOIN ranked f ON f.episode_id = e.id AND f.rank = 1
 WHERE e.title_id = ?2 AND e.removed_at IS NULL AND COALESCE(s.monitored, e.monitored) = 1`,
		s.req.VersionID, s.req.TitleID)
	if err != nil {
		return nil, fmt.Errorf("router: route episode snapshot: %w", err)
	}
	defer rows.Close()
	out := map[int64]itemFiles{}
	for rows.Next() {
		var id int64
		var quality sql.NullString
		var revision, real, score, at sql.NullInt64
		if err := rows.Scan(&id, &quality, &revision, &real, &score, &at); err != nil {
			return nil, err
		}
		var cur itemFiles
		if quality.Valid {
			cur = itemFiles{episodes: []int64{id}, quality: quality.String, revision: int(revision.Int64), real: int(real.Int64), at: at.Int64, n: 1}
			if score.Valid {
				cur.score = &score.Int64
			}
		}
		out[id] = cur
	}
	return out, rows.Err()
}
