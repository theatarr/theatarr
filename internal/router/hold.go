package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Upgrade approval (docs/design/router.md "Upgrade approval", migration 00037). versions[*].upgrades says what a route
// does with a release that would replace a file the version already has for the release's items (the movie, or any
// of the release's episodes):
//   - auto: nothing here; the release is routed like any other.
//   - off: the release is skipped (upgrades_off). A filled item keeps its file, whatever is found.
//   - approve: the release is held in upgrade_hold instead of being added: one pending hold per version, title and
//     episode set, replaced by a better release (quality rank in the version's profile, then score). A release that
//     cannot be placed anyway (no hash, blocklisted) is never held, and a declined hash is never held again.
//     Holding asks no provider anything. Approving a hold enqueues a route of its release with Request.Hold set,
//     which the route does not hold again; every other rule of the route applies (the gate, the blocklist,
//     pacing, the gateway's budgets).
//
// Releases for items with no file are never held: in a series route a pack that replaces some files is held while a
// release that only fills missing episodes is tried as usual. A dry run (shadow version, kill switch off) records
// would_hold steps in its plan and writes no hold.

// Step codes of upgrade approval.
const (
	codeUpgradeHeld     = "upgrade_held"     // held for approval (would_hold in a dry run)
	codeUpgradesOff     = "upgrades_off"     // versions[*].upgrades: off
	codeUpgradeDeclined = "upgrade_declined" // a manager declined this release for the version and title
	codeUpgradeWaiting  = "upgrade_waiting"  // a better release is held for the same items
)

// Hold states (upgrade_hold.state).
const (
	HoldPending  = "pending"
	HoldApproved = "approved"
	HoldDeclined = "declined"
	HoldExpired  = "expired"
	HoldReplaced = "replaced"
)

// HoldTTL is how long a pending hold lives without a search or RSS read offering its release again: past it the
// release counts as gone.
const HoldTTL = 14 * 24 * time.Hour

// itemFiles is what a version holds for a release's items.
type itemFiles struct {
	file *decide.ExistingFile
	// episodes with a file (empty for a movie); quality and score of the first file; at the newest file's time.
	episodes []int64
	quality  string
	revision int
	real     int
	score    *int64
	at       int64
	n        int
}

// currentFiles reads the version's current files for the items: active links, then the arr's files of the
// instance that owns the version (as the title page reads them). episodes empty means every file of the title.
func currentFiles(ctx context.Context, db *sql.DB, titleID, versionID int64, episodes []int64) (itemFiles, error) {
	type file struct {
		quality  string
		revision int
		real     int
		score    *int64
		at       int64
	}
	byEp := map[int64]file{}
	add := func(eid int64, f file, win bool) {
		if len(episodes) > 0 && !slices.Contains(episodes, eid) {
			return
		}
		if old, ok := byEp[eid]; ok && !win && old.at >= f.at {
			return
		}
		byEp[eid] = f
	}
	scan := func(q string, win bool) error {
		rows, err := db.QueryContext(ctx, q, versionID, titleID)
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				return nil
			}
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var eid int64
			var f file
			var score sql.NullInt64
			if err := rows.Scan(&eid, &f.quality, &f.revision, &f.real, &score, &f.at); err != nil {
				return err
			}
			if score.Valid {
				v := score.Int64
				f.score = &v
			}
			add(eid, f, win)
		}
		return rows.Err()
	}
	if err := scan(`SELECT COALESCE(fe.episode_id, 0), vf.quality, vf.quality_revision, 0, vf.custom_format_score,
		COALESCE(vf.added_at, 0)
		FROM version_file vf LEFT JOIN version_file_episode fe ON fe.version_file_id = vf.id
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE vf.version_id = ? AND vf.title_id = ? AND (vs.instance IS NULL OR vs.instance = vf.instance)`, false); err != nil {
		return itemFiles{}, fmt.Errorf("router: current files: %w", err)
	}
	if err := scan(`SELECT COALESCE(le.episode_id, 0), COALESCE(l.quality, ''),
		COALESCE(json_extract(l.file_metadata, '$.Revision'), 0), COALESCE(json_extract(l.file_metadata, '$.Real'), 0),
		l.score, l.created_at
		FROM link l JOIN link_version lv ON lv.link_id = l.id LEFT JOIN link_episode le ON le.link_id = l.id
		WHERE lv.version_id = ? AND l.title_id = ? AND l.state = 'active'`, true); err != nil {
		return itemFiles{}, fmt.Errorf("router: current links: %w", err)
	}
	var out itemFiles
	ids := make([]int64, 0, len(byEp))
	for id := range byEp {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for i, id := range ids {
		f := byEp[id]
		if i == 0 {
			out.quality, out.revision, out.real, out.score = f.quality, f.revision, f.real, f.score
		}
		out.at = max(out.at, f.at)
		if id != 0 {
			out.episodes = append(out.episodes, id)
		}
	}
	out.n = len(ids)
	return out, nil
}

// episodesKey is a hold's episode set: the sorted ids joined by commas, "" for a movie.
func episodesKey(eps []int64) string {
	s := slices.Clone(eps)
	slices.Sort(s)
	s = slices.Compact(s)
	parts := make([]string, len(s))
	for i, e := range s {
		parts[i] = strconv.FormatInt(e, 10)
	}
	return strings.Join(parts, ",")
}

func parseEpisodesKey(k string) []int64 {
	var out []int64
	for p := range strings.SplitSeq(k, ",") {
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// better reports whether candidate a beats a held release b: a higher quality rank in the version's profile (when
// both are known), then a higher score.
func better(aRank, aScore, bRank, bScore int) bool {
	if aRank > 0 && bRank > 0 && aRank != bRank {
		return aRank > bRank
	}
	return aScore > bScore
}

// screenUpgrades applies versions[*].upgrades before any candidate is tried: it returns the candidates the route
// may try, and records a step for each one it kept back. Under approve the best release of each episode set is
// held (written only when the route is not dry; s.res.Hold names it).
func (r *Router) screenUpgrades(ctx context.Context, s *routeState, cands []Candidate) ([]Candidate, error) {
	rule := s.vp.UpgradeRule()
	if rule == policy.UpgradesAuto || (rule == policy.UpgradesApprove && s.req.Hold != 0) {
		return cands, nil
	}
	var out []Candidate
	taken := map[string]bool{} // episode sets this route held a release for (or found held)
	for i := range cands {
		c := &cands[i]
		rel := &c.Release
		hash := indexer.NormalizeHash(rel.InfoHash)
		step := func(st Step) {
			st.Release, st.Hash = rel.Title, hash
			s.res.Steps = append(s.res.Steps, st)
		}
		cur, err := r.currentFilesNow(ctx, s, c.Episodes)
		if err != nil {
			return nil, err
		}
		if cur.n == 0 {
			out = append(out, *c)
			continue
		}
		what := fileWords(cur)
		if rule == policy.UpgradesOff {
			step(Step{Outcome: "skipped", Code: codeUpgradesOff, Message: fmt.Sprintf("versions.%s.upgrades is off: "+
				"it would replace %s", s.req.Version, what)})
			continue
		}
		if _, skip, ok, err := r.preflight(ctx, s.req.Version, rel); err != nil {
			return nil, err
		} else if !ok {
			step(skip)
			continue
		}
		declined, err := r.declined(ctx, s.req.VersionID, s.req.TitleID, hash)
		if err != nil {
			return nil, err
		}
		if declined != "" {
			step(Step{Outcome: "skipped", Code: codeUpgradeDeclined, Message: declined})
			continue
		}
		key := episodesKey(c.Episodes)
		if taken[key] {
			step(Step{Outcome: "skipped", Code: codeUpgradeWaiting, Message: "a better release for the same " +
				"items is held for approval"})
			continue
		}
		taken[key] = true
		st, id, err := r.hold(ctx, s, c, hash, key, cur, what)
		if err != nil {
			return nil, err
		}
		step(st)
		if id != 0 && s.res.Hold == 0 {
			s.res.Hold = id
		}
		if st.Code == codeUpgradeHeld && s.res.Release == "" {
			s.res.Release, s.res.Hash, s.res.Indexer = rel.Title, hash, rel.Indexer
		}
	}
	return out, nil
}

// fileWords names the files a release would replace: "WEBDL-2160p (score 1400)", "3 episode files (WEBDL-1080p)".
func fileWords(cur itemFiles) string {
	q := cur.quality
	if q == "" {
		q = "unknown quality"
	}
	if cur.score != nil {
		q += fmt.Sprintf(", score %d", *cur.score)
	}
	if len(cur.episodes) > 1 {
		return fmt.Sprintf("%d episode files (%s)", len(cur.episodes), q)
	}
	return "the file (" + q + ")"
}

// declined says why a hash is never held again for the version and title ("" when it may be).
func (r *Router) declined(ctx context.Context, versionID, titleID int64, hash string) (string, error) {
	var at int64
	var note string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT COALESCE(decided_at, 0), note FROM upgrade_hold
		WHERE version_id = ? AND title_id = ? AND info_hash = ? AND state = 'declined' ORDER BY id DESC LIMIT 1`,
		versionID, titleID, hash).Scan(&at, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("router: declined holds: %w", err)
	}
	why := "a manager declined this upgrade on " + store.FromMillis(at).Format("2006-01-02")
	if note != "" {
		why += ": " + note
	}
	return why, nil
}

// hold records (or, dry, describes) the pending hold of candidate c for its episode set. A pending hold of the same
// release is refreshed (seen_at); a worse one is replaced; a better one stays and c waits behind it. A pending hold
// whose file changed since it was held is stale and expires first.
func (r *Router) hold(ctx context.Context, s *routeState, c *Candidate, hash, key string, cur itemFiles,
	what string) (Step, int64, error) {
	now := store.Millis(r.now())
	msg := fmt.Sprintf("it would replace %s with %s (score %d); held until a manager approves it", what, c.Quality,
		c.Score)
	if s.dry {
		return Step{Outcome: "would_hold", Code: codeUpgradeHeld, Message: "versions." + s.req.Version +
			".upgrades is approve: " + strings.Replace(msg, "held until", "would be held until", 1)}, 0, nil
	}
	var st Step
	var id int64
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var old struct {
			id, rank, score, curAt   int64
			hash, release, candidate string
		}
		err := tx.QueryRowContext(ctx, `SELECT id, info_hash, release_title, quality_rank, score, current_at, candidate
			FROM upgrade_hold WHERE version_id = ? AND title_id = ? AND episodes = ? AND state = 'pending'`,
			s.req.VersionID, s.req.TitleID, key).Scan(&old.id, &old.hash, &old.release, &old.rank, &old.score, &old.curAt, &old.candidate)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			old.id = 0
		case err != nil:
			return err
		}
		if old.id != 0 && old.curAt != cur.at {
			if _, err := tx.ExecContext(ctx, `UPDATE upgrade_hold SET state = 'expired', decided_at = ?,
				note = 'a newer file arrived after it was held' WHERE id = ?`, now, old.id); err != nil {
				return err
			}
			old.id = 0
		}
		cj, err := json.Marshal(c)
		if err != nil {
			return err
		}
		switch {
		case old.id != 0 && old.hash == hash:
			id = old.id
			st = Step{Outcome: "held", Code: codeUpgradeHeld, Message: "still waiting for approval: " + msg}
			_, err = tx.ExecContext(ctx, `UPDATE upgrade_hold SET seen_at = ?, candidate = ?, score = ?,
				quality_rank = ?, reason = ? WHERE id = ?`, now, string(cj), c.Score, c.QualityRank, c.UpgradeReason, id)
			return err
		case old.id != 0 && !betterHeld(ctx, s.vp, *c, old.candidate, int(old.rank), int(old.score)):
			id = old.id
			st = Step{Outcome: "skipped", Code: codeUpgradeWaiting, Message: "a release at least as good is held " +
				"for approval: " + old.release}
			return nil
		case old.id != 0:
			if _, err := tx.ExecContext(ctx, `UPDATE upgrade_hold SET state = 'replaced', decided_at = ?,
				note = ? WHERE id = ?`, now, "replaced by a better release: "+c.Release.Title, old.id); err != nil {
				return err
			}
		}
		rep, _ := json.Marshal(nonNil(cur.episodes))
		var curScore any
		if cur.score != nil {
			curScore = *cur.score
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO upgrade_hold (title_id, version_id, version_key, episodes,
			info_hash, release_title, indexer, quality, score, quality_rank, reason, current_quality, current_score,
			current_at, replaces, candidate, source, policy_rev, state, created_at, seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
			s.req.TitleID, s.req.VersionID, s.req.Version, key, hash, c.Release.Title, c.Release.Indexer, c.Quality,
			c.Score, c.QualityRank, c.UpgradeReason, cur.quality, curScore, cur.at, string(rep), string(cj),
			s.req.Source, r.pol.Revision, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		st = Step{Outcome: "held", Code: codeUpgradeHeld, Message: "versions." + s.req.Version +
			".upgrades is approve: " + msg}
		return nil
	})
	if err != nil {
		return Step{}, 0, fmt.Errorf("router: upgrade hold: %w", err)
	}
	if st.Code == codeUpgradeHeld {
		r.log.Info("upgrade held for approval", "version", s.req.Version, "title", s.req.TitleID,
			"release", c.Release.Title, "hash", hash, "hold", id, "replaces", what)
	}
	return st, id, nil
}

// noteHoldRoute records on an approved hold what its route did.
func (r *Router) noteHoldRoute(ctx context.Context, id int64, res *Result) {
	var dl any
	if res.DownloadID != 0 {
		dl = res.DownloadID
	}
	outcome := string(res.Outcome)
	if res.Mode != ModeActive {
		outcome = "dry run (" + string(res.Mode) + "): " + outcome
	}
	if _, err := r.st.Writer().ExecContext(ctx, `UPDATE upgrade_hold SET download_id = COALESCE(?, download_id),
		route_outcome = ? WHERE id = ?`, dl, outcome, id); err != nil {
		r.log.Warn("upgrade hold: recording the route", "hold", id, "err", err)
	}
}

// ExpireHolds ends the pending holds that no longer stand: the version's rule is no longer approve, the release was
// blocklisted for the version, no search or RSS read offered it again within HoldTTL (it is gone), or the file it
// would replace changed (a newer one arrived, or it is gone: the next search decides again). It returns how many
// expired. Poll runs it.
func ExpireHolds(ctx context.Context, st *store.Store, pol *policy.Policy, now time.Time) (int, error) {
	var rows []pendingHold
	rs, err := st.Writer().QueryContext(ctx, `SELECT id, title_id, version_id, version_key, episodes, info_hash, release_title,
		seen_at, current_at FROM upgrade_hold WHERE state = 'pending'`)
	if err != nil {
		return 0, fmt.Errorf("router: pending holds: %w", err)
	}
	for rs.Next() {
		var x pendingHold
		if err := rs.Scan(&x.id, &x.titleID, &x.versionID, &x.version, &x.episodes, &x.hash, &x.release, &x.seenAt,
			&x.curAt); err != nil {
			rs.Close()
			return 0, err
		}
		rows = append(rows, x)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, x := range rows {
		why, err := holdStale(ctx, st.Writer(), pol, x, now, false)
		if err != nil {
			return n, err
		}
		if why == "" {
			continue
		}
		ok, err := expireHold(ctx, st.Writer(), x.id, why, now)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// pendingHold is what the expiry checks read of a pending hold.
type pendingHold struct {
	id, titleID, versionID, seenAt, curAt int64
	version, episodes, hash, release      string
}

// holdStale says why a pending hold no longer stands ("" when it does): ExpireHolds's rules. approving is Approve's
// check: a file that is gone does not end the hold there (the approved release fills the item; nothing better is
// replaced).
func holdStale(ctx context.Context, db *sql.DB, pol *policy.Policy, x pendingHold, now time.Time, approving bool) (
	string, error) {
	vp := pol.Versions[x.version]
	if vp == nil || vp.UpgradeRule() != policy.UpgradesApprove {
		return "versions." + x.version + ".upgrades is no longer approve", nil
	}
	var reason string
	err := db.QueryRowContext(ctx, `SELECT reason FROM router_blocklist WHERE version_key = ?
		AND info_hash IN (?, ?) ORDER BY created_at DESC LIMIT 1`, x.version, x.hash, BlocklistKey("", x.release)).Scan(&reason)
	switch {
	case err == nil:
		return "the release was blocklisted for the version: " + reason, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("router: blocklist: %w", err)
	}
	if store.FromMillis(x.seenAt).Add(HoldTTL).Before(now) {
		return fmt.Sprintf("no search or RSS read offered the release again for %d days", int(HoldTTL.Hours()/24)), nil
	}
	cur, err := currentFiles(ctx, db, x.titleID, x.versionID, parseEpisodesKey(x.episodes))
	if err != nil {
		return "", err
	}
	switch {
	case cur.n == 0 && approving:
	case cur.n == 0:
		return "the file it would replace is gone; the next search decides again", nil
	case cur.at != x.curAt:
		return "a newer file arrived (" + cur.quality + "); the next search decides again", nil
	}
	return "", nil
}

// expireHold ends pending hold id with why; it reports whether it did.
func expireHold(ctx context.Context, db *sql.DB, id int64, why string, now time.Time) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE upgrade_hold SET state = 'expired', decided_at = ?, note = ?
		WHERE id = ? AND state = 'pending'`, store.Millis(now), why, id)
	if err != nil {
		return false, fmt.Errorf("router: expiring hold %d: %w", id, err)
	}
	k, _ := res.RowsAffected()
	return k == 1, nil
}

// Errors of approving and declining a hold.
var (
	ErrHoldNotFound   = errors.New("router: no such upgrade hold")
	ErrHoldNotPending = errors.New("router: the upgrade hold is not pending")
)

// HoldRefused is an approval the policy does not allow now (the version's rule is off, the router may not write for
// it, or the release cannot go to debrid): the hold stays pending. Or the hold no longer stands: it expired.
type HoldRefused struct{ Reason string }

func (e *HoldRefused) Error() string { return "router: " + e.Reason }

// Decider is who approves or declines a hold: the account, and the API key's name or "session".
type Decider struct {
	UserID int64
	Via    string
}

// Holds approves and declines upgrade holds for the web layer. It holds no provider client: an approval enqueues a
// route job (router.route) with Request.Hold set, and that route applies every rule.
type Holds struct {
	Store  *store.Store
	Policy *policy.Policy
	Queue  *jobs.Queue
	Now    func() time.Time
}

func (h *Holds) now() time.Time {
	if h.Now == nil {
		return time.Now()
	}
	return h.Now()
}

// holdGate is Router.Gate for a policy, with the hold's own rule: approving needs the version's rule to allow upgrades,
// router.enabled, and the version active with grab on.
func holdGate(pol *policy.Policy, version string, c *Candidate) error {
	vp := pol.Versions[version]
	switch {
	case vp == nil:
		return &HoldRefused{Reason: "version " + version + " is no longer in the policy"}
	case vp.UpgradeRule() == policy.UpgradesOff:
		return &HoldRefused{Reason: "versions." + version + ".upgrades is off: nothing replaces its files"}
	case !pol.RouterSettings().Enabled:
		return &HoldRefused{Reason: "router.enabled is false: nothing is added to any provider; the hold stays pending"}
	case vp.Mode != policy.ModeActive:
		return &HoldRefused{Reason: "version " + version + " is Watch only: nothing is added for it; the hold " +
			"stays pending"}
	case !vp.Grab:
		return &HoldRefused{Reason: "versions." + version + ".grab is off: nothing is added for it; the hold stays " +
			"pending"}
	}
	return nil
}

// Approve approves a pending hold and enqueues the route of its release, in one transaction. The route job places
// it as any route would; the library then replaces the file as for any upgrade. A hold that no longer stands (the
// poller's expiry rules: past HoldTTL, a newer file arrived, blocklisted, the rule changed) is expired
// here and refused, even before the next poll pass gets to it: the approved route skips the freshness check, so a
// stale hold must not replace a better file.
func (h *Holds) Approve(ctx context.Context, id int64, by Decider) error {
	x := pendingHold{id: id}
	var state, cj string
	err := h.Store.Writer().QueryRowContext(ctx, `SELECT title_id, version_id, version_key, episodes, info_hash,
		release_title, seen_at, current_at, state, candidate FROM upgrade_hold WHERE id = ?`, id).Scan(&x.titleID,
		&x.versionID, &x.version, &x.episodes, &x.hash, &x.release, &x.seenAt, &x.curAt, &state, &cj)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrHoldNotFound
	}
	if err != nil {
		return err
	}
	if state != HoldPending {
		return ErrHoldNotPending
	}
	var hc Candidate
	if err := json.Unmarshal([]byte(cj), &hc); err != nil {
		return fmt.Errorf("router: hold %d candidate: %w", id, err)
	}
	// What the policy refuses now leaves the hold pending; only then is it checked for staleness.
	if err := holdGate(h.Policy, x.version, &hc); err != nil {
		return err
	}
	why, err := holdStale(ctx, h.Store.Writer(), h.Policy, x, h.now(), true)
	if err != nil {
		return err
	}
	if why != "" {
		if ok, err := expireHold(ctx, h.Store.Writer(), id, why, h.now()); err != nil {
			return err
		} else if !ok {
			return ErrHoldNotPending
		}
		return &HoldRefused{Reason: "the upgrade no longer stands and expired: " + why}
	}
	now := store.Millis(h.now())
	return h.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		var titleID, versionID int64
		var version, state, cj string
		err := tx.QueryRowContext(ctx, `SELECT title_id, version_id, version_key, state, candidate FROM upgrade_hold
			WHERE id = ?`, id).Scan(&titleID, &versionID, &version, &state, &cj)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrHoldNotFound
		}
		if err != nil {
			return err
		}
		if state != HoldPending {
			return ErrHoldNotPending
		}
		var c Candidate
		if err := json.Unmarshal([]byte(cj), &c); err != nil {
			return fmt.Errorf("router: hold %d candidate: %w", id, err)
		}
		if err := holdGate(h.Policy, version, &c); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE upgrade_hold SET state = 'approved', decided_at = ?, decided_by = ?,
			decided_via = ? WHERE id = ? AND state = 'pending'`, now, nullID(by.UserID), by.Via, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrHoldNotPending
		}
		req := Request{TitleID: titleID, VersionID: versionID, Version: version, Candidates: []Candidate{c},
			Source: "upgrade:" + strconv.FormatInt(id, 10), Hold: id, DecisionRev: h.Policy.Revision}
		p := routePayload{Request: req}
		_, _, err = enqueueRoute(ctx, h.Queue, tx, p, time.Time{})
		return err
	})
}

// Decline declines a pending hold with an optional note; its release is never held again for the version and title.
func (h *Holds) Decline(ctx context.Context, id int64, by Decider, note string) error {
	now := store.Millis(h.now())
	return h.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM upgrade_hold WHERE id = ?`, id).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrHoldNotFound
		}
		if err != nil {
			return err
		}
		if state != HoldPending {
			return ErrHoldNotPending
		}
		_, err = tx.ExecContext(ctx, `UPDATE upgrade_hold SET state = 'declined', decided_at = ?, decided_by = ?,
			decided_via = ?, note = ? WHERE id = ? AND state = 'pending'`, now, nullID(by.UserID), by.Via,
			strings.TrimSpace(note), id)
		return err
	})
}

func betterHeld(ctx context.Context, vp *policy.VersionPolicy, c Candidate, saved string, rank, score int) bool {
	var old Candidate
	if json.Unmarshal([]byte(saved), &old) == nil && vp.PreferSeaDex {
		old.Release = decide.RefreshSeaDexRelease(ctx, old.Release)
		if order, ok := profile.SeaDexOrder(true, indexer.SeaDexPreference(&c.Release), indexer.SeaDexPreference(&old.Release)); ok {
			return order > 0
		}
	}
	return better(c.QualityRank, c.Score, rank, score)
}
