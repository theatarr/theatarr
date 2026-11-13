package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// The queue check for movies: Radarr's QueueSpecification (src/NzbDrone.Core/DecisionEngine/Specifications/
// QueueSpecification.cs @ a96bf7c), at route time. A movie version gets no second release while another hash of the
// same title and version is in flight (inFlightStates: queued for a provider, adding, on the provider, ready or
// importing), unless the new release is an upgrade over the queued one by the version's profile:
//
//   - the queued release already meets the cutoff (quality and format score): refused;
//   - the new release is not an upgrade of the queued one (UpgradableSpecification.IsUpgradable): refused;
//   - it is an upgrade the profile does not allow (IsUpgradeAllowed), or versions.<key>.upgrades is off: refused.
//
// Every automatic source is checked (RSS, searches, re-searches, offers, release pushes, deferred jobs); a person's
// grab (manualSource) is not, as Radarr lets an interactive grab through. A release that cannot be compared (no
// profile, an unknown quality) is refused: the check fails closed. Series keep their episode overlap check
// (episodesInFlight).

// manualSource reports whether a route is a person's explicit grab: the release panel or an arr client's release
// grab, a retry by hand, or an upgrade approved on a build before 2026-11.
func manualSource(source string) bool {
	for _, p := range []string{"ui:grab:", "ui:retry:", "arrview:release:", "upgrade:"} {
		if strings.HasPrefix(source, p) {
			return true
		}
	}
	return false
}

// movieInFlight returns a download of another release of the movie in flight for the version that c does not
// upgrade, and why; 0 when there is none. The same hash is candidate's (inFlight and its reconciliation), in a dry
// run as in an active one.
func (r *Router) movieInFlight(ctx context.Context, s *routeState, c *Candidate) (int64, string, error) {
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT id, quality, score, release_title, COALESCE(release_data, '')
		FROM download WHERE title_id = ? AND version_id = ? AND state IN `+inFlightStates+`
		AND info_hash <> ? AND NOT (id = ? AND state = 'queued') ORDER BY id`,
		s.req.TitleID, s.req.VersionID, indexer.NormalizeHash(c.Release.InfoHash), s.req.DownloadID)
	if err != nil {
		return 0, "", fmt.Errorf("router: movie in-flight check: %w", err)
	}
	type queued struct {
		id      int64
		quality string
		score   int
		rel     indexer.Release
	}
	var qs []queued
	for rows.Next() {
		var q queued
		var data string
		if err := rows.Scan(&q.id, &q.quality, &q.score, &q.rel.Title, &data); err != nil {
			rows.Close()
			return 0, "", err
		}
		if data != "" {
			title := q.rel.Title
			if json.Unmarshal([]byte(data), &q.rel) != nil || q.rel.Title == "" {
				q.rel = indexer.Release{Title: title}
			}
		}
		qs = append(qs, q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, "", err
	}
	if len(qs) == 0 {
		return 0, "", nil
	}
	if !s.profiled {
		s.profile, s.profileErr = r.routeProfile(s.vp)
		s.profiled = true
	}
	for _, q := range qs {
		if why := queueRefusal(s, c, q.quality, q.score, decide.RefreshSeaDexRelease(ctx, q.rel)); why != "" {
			return q.id, why, nil
		}
	}
	return 0, "", nil
}

// queueRefusal says why c is no upgrade over a queued release of the movie ("" when it is one).
func queueRefusal(s *routeState, c *Candidate, quality string, score int, rel indexer.Release) string {
	if s.profileErr != nil || s.profile == nil {
		return fmt.Sprintf("the releases cannot be compared (%v)", s.profileErr)
	}
	if s.vp.UpgradeRule() == policy.UpgradesOff {
		return "versions." + s.req.Version + ".upgrades is off"
	}
	p := s.profile
	newQ, ok := profile.QualityByName(p.quality.Flavour, c.Quality)
	if !ok {
		return fmt.Sprintf("the release's quality %q is unknown", c.Quality)
	}
	curQ, ok := profile.QualityByName(p.quality.Flavour, quality)
	if !ok {
		return fmt.Sprintf("the queued release's quality %q is unknown", quality)
	}
	model := func(q profile.Quality, r indexer.Release, name string) profile.QualityModel {
		f := decide.LinkedFile(s.vp.Kind, decide.Release{Release: r}, name)
		return profile.QualityModel{Quality: q, SeaDexPreference: f.SeaDexPreference, Revision: profile.Revision{Version: max(f.Revision, 1), Real: f.Real}}
	}
	x := newRouteComparison(p, s.vp, model(curQ, rel, quality), score, model(newQ, c.Release, c.Quality), c.Score)
	if !x.engine.CutoffNotMet(x.profile, x.cur, x.curF, &x.new) {
		return "the queued release already meets the cutoff"
	}
	if why := x.upgradable(); why != profile.UpgradeNone {
		return "the queued release is of equal or higher preference (" + why.String() + ")"
	}
	if !x.engine.IsUpgradeAllowed(x.profile, x.cur, x.curF, x.new, x.newF) {
		return "another release is queued and the profile does not allow upgrades"
	}
	return ""
}

// routeComparison compares a candidate with a held or queued release the way the decision engine does, from what a
// route keeps of both: their qualities, revisions and SeaDex standing, and their two format scores, which stand in as
// two formats (format 1 the current release's score, format 2 the candidate's). The freshness and queue checks
// share it and keep their own rules around it (profile.SeaDexOrder inside the engine compares standing).
type routeComparison struct {
	engine     *profile.Engine
	profile    *profile.Profile
	cur, new   profile.QualityModel
	curF, newF []*format.Format
}

func newRouteComparison(p *routeProfile, vp *policy.VersionPolicy, cur profile.QualityModel, curScore int,
	nw profile.QualityModel, newScore int) routeComparison {
	qp := *p.quality
	fp := qp.Profile
	fp.FormatItems = []format.FormatItem{{Format: 1, Score: curScore}, {Format: 2, Score: newScore}}
	qp.Profile = fp
	return routeComparison{engine: &profile.Engine{Flavour: qp.Flavour,
		Config: profile.Config{DownloadPropersAndRepacks: p.propers, PreferSeaDex: vp.PreferSeaDex}},
		profile: &qp, cur: cur, new: nw, curF: []*format.Format{{ID: 1}}, newF: []*format.Format{{ID: 2}}}
}

func (x routeComparison) upgradable() profile.UpgradeableRejectReason {
	return x.engine.IsUpgradable(x.profile, x.cur, x.curF, x.new, x.newF)
}
