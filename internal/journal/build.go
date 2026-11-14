package journal

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

	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// builder builds entries for one request, caching titles and checks' context.
type builder struct {
	j *Journal
	// pol is the policy in force when the build started (Journal.policy).
	pol    *policy.Policy
	ctx    context.Context
	db     *sql.DB
	own    bool
	titles map[int64]*titleInfo
	// firstDeads are the journal ids of every dead-link finding's first action (deadlink.go), read once.
	firstDeads map[string]bool
}

func (j *Journal) newBuilder(ctx context.Context) *builder {
	return &builder{j: j, pol: j.policy(ctx), ctx: ctx, db: j.st.Reader(), own: j.hasOwnTables(ctx), titles: j.titleCache()}
}

// titleCache is the shared title cache, replaced every titleTTL; a builder keeps the map it got, and reads and
// writes it under tmu.
func (j *Journal) titleCache() map[int64]*titleInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.titles == nil || j.now().Sub(j.tAt) > titleTTL {
		j.titles, j.tAt = map[int64]*titleInfo{}, j.now()
	}
	return j.titles
}

// build turns a head into its entry, with flags and verdict; nil when its row is gone.
func (b *builder) build(h head) (*Entry, error) {
	e := &Entry{ID: h.id, At: store.FromMillis(h.at), Kind: h.kind, Version: h.version, Flags: []Flag{}}
	if ti, err := b.title(h.titleID); err != nil {
		return nil, err
	} else if ti != nil {
		e.Title = ti.ref
	}
	if b.pol != nil {
		if vp := b.pol.Versions[h.version]; vp != nil {
			e.Mode = string(vp.Mode)
			if e.Mode == "" {
				e.Mode = "shadow"
			}
		}
	}
	prefix, rest, _ := strings.Cut(h.id, ":")
	var err error
	switch prefix {
	case "dl":
		err = b.download(e, rest)
	case "lib":
		err = b.libAction(e, rest)
	case "bl":
		err = b.blocklist(e, rest)
	case "run":
		err = b.run(e, rest)
	case "plan":
		err = b.plan(e, rest)
	case "v":
		err = b.verdictEntry(e, rest)
	case "act":
		err = b.actionEntry(e, rest)
	default:
		return nil, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("journal: %s: %w", h.id, err)
	}
	if e.Kind != KindVerdict && e.Kind != KindAction {
		if err := b.check(e); err != nil {
			return nil, fmt.Errorf("journal: %s: checks: %w", h.id, err)
		}
		if err := b.verdict(e); err != nil {
			return nil, err
		}
	}
	finish(e)
	switch {
	case e.Reasoning != nil && e.Reasoning.PolicyRev != "":
		e.PolicyRev = e.Reasoning.PolicyRev
	case e.Download != nil && e.Download.PolicyRev != "":
		e.PolicyRev = e.Download.PolicyRev
	default:
		e.PolicyRev = e.QueuedPolicyRev
	}
	if e.QueuedPolicyRev == e.PolicyRev {
		e.QueuedPolicyRev = ""
	}
	return e, nil
}

// finish sets Suspect and Severity from the unacknowledged flags.
func finish(e *Entry) {
	e.Suspect, e.Severity = false, ""
	for _, f := range e.Flags {
		if f.Acked {
			continue
		}
		if SevRank(f.Severity) > SevRank(e.Severity) {
			e.Severity = f.Severity
		}
		if SevRank(f.Severity) >= SevRank(SevWarn) {
			e.Suspect = true
		}
	}
}

func ms(n sql.NullInt64) *time.Time {
	if !n.Valid || n.Int64 == 0 {
		return nil
	}
	t := store.FromMillis(n.Int64)
	return &t
}

func intp(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// dlRow is one download.
type dlRow struct {
	id, titleID, versionID                      int64
	version, hash, release, indexer, quality    string
	score                                       int
	size                                        sql.NullInt64
	episodes, provider, torrentID, status, name string
	progress                                    float64
	cached, ownAdd                              sql.NullInt64
	reused                                      bool
	state, errText, source, policyRev           string
	created, updated                            int64
	added, ready, imported                      sql.NullInt64
	formats                                     sql.NullString
}

func (b *builder) loadDownload(id int64) (*dlRow, error) {
	var d dlRow
	err := b.db.QueryRowContext(b.ctx, `SELECT id, title_id, version_id, version_key, info_hash, release_title, indexer,
		quality, score, size_bytes, episodes, COALESCE(provider, ''), COALESCE(provider_torrent_id, ''), provider_status,
		name, progress, cached, own_add, reused, state, COALESCE(error, ''), source, created_at, updated_at, added_at,
		ready_at, imported_at, formats, policy_rev FROM download WHERE id = ?`, id).Scan(&d.id, &d.titleID,
		&d.versionID, &d.version, &d.hash, &d.release, &d.indexer, &d.quality, &d.score, &d.size, &d.episodes,
		&d.provider, &d.torrentID, &d.status, &d.name, &d.progress, &d.cached, &d.ownAdd, &d.reused, &d.state,
		&d.errText, &d.source, &d.created, &d.updated, &d.added, &d.ready, &d.imported, &d.formats, &d.policyRev)
	return &d, err
}

func boolp(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 == 1
	return &v
}

func (b *builder) download(e *Entry, rest string) error {
	idS, sub, _ := strings.Cut(rest, ":")
	id, err := strconv.ParseInt(idS, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	d, err := b.loadDownload(id)
	if err != nil {
		return err
	}
	e.Source = "theatarr"
	score := d.score
	e.Release = &Release{Title: clean(d.release), Indexer: d.indexer, InfoHash: d.hash, Size: d.size.Int64,
		Quality: d.quality, Score: &score, How: d.source}
	if d.formats.Valid {
		_ = json.Unmarshal([]byte(d.formats.String), &e.Release.Formats)
	}
	var eps []int64
	_ = json.Unmarshal([]byte(d.episodes), &eps)
	e.f.episodeIDs = eps
	if ti, _ := b.title(d.titleID); ti != nil {
		e.Release.Episodes = ti.labels(eps)
	}
	e.Download = &Download{ID: d.id, State: d.state, Provider: d.provider, TorrentID: d.torrentID, Name: clean(d.name),
		Cached: boolp(d.cached), Reused: d.reused, OwnAdd: boolp(d.ownAdd), Progress: d.progress,
		Error: cleanMessage(d.errText), Source: d.source, CreatedAt: store.FromMillis(d.created), AddedAt: ms(d.added),
		ReadyAt: ms(d.ready), ImportedAt: ms(d.imported), PolicyRev: d.policyRev}
	if run, ok := strings.CutPrefix(d.source, "acquire:"); ok {
		// The revision the acquire run was queued under; finish keeps it only when it differs.
		_ = b.db.QueryRowContext(b.ctx, `SELECT policy_rev FROM acquire_run WHERE id = ?`, run).Scan(&e.QueuedPolicyRev)
	}
	if err := b.attempts(e, d.id); err != nil {
		return err
	}
	if err := b.cache(e, d.hash); err != nil {
		return err
	}
	switch sub {
	case "":
		verb := "grabbed"
		if e.Kind == KindUpgrade {
			verb = "upgrade grabbed"
		}
		where := ""
		if d.provider != "" {
			where = " on " + d.provider
		}
		e.Summary = fmt.Sprintf("%s %s%s; now %s", verb, e.Release.Title, where, d.state)
		if err := b.decisionFor(e, d); err != nil {
			return err
		}
		if err := b.prevFile(e, d.versionID, eps, d.created); err != nil {
			return err
		}
	case "fail":
		if d.state != "failed" {
			return sql.ErrNoRows
		}
		e.Summary = "download failed: " + e.Download.Error
		e.Related = append(e.Related, "dl:"+idS)
		if err := b.failures(e, d.titleID, d.version, d.updated); err != nil {
			return err
		}
	case "import":
		if err := b.libraryImport(e, d); err != nil {
			return err
		}
		e.Related = append(e.Related, "dl:"+idS)
	default:
		return sql.ErrNoRows
	}
	return b.downloadAfter(e, d, sub)
}

func (b *builder) attempts(e *Entry, dl int64) error {
	rows, err := b.db.QueryContext(b.ctx, `SELECT provider, outcome, code, message, retry_at, attempted_at
		FROM download_attempt WHERE download_id = ? ORDER BY id`, dl)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		var retry sql.NullInt64
		var at int64
		if err := rows.Scan(&a.Provider, &a.Outcome, &a.Code, &a.Message, &retry, &at); err != nil {
			return err
		}
		a.Message, a.At, a.RetryAt = cleanMessage(a.Message), store.FromMillis(at), ms(retry)
		e.Attempts = append(e.Attempts, a)
	}
	return rows.Err()
}

func (b *builder) cache(e *Entry, hash string) error {
	if hash == "" {
		return nil
	}
	rows, err := b.db.QueryContext(b.ctx, `SELECT provider, state, source, checked_at, expires_at FROM provider_cache
		WHERE info_hash = ? ORDER BY provider`, strings.ToLower(hash))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Evidence
		var at int64
		var exp sql.NullInt64
		if err := rows.Scan(&c.Provider, &c.State, &c.Source, &at, &exp); err != nil {
			return err
		}
		c.CheckedAt, c.ExpiresAt = store.FromMillis(at), ms(exp)
		e.Cache = append(e.Cache, c)
	}
	return rows.Err()
}

// snapshotFor reads the journal_decision a route came from: same source, title and version, at or before at.
func (b *builder) snapshotFor(source string, titleID int64, version string, at int64) (*Reasoning, error) {
	if !b.own || source == "" {
		return nil, nil
	}
	var cj, rj, notes, target, rev string
	var results, accepted int
	var trimmed bool
	err := b.db.QueryRowContext(b.ctx, `SELECT candidates, rejections, notes, target, results, accepted, trimmed,
		policy_rev FROM journal_decision WHERE source = ? AND title_id = ? AND version_key = ? AND created_at <= ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, source, titleID, version, at).Scan(&cj, &rj, &notes, &target,
		&results, &accepted, &trimmed, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &Reasoning{Basis: "theatarr search", Results: results, Accepted: accepted, Notes: clean(notes), PolicyRev: rev}
	if target == "rss" {
		r.Basis = "theatarr rss"
	}
	var cands []snapshot.Candidate
	_ = json.Unmarshal([]byte(cj), &cands)
	_ = json.Unmarshal([]byte(rj), &r.Rejections)
	for i := range cands {
		cleanCandidate(&cands[i])
	}
	r.RunnersUp = cands
	if trimmed {
		r.Missing = "candidates trimmed by retention"
	}
	return r, nil
}

// pickChosen moves the chosen candidate to Chosen: the one with hash, else the one named release, else (with
// neither given) the first accepted one.
func (r *Reasoning) pickChosen(hash, release string) {
	idx := -1
	for i, c := range r.RunnersUp {
		if hash != "" && strings.EqualFold(c.InfoHash, hash) {
			idx = i
			break
		}
	}
	for i, c := range r.RunnersUp {
		if idx < 0 && release != "" && c.Release == release {
			idx = i
		}
	}
	for i, c := range r.RunnersUp {
		if idx < 0 && hash == "" && release == "" && c.Accepted {
			idx = i
		}
	}
	if idx < 0 {
		return
	}
	c := r.RunnersUp[idx]
	r.Chosen = &c
	r.RunnersUp = slices.Delete(slices.Clone(r.RunnersUp), idx, idx+1)
	r.Existing = c.Existing
}

func (b *builder) decisionFor(e *Entry, d *dlRow) error {
	r, err := b.snapshotFor(d.source, d.titleID, d.version, d.created)
	if err != nil {
		return err
	}
	if r == nil {
		e.Reasoning = &Reasoning{Basis: "router", Missing: "no stored candidate list for this route " +
			"(recorded for routes from migration 00024 on; source " + orDash(d.source) + ")"}
		return nil
	}
	r.pickChosen(d.hash, clean(d.release))
	if r.Chosen != nil {
		if r.Chosen.Existing != nil {
			s := r.Chosen.Existing.Score
			e.f.prevScore, e.f.prevQuality = &s, r.Chosen.Existing.Quality
		}
		if len(e.Release.Languages) == 0 {
			e.Release.Languages = r.Chosen.Languages
		}
	}
	e.Reasoning = r
	return nil
}

// prevFile fills the version's file before the grab, when the decision did not say: the latest of our imported
// downloads and the arr's files for the same movie or episodes.
func (b *builder) prevFile(e *Entry, versionID int64, eps []int64, before int64) error {
	if e.f.prevScore != nil || e.Kind != KindUpgrade {
		return nil
	}
	var rel, q string
	var score sql.NullInt64
	err := b.db.QueryRowContext(b.ctx, `SELECT release_title, quality, score FROM download WHERE version_id = ?
		AND state = 'imported' AND imported_at < ? ORDER BY imported_at DESC LIMIT 1`, versionID, before).Scan(&rel,
		&q, &score)
	if errors.Is(err, sql.ErrNoRows) {
		err = b.db.QueryRowContext(b.ctx, `SELECT COALESCE(release_title, scene_name), quality, custom_format_score
			FROM version_file WHERE version_id = ? AND COALESCE(added_at, 0) < ? ORDER BY added_at DESC LIMIT 1`,
			versionID, before).Scan(&rel, &q, &score)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e.f.prevScore, e.f.prevQuality, e.f.prevRelease = intp(score), q, clean(rel)
	return nil
}

// downloadAfter lists what followed a download: state changes, blocklisting, re-searches, the import.
func (b *builder) downloadAfter(e *Entry, d *dlRow, sub string) error {
	base := "dl:" + strconv.FormatInt(d.id, 10)
	add := func(at sql.NullInt64, kind, text, ref string) {
		if t := ms(at); t != nil {
			e.After = append(e.After, Event{At: *t, Kind: kind, Text: text, Ref: ref})
			if ref != "" && ref != e.ID && !slices.Contains(e.Related, ref) {
				e.Related = append(e.Related, ref)
			}
		}
	}
	if sub == "" {
		add(d.added, "added", "accepted by "+orDash(d.provider), "")
		add(d.ready, "ready", "every selected file on "+orDash(d.provider), "")
	}
	if d.state == "failed" {
		k := "failure"
		if strings.HasPrefix(d.errText, "stalled") {
			k = "stall"
		}
		add(sql.NullInt64{Int64: d.updated, Valid: true}, k, cleanMessage(d.errText), base+":fail")
	}
	var li sql.NullString
	var liAt sql.NullInt64
	if err := b.db.QueryRowContext(b.ctx, `SELECT state, updated_at FROM library_import WHERE download_id = ?`,
		d.id).Scan(&li, &liAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if li.Valid && sub != "import" {
		add(liAt, "import", li.String, base+":import")
	}
	if sub != "import" {
		add(d.imported, "imported", "linked into the library", "")
	}
	rows, err := b.db.QueryContext(b.ctx, `SELECT rowid, created_at, source, reason FROM router_blocklist
		WHERE version_key = ? AND info_hash = ?`, d.version, d.hash)
	if err != nil {
		return err
	}
	var bls []Event
	for rows.Next() {
		var id, at int64
		var src, reason string
		if err := rows.Scan(&id, &at, &src, &reason); err != nil {
			rows.Close()
			return err
		}
		bls = append(bls, Event{At: store.FromMillis(at), Kind: "blocklist", Text: src + ": " + cleanMessage(reason),
			Ref: "bl:" + strconv.FormatInt(id, 10)})
	}
	rows.Close()
	for _, ev := range bls {
		add(sql.NullInt64{Int64: store.Millis(ev.At), Valid: true}, ev.Kind, ev.Text, ev.Ref)
	}
	if d.state == "failed" {
		if err := b.researchAfter(e, d.titleID, d.version, d.updated-60_000, add); err != nil {
			return err
		}
	}
	slices.SortStableFunc(e.After, func(a, c Event) int { return a.At.Compare(c.At) })
	return nil
}

func (b *builder) researchAfter(e *Entry, titleID int64, version string, after int64,
	add func(sql.NullInt64, string, string, string)) error {
	rows, err := b.db.QueryContext(b.ctx, `SELECT id, created_at, state, research, message FROM acquire_run r
		WHERE title_id = ? AND version_key = ? AND `+failureRunSQL+` AND created_at >= ?
		ORDER BY created_at LIMIT 5`, titleID, version, after)
	if err != nil {
		return err
	}
	type ev struct {
		id, at           int64
		state, why, text string
	}
	var evs []ev
	for rows.Next() {
		var x ev
		if err := rows.Scan(&x.id, &x.at, &x.state, &x.why, &x.text); err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, x)
	}
	rows.Close()
	for _, x := range evs {
		add(sql.NullInt64{Int64: x.at, Valid: true}, "research", x.why+": "+x.state+" "+cleanMessage(x.text),
			"run:"+strconv.FormatInt(x.id, 10))
	}
	return rows.Err()
}

// failures counts failure-like events of a title's version in the 30 days up to at (our failed downloads, the
// blocklist without a download), and those in the 30 days after it.
func (b *builder) failures(e *Entry, titleID int64, version string, at int64) error {
	const window = 30 * 24 * 3600 * 1000
	count := func(from, to int64) (int, error) {
		var n int
		err := b.db.QueryRowContext(b.ctx, `SELECT
			(SELECT COUNT(*) FROM download WHERE title_id = ?1 AND version_key = ?2 AND state = 'failed'
				AND updated_at BETWEEN ?3 AND ?4)
			+ (SELECT COUNT(*) FROM router_blocklist WHERE title_id = ?1 AND version_key = ?2 AND download_id IS NULL
				AND created_at BETWEEN ?3 AND ?4)`, titleID, version, from, to).Scan(&n)
		return n, err
	}
	var err error
	if e.f.failureCount, err = count(at-window, at+1000); err != nil {
		return err
	}
	e.f.failuresAfter, err = count(at+1001, at+window)
	return err
}

// libraryImport fills the library's import of a download.
type planJSON struct {
	Folder string `json:"folder"`
	Files  []struct {
		Path   string        `json:"path"`
		Rel    string        `json:"rel"`
		Size   int64         `json:"size"`
		Probe  *probe.Result `json:"probe"`
		Reason string        `json:"reason"`
		Note   string        `json:"note"`
	} `json:"files"`
	Links []struct {
		Path   string `json:"path"`
		Target string `json:"target"`
		Exists bool   `json:"exists"`
	} `json:"links"`
	Rejected []struct {
		Path       string   `json:"path"`
		Reason     string   `json:"reason"`
		Episodes   []string `json:"episodes"`
		NotUpgrade bool     `json:"not_upgrade"`
	} `json:"rejected"`
	Unmatched []string `json:"unmatched"`
	Refused   string   `json:"refused"`
}

func (b *builder) libraryImport(e *Entry, d *dlRow) error {
	var state, plan string
	var errText sql.NullString
	if err := b.db.QueryRowContext(b.ctx, `SELECT state, last_error, plan FROM library_import WHERE download_id = ?`,
		d.id).Scan(&state, &errText, &plan); err != nil {
		return err
	}
	im := &Import{State: state, Error: cleanMessage(errText.String)}
	var p planJSON
	_ = json.Unmarshal([]byte(plan), &p)
	for _, f := range p.Files {
		file := File{Path: f.Path, Size: f.Size, Source: "probe", Note: f.Note}
		if f.Note != "" && e.f.importNotes == "" {
			e.f.importNotes = f.Note
		}
		if f.Probe != nil {
			file.RuntimeSeconds = int(f.Probe.RunTimeSeconds)
			if v := f.Probe.Video; v != nil {
				file.Resolution = fmt.Sprintf("%dx%d", v.Width, v.Height)
				file.VideoCodec = v.Codec
				if e.f.fileWidth == 0 {
					e.f.fileWidth, e.f.fileHeight = v.Width, v.Height
				}
			}
			var langs []string
			for _, a := range f.Probe.Audio {
				if a.Language != "" && !slices.Contains(langs, a.Language) {
					langs = append(langs, a.Language)
				}
			}
			file.AudioLanguages = strings.Join(langs, "/")
			if e.f.fileRuntime == 0 {
				e.f.fileRuntime, e.f.fileAudio = file.RuntimeSeconds, langs
			}
		}
		im.Files = append(im.Files, file)
	}
	for _, r := range p.Rejected {
		why := cleanMessage(r.Reason)
		if len(r.Episodes) > 0 {
			why += " (" + strings.Join(r.Episodes, ", ") + ")"
		}
		im.Files = append(im.Files, File{Path: r.Path, Source: "rejected: " + why})
		if !r.NotUpgrade { // a file skipped as not an upgrade is no guard refusal
			e.f.importRejects = append(e.f.importRejects, why)
		}
	}
	for _, l := range p.Links {
		im.Links = append(im.Links, Link{Path: l.Path, Target: l.Target, Kind: "planned"})
	}
	rows, err := b.db.QueryContext(b.ctx, `SELECT id, path, target, state FROM link WHERE download_id = ? ORDER BY id`,
		d.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Path, &l.Target, &l.State); err != nil {
			return err
		}
		l.Kind = "link"
		im.Links = append(im.Links, l)
		e.f.importLinked = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if state == "rejected" && !e.f.importLinked && len(e.f.importRejects) > 0 {
		var n int
		if err := b.db.QueryRowContext(b.ctx, `SELECT COUNT(*) FROM router_blocklist WHERE download_id = ?
			AND source = 'library'`, d.id).Scan(&n); err != nil {
			return err
		}
		e.f.importBlocklisted = n > 0
	}
	e.Import = im
	e.Source = "library"
	e.Summary = "library import " + state
	switch {
	case p.Refused != "":
		e.Summary += " (" + clean(p.Refused) + ")"
	case im.Error != "":
		e.Summary += ": " + im.Error
	}
	if len(p.Unmatched) > 0 {
		e.Summary += fmt.Sprintf("; %d files matched no episode", len(p.Unmatched))
	}
	return nil
}

func (b *builder) libAction(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	var kind, mode, detail string
	var path, target, release, quality sql.NullString
	var dl, link sql.NullInt64
	// A link action's release title and quality are its own download's (a replace names the old file, so it has
	// none; a link action without a download stays bare).
	if err := b.db.QueryRowContext(b.ctx, `SELECT a.kind, a.mode, a.path, a.target, a.detail, a.download_id, a.link_id,
		d.release_title, d.quality FROM library_action a LEFT JOIN download d ON d.id = a.download_id AND a.kind = 'link'
		WHERE a.id = ?`, id).Scan(&kind, &mode, &path, &target, &detail, &dl, &link, &release, &quality); err != nil {
		return err
	}
	if release.Valid {
		e.Release = &Release{Title: clean(release.String), Quality: quality.String}
	}
	e.Source = "library"
	e.Import = &Import{State: mode, Links: []Link{{ID: link.Int64, Path: path.String, Target: target.String,
		Mode: mode, Kind: kind}}}
	e.Summary = fmt.Sprintf("%s (%s) %s", kind, mode, path.String)
	if detail != "" {
		e.Summary += ": " + cleanMessage(detail)
	}
	if dl.Valid {
		e.Related = append(e.Related, "dl:"+strconv.FormatInt(dl.Int64, 10))
	}
	if e.Title.ID == 0 && path.String != "" {
		e.Title.Name = folderName(path.String)
	}
	return nil
}

// folderName is the title folder of a library path: the segment before a "Season NN" folder or the file.
func folderName(p string) string {
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if !strings.HasPrefix(strings.ToLower(parts[i]), "season ") && parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

func (b *builder) blocklist(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	var hash, release, reason, source string
	var dl, researched sql.NullInt64
	var titleID sql.NullInt64
	if err := b.db.QueryRowContext(b.ctx, `SELECT info_hash, release_title, reason, source, download_id, researched_at,
		title_id FROM router_blocklist WHERE rowid = ?`, id).Scan(&hash, &release, &reason, &source, &dl, &researched,
		&titleID); err != nil {
		return err
	}
	e.Source = source
	if source == router.BlocklistLibrary || source == router.BlocklistStalled || source == router.BlocklistTorrentFiles {
		e.Source = "router"
		if source == router.BlocklistLibrary {
			e.Source = "library"
		}
	}
	e.Release = &Release{Title: clean(release), InfoHash: hash}
	e.Summary = fmt.Sprintf("blocklisted for %s by %s: %s", e.Version, source, cleanMessage(reason))
	if dl.Valid {
		ref := "dl:" + strconv.FormatInt(dl.Int64, 10)
		e.Related = append(e.Related, ref)
		if d, err := b.loadDownload(dl.Int64); err == nil {
			score := d.score
			e.Release.Indexer, e.Release.Quality, e.Release.Score, e.Release.Size = d.indexer, d.quality, &score,
				d.size.Int64
		}
	}
	if researched.Valid {
		e.After = append(e.After, Event{At: store.FromMillis(researched.Int64), Kind: "research",
			Text: "re-search requested (or the version could not write)"})
	}
	return b.failures(e, titleID.Int64, e.Version, store.Millis(e.At))
}

func (b *builder) run(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	var source, target, state, message, queuedRev string
	var research sql.NullString
	var results, accepted int
	var created int64
	var failure bool
	if err := b.db.QueryRowContext(b.ctx, `SELECT source, target, state, message, research, results, accepted,
		policy_rev, created_at, COALESCE(`+failureRunSQL+`, 0) FROM acquire_run WHERE id = ?`, id).Scan(&source, &target,
		&state, &message, &research, &results, &accepted, &queuedRev, &created, &failure); err != nil {
		return err
	}
	e.QueuedPolicyRev = queuedRev
	e.Source = "theatarr"
	e.Summary = fmt.Sprintf("%s search of %s: %s: %s", source, target, state, cleanMessage(message))
	if failure {
		e.Summary = fmt.Sprintf("re-search (%s) of %s: %s: %s", research.String, target, state, cleanMessage(message))
	}
	r, err := b.snapshotFor("acquire:"+rest, e.Title.ID, e.Version, created+24*3600*1000)
	if err != nil {
		return err
	}
	if r == nil {
		r = &Reasoning{Basis: "theatarr search", Results: results, Accepted: accepted}
	}
	r.pickChosen("", "")
	e.Reasoning = r
	rows, err := b.db.QueryContext(b.ctx, `SELECT id FROM download WHERE source = ? AND title_id = ?
		AND version_key = ? ORDER BY id`, "acquire:"+rest, e.Title.ID, e.Version)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dl int64
		if err := rows.Scan(&dl); err != nil {
			return err
		}
		e.Related = append(e.Related, "dl:"+strconv.FormatInt(dl, 10))
	}
	return rows.Err()
}

func (b *builder) planRow(id int64) (*Plan, error) {
	p := &Plan{ID: id}
	var steps string
	if err := b.db.QueryRowContext(b.ctx, `SELECT mode, outcome, release_title, provider, source, steps
		FROM router_plan WHERE id = ?`, id).Scan(&p.Mode, &p.Outcome, &p.Release, &p.Provider, &p.Source,
		&steps); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(steps), &p.Steps)
	for i := range p.Steps {
		p.Steps[i].Message = cleanMessage(p.Steps[i].Message)
	}
	p.Release = clean(p.Release)
	p.Summary = router.PlanSummary(p.Outcome, p.Release, p.Provider, p.Steps)
	return p, nil
}

func (b *builder) plan(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	p, err := b.planRow(id)
	if err != nil {
		return err
	}
	e.Source = "router"
	e.Summary = "dry run (" + p.Mode + "): " + p.Summary
	var hash, indexer string
	_ = b.db.QueryRowContext(b.ctx, `SELECT info_hash, indexer FROM router_plan WHERE id = ?`, id).Scan(&hash, &indexer)
	if p.Release != "" {
		e.Release = &Release{Title: p.Release, InfoHash: hash, Indexer: indexer, How: p.Source}
	}
	r, err := b.snapshotFor(p.Source, e.Title.ID, e.Version, store.Millis(e.At))
	if err != nil {
		return err
	}
	if r == nil {
		r = &Reasoning{Basis: "router"}
	}
	r.pickChosen(hash, p.Release)
	r.Plan = p
	e.Reasoning = r
	return nil
}
