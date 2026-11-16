package router

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// DecideFunc decides a release for a version (decide.Engine.Decide).
type DecideFunc func(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version,
	existing *decide.ExistingFile) (*decide.Decision, error)

// DecideSeriesFunc decides a series release for its mapped episodes and search criteria (decide.Engine.DecideSearch).
type DecideSeriesFunc func(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version,
	search *decide.SeriesSearch) (*decide.Decision, error)

// LinkStoredHashSQL is the torrent hash a link l records, lower case, or NULL: its content file's content, else its
// download's, the file metadata's InfoHash, the adopted source's (an adopted link). The web's linkHashSQL adds the
// shared-target fallback.
const LinkStoredHashSQL = `lower(COALESCE(
		(SELECT NULLIF(c_h.info_hash, '') FROM content_file cf_h JOIN content c_h ON c_h.id = cf_h.content_id
			WHERE cf_h.id = l.content_file_id),
		(SELECT NULLIF(d_h.info_hash, '') FROM download d_h WHERE d_h.id = l.download_id),
		CASE WHEN json_valid(l.file_metadata) THEN NULLIF(json_extract(l.file_metadata, '$.InfoHash'), '') END,
		CASE WHEN json_valid(l.adopted_source) THEN NULLIF(json_extract(l.adopted_source, '$.hash'), '') END))`

// OtherRelease is a release imported for one library, to be decided for another library of the same title
// (docs/design/router.md "Other libraries at import").
type OtherRelease struct {
	Indexer  string
	Title    string
	Hash     string
	Size     int64
	Episodes []int64 // a series download's episodes (the search criteria)
}

// DecideOther decides an imported release for version v of title t (loaded with its current files: LoadExisting for a
// movie, LoadSeriesVersion for a series) with v's own rules, as a search would: a series release is judged in TVDB
// numbering and in each scene numbering of its episodes, the best kept. Accepted, it returns the route candidate;
// otherwise nil and why (a release whose indexer is no configured one is not decided: the version's indexer rule
// cannot be applied).
func DecideOther(ctx context.Context, pol *policy.Policy, eng *decide.Engine, rel OtherRelease, t decide.Title,
	v decide.Version, existing *decide.ExistingFile, now time.Time) (*Candidate, string, error) {
	ix := pol.Indexers[rel.Indexer]
	if ix == nil {
		return nil, "its indexer is not a configured one", nil
	}
	r := indexer.Release{Indexer: rel.Indexer, Kind: ix.Kind, Privacy: ix.Privacy, Title: rel.Title, InfoHash: rel.Hash,
		Size: rel.Size, Seeders: -1, Peers: -1}
	var d *decide.Decision
	var crit []*decide.SeriesSearch
	var err error
	series := t.Kind == policy.KindSeries
	if series {
		ss := seriesSearchFor(&t, rel.Episodes)
		crit = plainCriteria(ss, criteriaQueries(&t, ss, now))
		d, err = decideBest(ctx, eng.DecideSearch, r, t, v, crit, ss)
	} else {
		d, err = eng.Decide(ctx, decide.Release{Release: r}, t, v, existing)
	}
	if err != nil {
		return nil, "", err
	}
	if !d.Accepted {
		why := make([]string, 0, len(d.Rejections))
		for _, rj := range d.Rejections {
			why = append(why, rj.String())
		}
		return nil, "rejected: " + strings.Join(why, ", "), nil
	}
	return &Candidate{Search: series, Criteria: crit, Release: r, Quality: d.Quality, Score: d.Score,
			Episodes: d.EpisodeIDs, Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping()}, "",
		nil
}

// ScreenOther runs on candidate c of library req.Version the checks a route of an automatic source makes before it
// asks any provider, with the same functions: a prefer-SeaDex library's refreshed SeaDex standing, freshCandidates (the title and the library monitored and not removed,
// a movie's minimum availability, each episode still monitored, the release improving the library's current files
// under its upgrade setting, prefer-SeaDex's same-torrent rule), screenUpgrades (upgrades: off never replaces a file)
// and the queue checks (movieInFlight, episodesInFlight). It returns the candidate as the route would keep it (a
// series' episodes narrowed to those still wanted), or nil and why. It writes nothing.
func ScreenOther(ctx context.Context, st *store.Store, pol *policy.Policy, profiles ProfileFunc, now func() time.Time,
	req Request) (*Candidate, string, error) {
	vp := pol.Versions[req.Version]
	if vp == nil || len(req.Candidates) != 1 {
		return nil, "not in the policy", nil
	}
	r := &Router{st: st, pol: pol, set: pol.RouterSettings(), log: slog.New(slog.DiscardHandler), now: now,
		profiles: profiles}
	// The route's own first step for a prefer-SeaDex library: the candidate's SeaDex standing from the stored
	// evidence (the context's), so the freshness check compares it as the decision did.
	req.Candidates = append([]Candidate(nil), req.Candidates...)
	if vp.PreferSeaDex {
		req.Candidates[0].Release = decide.RefreshSeaDexRelease(ctx, req.Candidates[0].Release)
	}
	s := &routeState{req: req, vp: vp, dry: true, res: &Result{Version: req.Version, Steps: []Step{}},
		pace: map[string]pace{}}
	why := func() string {
		if n := len(s.res.Steps); n > 0 {
			return s.res.Steps[n-1].Message
		}
		return "not wanted"
	}
	cands, err := r.freshCandidates(ctx, s)
	if err != nil || len(cands) == 0 {
		return nil, why(), err
	}
	if cands, err = r.screenUpgrades(ctx, s, cands); err != nil || len(cands) == 0 {
		return nil, why(), err
	}
	c := &cands[0]
	if vp.Kind == policy.KindSeries {
		if id, err := r.episodesInFlight(ctx, s.req, c, true); err != nil || id != 0 {
			return nil, fmt.Sprintf("download %d holds overlapping episodes already", id), err
		}
	} else if id, msg, err := r.movieInFlight(ctx, s, c); err != nil || id != 0 {
		return nil, fmt.Sprintf("download %d holds another release of the movie: %s", id, msg), err
	}
	return c, "", nil
}

// Unstartable is why a grab by hand would open no download: Code is the step code the route would record
// (blocklisted, in_flight, upgrades_off, client_indexer, not_debrid) and Message its words; for in_flight, Download
// and Release name the download that holds the release or its episodes already.
type Unstartable struct {
	Code, Message string
	Download      int64
	Release       string
}

// ScreenManual runs on the one candidate of a grab by hand (req) the route's own screens that open no download,
// with the same functions, so the grab can say why before it is queued: the version not routed (notRouted), upgrades
// off with a file there (screenUpgrades), a series' episodes held by another download (episodesInFlight), the
// blocklist (blocklisted), the same hash in flight (inFlight; a stranded queued row is resumed: strandedQueued) and,
// for a client-routed version, an indexer that keeps its releases on providers (clientIndexer). A movie grab by hand is not held back by another release in flight, as
// in the route. nil when the route would try it. It writes nothing.
func ScreenManual(ctx context.Context, st *store.Store, pol *policy.Policy, profiles ProfileFunc, now func() time.Time,
	req Request) (*Unstartable, error) {
	vp := pol.Versions[req.Version]
	if vp == nil || len(req.Candidates) != 1 {
		return nil, nil
	}
	if why := notRouted(vp); why != "" {
		return &Unstartable{Code: codeNotDebrid, Message: why}, nil
	}
	r := &Router{st: st, pol: pol, set: pol.RouterSettings(), log: slog.New(slog.DiscardHandler), now: now,
		profiles: profiles}
	req.DownloadID = 0
	s := &routeState{req: req, vp: vp, res: &Result{Version: req.Version, Steps: []Step{}}, pace: map[string]pace{}}
	cands, err := r.screenUpgrades(ctx, s, req.Candidates)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return &Unstartable{Code: codeUpgradesOff, Message: s.res.Steps[len(s.res.Steps)-1].Message}, nil
	}
	c := &cands[0]
	hash := indexer.NormalizeHash(c.Release.InfoHash)
	held := func(id int64) (*Unstartable, error) {
		u := &Unstartable{Code: "in_flight", Download: id, Message: fmt.Sprintf("download %d holds it already", id)}
		err := st.Reader().QueryRowContext(ctx, `SELECT release_title FROM download WHERE id = ?`, id).Scan(&u.Release)
		return u, err
	}
	if vp.Kind == policy.KindSeries {
		if id, err := r.episodesInFlight(ctx, s.req, c, false); err != nil || id != 0 {
			if err != nil {
				return nil, err
			}
			return held(id)
		}
	}
	if why, err := r.blocklisted(ctx, req.Version, hash, c.Release.Title); err != nil || why != "" {
		if err != nil {
			return nil, err
		}
		return &Unstartable{Code: "blocklisted", Message: why}, nil
	}
	if id, ok, err := r.inFlight(ctx, s.req, hash); err != nil {
		return nil, err
	} else if ok {
		// A queued row no route job will pick up is resumed by the route (candidate), not reported in flight.
		if stranded, err := r.strandedQueued(ctx, id); err != nil {
			return nil, err
		} else if !stranded {
			return held(id)
		}
	}
	if vp.ViaClient() {
		if code, msg := r.clientIndexer(vp, &c.Release); code != "" {
			return &Unstartable{Code: code, Message: msg}, nil
		}
	}
	return nil, nil
}

// HoldsHash reports whether a version already has the hash: an active link of ours, or a download of it that did not
// fail.
func HoldsHash(ctx context.Context, db *sql.DB, versionID int64, hash string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = ? AND l.state = 'active'
			AND `+LinkStoredHashSQL+` = ?)
		+ (SELECT COUNT(*) FROM download WHERE version_id = ? AND lower(info_hash) = ? AND state <> 'failed')`,
		versionID, hash, versionID, hash).Scan(&n)
	return n > 0, err
}

// Blocklisted reports whether version must not route hash (router_blocklist, by hash or release title key).
func Blocklisted(ctx context.Context, db *sql.DB, version, hash, release string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM router_blocklist WHERE version_key = ? AND info_hash IN (?, ?)`,
		version, BlocklistKey(hash, ""), BlocklistKey("", release)).Scan(&n)
	return n > 0, err
}

// BlockedSet is a version's blocklist keys (router_blocklist.info_hash), read once for many releases.
type BlockedSet map[string]bool

// Has is Blocklisted for one release of the set's version.
func (b BlockedSet) Has(hash, release string) bool {
	return b[BlocklistKey(hash, "")] || b[BlocklistKey("", release)]
}

// VersionBlocklist reads version's blocklist keys in one query (Blocklisted for many releases).
func VersionBlocklist(ctx context.Context, db *sql.DB, version string) (BlockedSet, error) {
	rows, err := db.QueryContext(ctx, `SELECT info_hash FROM router_blocklist WHERE version_key = ?`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := BlockedSet{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// seriesSearchFor is the search for a release's episodes, in TVDB numbering (the scene criteria are derived from it).
func seriesSearchFor(t *decide.Title, ids []int64) *decide.SeriesSearch {
	if len(ids) == 0 {
		return nil
	}
	s := &decide.SeriesSearch{Episodes: append([]int64(nil), ids...), MonitoredOnly: true}
	for i := range t.Episodes {
		if t.Episodes[i].ID == ids[0] {
			s.Season, s.Episode = t.Episodes[i].Season, t.Episodes[i].Number
			break
		}
	}
	multi := len(ids) > 1
	switch {
	case t.SeriesType == "anime" && multi:
		s.Kind = profile.SearchAnimeSeason
	case t.SeriesType == "anime":
		s.Kind = profile.SearchAnimeEpisode
	case t.SeriesType == "daily" && !multi:
		s.Kind = profile.SearchDaily
	case t.SeriesType == "daily":
		s.Kind = profile.SearchDailySeason
	case multi:
		s.Kind = profile.SearchSeason
	default:
		s.Kind = profile.SearchSingleEpisode
	}
	return s
}
