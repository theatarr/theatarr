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
	titles map[int64]*titleInfo
	// list: building a page of the list, where an adoption's line counts its group's files from its History row
	// instead of reading them (they are listed on its entry).
	list bool
	// full: building the entry page, which shows stored messages and details in full; the list and the flags keep
	// the short form (cleanText).
	full bool
}

func (b *builder) msg(s string) string   { return cleanText(s, b.full) }
func (b *builder) plain(s string) string { return plainText(s, b.full) }

func (j *Journal) newBuilder(ctx context.Context) *builder {
	return &builder{j: j, pol: j.policy(ctx), ctx: ctx, db: j.st.Reader(), titles: j.titleCache()}
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

// load reads a head's entry from its sources, without flags; nil when its row is gone.
func (b *builder) load(h head) (*Entry, error) {
	e := &Entry{ID: h.id, At: store.FromMillis(h.at), Kind: h.kind, Version: h.version, Flags: []Flag{}}
	if ti, err := b.title(h.titleID); err != nil {
		return nil, err
	} else if ti != nil {
		e.Title = ti.ref
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

// finish sets Suspect and Severity from the flags.
func finish(e *Entry) {
	e.Suspect, e.Severity = false, ""
	for _, f := range e.Flags {
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
	// its library import, if any: state, error, when
	liState, liErr sql.NullString
	liAt           sql.NullInt64
}

func (b *builder) loadDownload(id int64) (*dlRow, error) {
	var d dlRow
	err := b.db.QueryRowContext(b.ctx, `SELECT id, title_id, version_id, version_key, info_hash, release_title, indexer,
		quality, score, size_bytes, episodes, COALESCE(provider, ''), COALESCE(provider_torrent_id, ''), provider_status,
		name, progress, cached, own_add, reused, state, COALESCE(error, ''), source, created_at, updated_at, added_at,
		ready_at, imported_at, formats, policy_rev,
		(SELECT state FROM library_import WHERE download_id = download.id),
		(SELECT last_error FROM library_import WHERE download_id = download.id),
		(SELECT updated_at FROM library_import WHERE download_id = download.id) FROM download WHERE id = ?`, id).Scan(&d.id, &d.titleID,
		&d.versionID, &d.version, &d.hash, &d.release, &d.indexer, &d.quality, &d.score, &d.size, &d.episodes,
		&d.provider, &d.torrentID, &d.status, &d.name, &d.progress, &d.cached, &d.ownAdd, &d.reused, &d.state,
		&d.errText, &d.source, &d.created, &d.updated, &d.added, &d.ready, &d.imported, &d.formats, &d.policyRev,
		&d.liState, &d.liErr, &d.liAt)
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
		Error: b.msg(d.errText), Source: d.source, CreatedAt: store.FromMillis(d.created), AddedAt: ms(d.added),
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
		e.Label = "Grabbed"
		where := ""
		if d.provider != "" {
			where = " on " + providerShort(d.provider)
		}
		// Its link actions stand under this line (headParts): the files it linked and replaced.
		kids, err := b.downloadLinks(d.id)
		if err != nil {
			return err
		}
		var linked, replaced []string
		for _, k := range kids {
			switch {
			case k.mode != "applied":
			case k.kind == "link":
				linked = append(linked, k.path)
			case !strings.HasPrefix(k.detail, "import undone"):
				replaced = append(replaced, k.path)
			}
		}
		e.Paths = append(linked, replaced...)
		switch {
		case len(replaced) > 0:
			e.Label = "Upgraded" // as History says: it replaced a file
		case d.state == "failed":
			e.Label = "Failed"
		}
		outcome := grabOutcome(d.state, d.errText, len(linked), len(replaced), baseName(firstOf(replaced)), b.full)
		if d.state == "ready" || d.state == "importing" {
			// Its import's state, when it waits or was held.
			st, why := d.liState.String, d.liErr.String
			if st != "" && st != "linked" {
				_, outcome = importWords(st, false)
				if why != "" && st != "dry_run" {
					outcome += ": " + b.plain(why)
				}
			}
		}
		e.Summary = join("Grabbed "+e.Release.Title+where, outcome)
		if err := b.decisionFor(e, d); err != nil {
			return err
		}
		if err := b.prevFile(e, d.versionID, eps, d.created); err != nil {
			return err
		}
		for _, k := range kids {
			e.After = append(e.After, Event{At: store.FromMillis(k.at), Kind: KindLink, Text: k.words(b),
				Ref: "lib:" + strconv.FormatInt(k.id, 10)})
		}
	case "fail":
		if d.state != "failed" {
			return sql.ErrNoRows
		}
		e.Label = "Failed"
		e.Summary = "Download failed: " + b.plain(e.Download.Error)
		if e.Kind == KindStall {
			e.Label, e.Summary = "Stalled", "Download stalled: "+b.plain(strings.TrimPrefix(e.Download.Error, "stalled: "))
		}
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
		a.Message, a.At, a.RetryAt = b.msg(a.Message), store.FromMillis(at), ms(retry)
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
	if source == "" {
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
// downloads for the same movie or episodes.
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
		add(d.added, "added", "accepted by "+orDash(providerShort(d.provider)), "")
		add(d.ready, "ready", "every selected file on "+orDash(providerShort(d.provider)), "")
	}
	if d.state == "failed" {
		k := "failure"
		if strings.HasPrefix(d.errText, "stalled") {
			k = "stall"
		}
		add(sql.NullInt64{Int64: d.updated, Valid: true}, k, b.msg(d.errText), base+":fail")
	}
	if d.liState.Valid && sub != "import" {
		_, words := importWords(d.liState.String, d.state == "failed")
		add(d.liAt, "import", words, base+":import")
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
		bls = append(bls, Event{At: store.FromMillis(at), Kind: "blocklist", Text: src + ": " + b.msg(reason),
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
		add(sql.NullInt64{Int64: x.at, Valid: true}, "research", x.why+": "+x.state+" "+b.msg(x.text),
			"run:"+strconv.FormatInt(x.id, 10))
	}
	return rows.Err()
}

// failureWindow is how far back repeated failures count.
const failureWindow = 30 * 24 * 3600 * 1000

// failures counts failure-like events of a title's version in the 30 days up to at (our failed downloads, the
// blocklist without a download), and those in the 30 days after it (none for a new event; an older row flagged by
// the start pass may have later ones).
func (b *builder) failures(e *Entry, titleID int64, version string, at int64) error {
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
	if e.f.failureCount, err = count(at-failureWindow, at+1000); err != nil {
		return err
	}
	e.f.failuresAfter, err = count(at+1001, at+failureWindow)
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
	im := &Import{State: state, Error: b.msg(errText.String)}
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
		why := b.msg(r.Reason)
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
	label, words := importWords(state, d.state == "failed")
	e.Label = label
	why := ""
	switch {
	case p.Refused != "":
		why = b.plain(p.Refused)
	case im.Error != "":
		why = b.plain(im.Error)
	}
	var linked []string
	for _, l := range im.Links {
		if l.Kind == "link" {
			linked = append(linked, l.Path)
		}
	}
	files := ""
	if len(linked) > 1 {
		files = fmt.Sprintf("%d files", len(linked))
	}
	e.Paths = linked
	unmatched := ""
	if len(p.Unmatched) > 0 {
		unmatched = fmt.Sprintf("%d files matched no episode", len(p.Unmatched))
	}
	head := "Imported " + e.Release.Title
	if state != "linked" {
		head = e.Release.Title + ": " + words
		if why != "" {
			head += ": " + why
			why = ""
		}
	}
	e.Summary = join(head, files, why, unmatched)
	return nil
}

// libKid is one of a download's link actions (link or replace, applied): they stand under its grab's line.
type libKid struct {
	id, at                   int64
	kind, mode, path, detail string
}

// words is the action as a line of the grab's "What followed".
func (k libKid) words(b *builder) string {
	verb := map[string]string{"link": "link", "replace": "replace"}[k.kind]
	switch k.mode {
	case "applied":
	case "dry_run":
		return join("Dry run: would "+verb+" "+baseName(k.path), b.plain(k.detail))
	default:
		return join("Could not "+verb+" "+baseName(k.path), b.plain(k.detail))
	}
	if k.kind == "link" {
		return "Linked " + baseName(k.path)
	}
	if strings.HasPrefix(k.detail, "import undone") {
		return join("Import undone for "+baseName(k.path), b.plain(strings.TrimPrefix(k.detail, "import undone: ")))
	}
	return join("Replaced "+baseName(k.path), b.plain(k.detail))
}

// downloadLinks are download id's applied link and replace actions, oldest first.
func (b *builder) downloadLinks(id int64) ([]libKid, error) {
	rows, err := b.db.QueryContext(b.ctx, `SELECT id, at, kind, mode, COALESCE(path, ''), detail FROM library_action
		WHERE download_id = ? AND kind IN ('link', 'replace') ORDER BY at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []libKid
	for rows.Next() {
		var k libKid
		if err := rows.Scan(&k.id, &k.at, &k.kind, &k.mode, &k.path, &k.detail); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func firstOf(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
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
	if path.String != "" {
		e.Paths = []string{path.String}
	}
	if dl.Valid {
		e.Related = append(e.Related, "dl:"+strconv.FormatInt(dl.Int64, 10))
	}
	if e.Title.ID == 0 && path.String != "" {
		e.Title.Name = folderName(path.String)
	}
	return b.libWords(e, id, kind, mode, path.String, detail, dl.Valid)
}

// libWords words a library action's entry; an adoption's first file stands for its group and the files it replaced,
// a hand unlink for its action (headParts).
func (b *builder) libWords(e *Entry, id int64, kind, mode, path, detail string, withDownload bool) error {
	name := baseName(path)
	verbs := map[string]string{"link": "link", "replace": "replace", "relink": "relink", "dead": "mark missing"}
	switch {
	case mode == "dry_run":
		e.Label, e.Summary = "Dry run", join("Dry run: would "+verbs[kind]+" "+name, b.plain(detail))
	case mode != "applied":
		e.Label, e.Summary = "Not done", join("Could not "+verbs[kind]+" "+name, b.plain(detail))
	case kind == "link" && withDownload:
		e.Label, e.Summary = "Linked", "Linked "+name
	case kind == "link" && strings.HasPrefix(strings.ToLower(detail), "adopted from "):
		return b.adoption(e, id, name, detail)
	case kind == "link":
		if strings.HasPrefix(detail, "linked ") {
			detail = "" // the file it links to, on the entry
		}
		e.Label, e.Summary = "Linked", join("Linked "+name, b.plain(detail))
	case kind == "replace" && withDownload:
		k := libKid{kind: kind, mode: mode, path: path, detail: detail}
		e.Label, e.Summary = "Replaced", k.words(b)
		if strings.HasPrefix(detail, "import undone") {
			e.Label = "Import undone"
		}
	case kind == "replace" && strings.HasPrefix(detail, "change of episodes failed"):
		e.Label, e.Summary = "Put back", join(name, b.plain(detail))
	case kind == "replace":
		e.Label = "Removed"
		if r, ok := strings.CutPrefix(detail, "unlinked ("); ok {
			why, rest, _ := strings.Cut(r, "), moved aside")
			rest = strings.TrimPrefix(rest, " to ")
			trash := "moved to the trash"
			if _, more, ok := strings.Cut(rest, "; "); ok {
				trash = join(trash, b.plain(more))
			} else if rest == "" {
				trash = "already gone"
			}
			e.Summary = join("Removed "+name, b.plain(byWhom(why)), trash)
		} else {
			e.Summary = join("Removed "+name, b.plain(detail))
		}
		return b.unlinkAction(e, path)
	case kind == "relink":
		e.Label, e.Summary = "Relinked", join("Relinked "+name, b.plain(detail))
	case kind == "dead":
		if missingHash.MatchString(detail) {
			detail = "" // only the file's hash
		}
		e.Label, e.Summary = "Missing", join("Missing "+name, b.plain(detail))
	default:
		e.Label, e.Summary = kind, join(name, b.plain(detail))
	}
	return nil
}

// unlinkAction adds the hand unlink's action (its own entry stands under this one) to a removal's What followed.
func (b *builder) unlinkAction(e *Entry, path string) error {
	if path == "" || !b.j.hasActions(b.ctx) {
		return nil
	}
	var id, at int64
	var by string
	err := b.db.QueryRowContext(b.ctx, `SELECT id, created_at, by FROM journal_action WHERE action = 'unlink_file'
		AND target = ? AND version_key = ? AND COALESCE(title_id, 0) = ? AND created_at BETWEEN ? AND ? ORDER BY id
		LIMIT 1`, path, e.Version, e.Title.ID, store.Millis(e.At), store.Millis(e.At)+60_000).Scan(&id, &at, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ref := "act:" + strconv.FormatInt(id, 10)
	e.After = append(e.After, Event{At: store.FromMillis(at), Kind: KindAction, Text: "Removed by " + orDash(by), Ref: ref})
	e.Related = append(e.Related, ref)
	return nil
}

// tipFiles is how many of an adoption's files the list's tooltip names before "and N more".
const tipFiles = 5

// adoption words an adoption's action. The first file of its History group (one row per title, library, release and
// day) stands for the group's other files and for the files the adoption replaced (History's own pairing):
// "Adopted 8 files of Season 1 from Sonarr (sonarr_4k) · replaced 2 files".
func (b *builder) adoption(e *Entry, id int64, name, detail string) error {
	e.Label = "Adopted"
	inst, _, _ := strings.Cut(strings.TrimSpace(detail[len("adopted from "):]), " ")
	from := " from " + arrWords(clean(inst))
	e.Summary = "Adopted " + name + from
	ref := "lib:" + strconv.FormatInt(id, 10)
	var pAt, files int64
	var rel, eps, more string
	// The list's tooltip names the group's first files: up to tipFiles-1 more besides this one, read in the same
	// statement (the group's other actions follow its first, on the day of the group's row; the entry reads them all
	// below).
	moreSQL := `'[]'`
	if b.list {
		moreSQL = `(SELECT json_group_array(path) FROM (SELECT COALESCE(la.path, '') AS path FROM library_action la
			WHERE la.at >= (SELECT at FROM library_action WHERE id = ?2) AND la.at < h.at / 86400000 * 86400000 + 86400000
			AND la.id <> ?2 AND la.kind = 'link'
			AND la.mode = 'applied' AND la.download_id IS NULL AND la.detail LIKE 'adopted from %'
			AND ` + libTitleSQL + ` = ?3 AND COALESCE(la.version_key, '') = ?4
			AND ` + store.HistoryAdoptReleaseSQL + ` = COALESCE(h.release, '')
			AND NOT EXISTS (SELECT 1 FROM history o WHERE o.ref = 'lib:' || la.id) ORDER BY la.at, la.id LIMIT ` +
			strconv.Itoa(tipFiles-1) + `))`
	}
	args := []any{ref}
	if b.list {
		args = append(args, id, e.Title.ID, e.Version)
	}
	err := b.db.QueryRowContext(b.ctx, `SELECT at, COALESCE(release, ''), episodes, files, `+moreSQL+` FROM history h
		WHERE ref = ?1 AND event = 'adopted'`, args...).Scan(&pAt, &rel, &eps, &files, &more)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // one of a group's other files, or no History row
	}
	if err != nil {
		return err
	}
	type file struct {
		id, at     int64
		path, what string
	}
	var members, replaced []file
	n := int(files)
	if !b.list {
		day := pAt / 86_400_000 * 86_400_000
		rows, err := b.db.QueryContext(b.ctx, `SELECT la.id, la.at, COALESCE(la.path, '') FROM library_action la
			WHERE la.at >= ? AND la.at < ? AND la.id <> ? AND la.kind = 'link' AND la.mode = 'applied'
			AND la.download_id IS NULL AND la.detail LIKE 'adopted from %' AND `+libTitleSQL+` = ?
			AND COALESCE(la.version_key, '') = ? AND `+store.HistoryAdoptReleaseSQL+` = ?
			AND NOT EXISTS (SELECT 1 FROM history o WHERE o.ref = 'lib:' || la.id) ORDER BY la.at, la.id`,
			day, day+86_400_000, id, e.Title.ID, e.Version, rel)
		if err != nil {
			return err
		}
		for rows.Next() {
			var f file
			if err := rows.Scan(&f.id, &f.at, &f.path); err != nil {
				rows.Close()
				return err
			}
			f.what = "Adopted " + baseName(f.path)
			members = append(members, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		n = len(members) + 1
	}
	rows, err := b.db.QueryContext(b.ctx, `SELECT CAST(substr(history.ref, 5) AS INTEGER), history.at,
			COALESCE(la.path, ''), COALESCE(la.detail, '')
		FROM history p JOIN history ON history.event = 'removed'
			AND `+adoptionOfRemovalSQL(`history.at BETWEEN p.at - 60000 AND p.at + 60000`)+`
		LEFT JOIN library_action la ON la.id = CAST(substr(history.ref, 5) AS INTEGER)
		WHERE p.ref = ? ORDER BY history.at, history.ref`, ref)
	if err != nil {
		return err
	}
	for rows.Next() {
		var f file
		var d string
		if err := rows.Scan(&f.id, &f.at, &f.path, &d); err != nil {
			rows.Close()
			return err
		}
		why := ""
		if r, ok := strings.CutPrefix(d, "unlinked ("); ok {
			why, _, _ = strings.Cut(r, "), moved aside")
		}
		f.what = join("Removed "+baseName(f.path), b.plain(byWhom(why)))
		replaced = append(replaced, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if n > 1 {
		var ids []int64
		_ = json.Unmarshal([]byte(eps), &ids)
		what := fmt.Sprintf("%d files", n)
		if ti, _ := b.title(e.Title.ID); ti != nil {
			if s := seasonOf(ti.labels(ids)); s != "" {
				what += " of " + s
			}
		}
		e.Summary = "Adopted " + what + from
	}
	if len(replaced) > 0 {
		var names []string
		for _, f := range replaced {
			names = append(names, baseName(f.path))
		}
		e.Summary = join(e.Summary, "replaced "+filesWord(names))
	}
	if b.list {
		var paths []string
		_ = json.Unmarshal([]byte(more), &paths)
		e.Paths = append(e.Paths, paths...)
		if left := n - 1 - len(paths); left > 0 && len(paths) == tipFiles-1 {
			e.Paths = append(e.Paths, fmt.Sprintf("and %d more", left))
		}
	}
	for _, f := range append(members, replaced...) {
		e.Paths = append(e.Paths, f.path)
		r := "lib:" + strconv.FormatInt(f.id, 10)
		e.After = append(e.After, Event{At: store.FromMillis(f.at), Kind: KindLink, Text: f.what, Ref: r})
		e.Related = append(e.Related, r)
	}
	slices.SortStableFunc(e.After, func(a, c Event) int { return a.At.Compare(c.At) })
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
	e.Label = "Blocked"
	e.Summary = "Blocked " + e.Release.Title + ": " + b.plain(reason)
	if dl.Valid {
		ref := "dl:" + strconv.FormatInt(dl.Int64, 10)
		e.Related = append(e.Related, ref)
		if d, err := b.loadDownload(dl.Int64); err == nil {
			score := d.score
			e.Release.Indexer, e.Release.Quality, e.Release.Score, e.Release.Size = d.indexer, d.quality, &score,
				d.size.Int64
		}
	}
	if researched.Valid && !b.list {
		// The row is marked whether or not a search was asked for (a library that may not write, a release added by
		// hand). The ask is the acquire.research job router/research.go queues for this row just before the mark,
		// keyed "acquire.research/<library>/<title>/<source>/<download>/<ms>"; it stays recorded while it waits
		// (Pause, the hourly allowance) and until finished jobs are pruned. Only a recorded ask gets a line.
		prefix := router.JobResearch + "/" + e.Version + "/" + strconv.FormatInt(titleID.Int64, 10) + "/" + source + "/" +
			strconv.FormatInt(dl.Int64, 10) + "/"
		var asked bool
		if err := b.db.QueryRowContext(b.ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE kind = ?
			AND substr(dedupe_key, 1, length(?)) = ? AND created_at BETWEEN ? AND ?)`, router.JobResearch, prefix, prefix,
			researched.Int64-60_000, researched.Int64+1000).Scan(&asked); err != nil {
			return err
		}
		if asked {
			e.After = append(e.After, Event{At: store.FromMillis(researched.Int64), Kind: "research",
				Text: "Search again requested"})
		}
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
	e.Label = "Search"
	why := ""
	if failure {
		e.Label, why = "Search after a failure", research.String
	}
	e.Summary = searchWords(source, target, state, why, message, b.full)
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
