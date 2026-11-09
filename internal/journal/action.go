package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/store"
)

// Operator actions (migration 00039, docs/design/ui.md "Recovery actions"): what a person did by hand through the web
// UI. The web layer records one row per action after the change it made; the journal lists each as an entry of kind
// action (id act:<id>), with who did it, so an unblock, which deletes its blocklist row, still leaves a trace.

// Action names.
const (
	ActGrab           = "grab"            // a chosen release from a live search was routed
	ActRemoveDownload = "remove_download" // a download's record ended (optionally blocklisted, searched again)
	ActRetryDownload  = "retry_download"  // a failed download routed again, or a stuck import run now
	ActUnblock        = "unblock"         // a blocklisted release may be routed again
	ActRemoveVersion  = "remove_version"  // a copy left the library (unmonitored, hidden from every view)
	ActRemoveTitle    = "remove_title"    // every copy of a title left the library
	ActUnlinkFile     = "unlink_file"     // one of Theatarr's library symlinks moved to the trash
)

// Action is one operator action.
type Action struct {
	ID         int64     `json:"id"`
	Action     string    `json:"action"`
	TitleID    int64     `json:"title_id,omitempty"`
	Version    string    `json:"version,omitempty"`
	InfoHash   string    `json:"info_hash,omitempty"`
	Release    string    `json:"release,omitempty"`
	DownloadID int64     `json:"download_id,omitempty"`
	Target     string    `json:"target,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	By         string    `json:"by,omitempty"`
	At         time.Time `json:"at"`
}

const actionHeadSQL = `
UNION ALL
SELECT 'act:' || a.id, a.created_at, COALESCE(a.title_id, 0), a.version_key, 'action' FROM journal_action a`

// hasActions reports whether the database has journal_action (migration 00039).
func (j *Journal) hasActions(ctx context.Context) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.acts != nil {
		return *j.acts
	}
	var n int
	if err := j.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'
		AND name = 'journal_action'`).Scan(&n); err != nil {
		return false
	}
	ok := n == 1
	j.acts = &ok
	return ok
}

// trimText keeps free text short and clean for the audit row.
func trimText(s string, n int) string {
	s = clean(strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// RecordAction stores one operator action inside the caller's transaction; At defaults to now. The id is returned.
func RecordAction(ctx context.Context, tx *sql.Tx, a Action) (int64, error) {
	if a.Action == "" {
		return 0, errors.New("journal: action is required")
	}
	if a.At.IsZero() {
		a.At = time.Now()
	}
	var title, dl any
	if a.TitleID > 0 {
		title = a.TitleID
	}
	if a.DownloadID > 0 {
		dl = a.DownloadID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO journal_action (action, title_id, version_key, info_hash,
		release_title, download_id, target, detail, by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Action, title, a.Version, strings.ToLower(a.InfoHash), trimText(a.Release, 500), dl, trimText(a.Target, 1000),
		trimText(a.Detail, 1000), trimText(a.By, 200), store.Millis(a.At))
	if err != nil {
		return 0, fmt.Errorf("journal: action: %w", err)
	}
	return res.LastInsertId()
}

// Record stores one operator action in its own transaction, timed by the journal's clock.
func (j *Journal) Record(ctx context.Context, a Action) (int64, error) {
	if !j.hasActions(ctx) {
		return 0, errors.New("journal: the database lacks migration 00039 (journal_action)")
	}
	if a.At.IsZero() {
		a.At = j.now()
	}
	var id int64
	err := j.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = RecordAction(ctx, tx, a)
		return err
	})
	return id, err
}

var actionWords = map[string]string{
	ActGrab:           "grabbed by hand",
	ActRemoveDownload: "download removed",
	ActRetryDownload:  "download retried",
	ActUnblock:        "release unblocked",
	ActRemoveVersion:  "copy removed",
	ActRemoveTitle:    "title removed",
	ActUnlinkFile:     "file unlinked",
}

// actionEntry is an operator action's own entry.
func (b *builder) actionEntry(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	a := &Action{ID: id}
	var title, dl sql.NullInt64
	var at int64
	if err := b.db.QueryRowContext(b.ctx, `SELECT action, title_id, version_key, info_hash, release_title, download_id,
		target, detail, by, created_at FROM journal_action WHERE id = ?`, id).Scan(&a.Action, &title, &a.Version,
		&a.InfoHash, &a.Release, &dl, &a.Target, &a.Detail, &a.By, &at); err != nil {
		return err
	}
	a.TitleID, a.DownloadID, a.At = title.Int64, dl.Int64, store.FromMillis(at)
	e.Source, e.Action = "web", a
	if a.Release != "" || a.InfoHash != "" {
		e.Release = &Release{Title: a.Release, InfoHash: a.InfoHash}
	}
	if a.DownloadID > 0 {
		e.Related = append(e.Related, "dl:"+strconv.FormatInt(a.DownloadID, 10))
	}
	words := actionWords[a.Action]
	if words == "" {
		words = a.Action
	}
	e.Summary = words + " by " + orDash(a.By)
	if a.Detail != "" {
		e.Summary += ": " + a.Detail
	}
	return nil
}
