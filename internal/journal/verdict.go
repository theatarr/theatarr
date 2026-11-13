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
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Verdicts.
const (
	VerdictOK    = "ok"
	VerdictWrong = "wrong"
)

// Verdict actions.
const (
	ActionRecorded    = "recorded"
	ActionBlocklisted = "blocklisted"
)

// BlocklistJournal is the router_blocklist source of a "wrong" verdict.
const BlocklistJournal = "journal"

// ResearchJournal is the acquire.research reason of a "wrong" verdict.
const ResearchJournal = "journal"

// ErrBadVerdict is a verdict request the journal refuses.
var ErrBadVerdict = errors.New("journal: bad verdict")

// ErrNotMigrated is a database without the journal's tables (migration 00024).
var ErrNotMigrated = errors.New("journal: the database lacks migration 00024 (journal_verdict)")

// VerdictRequest is the body of POST /journal/{id}/verdict.
type VerdictRequest struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// verdict loads an entry's latest verdict and acknowledges the flags it covered.
func (b *builder) verdict(e *Entry) error {
	if !b.own {
		return nil
	}
	v, err := b.loadVerdict(`entry_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, e.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e.Verdict = v
	for i := range e.Flags {
		if slices.Contains(v.Acked, e.Flags[i].Code) {
			e.Flags[i].Acked = true
		}
	}
	e.After = append(e.After, Event{At: v.At, Kind: "verdict", Text: v.Verdict + ": " + v.Reason,
		Ref: "v:" + strconv.FormatInt(v.ID, 10)})
	return nil
}

func (b *builder) loadVerdict(where string, args ...any) (*Verdict, error) {
	v := &Verdict{}
	var acked string
	var job sql.NullInt64
	var at int64
	err := b.db.QueryRowContext(b.ctx, `SELECT id, entry_id, verdict, reason, acked, action, action_note, research_job,
		by, created_at FROM journal_verdict WHERE `+where, args...).Scan(&v.ID, &v.Entry, &v.Verdict, &v.Reason, &acked,
		&v.Action, &v.Note, &job, &v.By, &at)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(acked), &v.Acked)
	v.ResearchJob, v.At = job.Int64, store.FromMillis(at)
	return v, nil
}

// verdictEntry is a verdict's own entry.
func (b *builder) verdictEntry(e *Entry, rest string) error {
	if !b.own {
		return sql.ErrNoRows
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	v, err := b.loadVerdict(`id = ?`, id)
	if err != nil {
		return err
	}
	var hash string
	_ = b.db.QueryRowContext(b.ctx, `SELECT info_hash FROM journal_verdict WHERE id = ?`, id).Scan(&hash)
	e.Source, e.Verdict, e.Judged = "journal", v, v.Entry
	e.Related = []string{v.Entry}
	if hash != "" {
		e.Release = &Release{InfoHash: hash}
	}
	e.Summary = fmt.Sprintf("%s on %s by %s: %s (%s)", v.Verdict, v.Entry, orDash(v.By), v.Reason, v.Action)
	if v.Note != "" {
		e.Summary += "; " + v.Note
	}
	return nil
}

// Judge records a verdict on an entry. Both verdicts acknowledge the entry's current flags. "wrong" on an entry
// with a release hash, for a version that is active while router.enabled is true, also blocklists the hash for the
// version (router_blocklist, source journal) and queues acquire.research for the title (and the entry's episodes),
// in one transaction with the verdict. In shadow mode, or with the router off, the verdict is only recorded.
// Nothing is ever deleted, on a provider or anywhere else.
func (j *Journal) Judge(ctx context.Context, id string, req VerdictRequest, by string) (*Verdict, error) {
	req.Verdict = strings.ToLower(strings.TrimSpace(req.Verdict))
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Verdict != VerdictOK && req.Verdict != VerdictWrong {
		return nil, fmt.Errorf("%w: verdict must be ok or wrong", ErrBadVerdict)
	}
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > 1000 {
		return nil, fmt.Errorf("%w: reason is required (at most 1000 characters)", ErrBadVerdict)
	}
	req.Reason = clean(req.Reason)
	if !j.hasOwnTables(ctx) {
		return nil, ErrNotMigrated
	}
	e, err := j.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.Kind == KindVerdict {
		return nil, fmt.Errorf("%w: a verdict cannot be judged; judge %s", ErrBadVerdict, e.Judged)
	}
	acked := []string{}
	for _, f := range e.Flags {
		if !slices.Contains(acked, f.Code) {
			acked = append(acked, f.Code)
		}
	}
	ackJSON, _ := json.Marshal(acked)
	hash := ""
	if e.Release != nil {
		hash = strings.ToLower(e.Release.InfoHash)
	}
	v := &Verdict{Entry: e.ID, Verdict: req.Verdict, Reason: req.Reason, Acked: acked, Action: ActionRecorded, By: by,
		At: j.now().UTC()}
	act, why := j.mayAct(j.policy(ctx), e, hash)
	if req.Verdict == VerdictWrong && !act {
		v.Note = why
	}
	err = j.st.WriteTx(ctx, func(tx *sql.Tx) error {
		v.Action, v.ResearchJob = ActionRecorded, 0
		if req.Verdict == VerdictWrong && act {
			if err := j.wrong(ctx, tx, e, hash, req.Reason, v); err != nil {
				return err
			}
		}
		var job any
		if v.ResearchJob > 0 {
			job = v.ResearchJob
		}
		var title any
		if e.Title.ID > 0 {
			title = e.Title.ID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO journal_verdict (entry_id, verdict, reason, acked, title_id,
			version_key, info_hash, action, action_note, research_job, by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.ID, v.Verdict, v.Reason, string(ackJSON), title, e.Version,
			hash, v.Action, v.Note, job, by, store.Millis(v.At))
		if err != nil {
			return err
		}
		v.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("journal: verdict: %w", err)
	}
	j.log.Info("journal verdict", "entry", e.ID, "verdict", v.Verdict, "action", v.Action, "research_job",
		v.ResearchJob, "by", by)
	return v, nil
}

// mayAct says whether a "wrong" verdict on e blocklists and searches again, and why not.
func (j *Journal) mayAct(pol *policy.Policy, e *Entry, hash string) (bool, string) {
	var vp *policy.VersionPolicy
	if pol != nil {
		vp = pol.Versions[e.Version]
	}
	switch {
	case vp == nil:
		return false, "recorded only: the policy has no version " + orDash(e.Version)
	case vp.Mode != policy.ModeActive:
		return false, "recorded only: version " + e.Version + " is Watch only"
	case !pol.RouterSettings().Enabled:
		return false, "recorded only: router.enabled is false"
	case hash == "":
		return false, "recorded only: the entry has no release hash to blocklist"
	case e.Title.ID == 0:
		return false, "recorded only: the entry has no title"
	}
	return true, ""
}

// wrong blocklists the hash for the version and queues the re-search, inside tx.
func (j *Journal) wrong(ctx context.Context, tx *sql.Tx, e *Entry, hash, reason string, v *Verdict) error {
	var dl int64
	if e.Download != nil {
		dl = e.Download.ID
	}
	release := ""
	if e.Release != nil {
		release = e.Release.Title
	}
	if err := router.Blocklist(ctx, tx, e.Version, hash, e.Title.ID, dl, release,
		"journal verdict on "+e.ID+": "+reason, BlocklistJournal, store.Millis(v.At)); err != nil {
		return err
	}
	// Mark it researched: this verdict queues its own search, so the poller must not queue another.
	if _, err := tx.ExecContext(ctx, `UPDATE router_blocklist SET researched_at = ? WHERE version_key = ?
		AND info_hash = ?`, store.Millis(v.At), e.Version, hash); err != nil {
		return err
	}
	v.Action = ActionBlocklisted
	v.Note = "blocklisted " + shortHash(hash) + " for " + e.Version
	var versionID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, e.Title.ID,
		e.Version).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) || j.q == nil {
		v.Note += "; no re-search queued (no version row or no job queue)"
		return nil
	}
	if err != nil {
		return err
	}
	p := router.ResearchRequest{TitleID: e.Title.ID, VersionID: versionID, Version: e.Version,
		Episodes: e.f.episodeIDs, Reason: ResearchJournal, DownloadID: v.downloadID(e)}
	key := router.JobResearch + "/" + e.Version + "/" + strconv.FormatInt(e.Title.ID, 10) + "/journal/" + e.ID +
		"/" + strconv.FormatInt(store.Millis(v.At), 10)
	job, _, err := j.q.EnqueueTx(ctx, tx, router.JobResearch, p, jobs.EnqueueOptions{DedupeKey: key, MaxAttempts: 1})
	if err != nil {
		return err
	}
	v.ResearchJob = job
	v.Note += fmt.Sprintf("; re-search queued (job %d; it starts under the hourly search limit)", job)
	return nil
}

func (v *Verdict) downloadID(e *Entry) int64 {
	if e.Download != nil {
		return e.Download.ID
	}
	return 0
}
