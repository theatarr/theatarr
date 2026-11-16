package journal

import (
	"cmp"
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

// actionHeadSQL is the actions' part of the list; a hand unlink's parent is its library action (the same file,
// title and library, recorded just before it).
const actionHeadSQL = `
UNION ALL
SELECT 'act:' || a.id, a.created_at, COALESCE(a.title_id, 0), a.version_key, 'action',
	CASE WHEN a.action = 'unlink_file' THEN (` + unlinkLibSQL + `) END FROM journal_action a`

// unlinkLibSQL is the library action of a hand unlink (alias a): the replace without a download of its file, title
// and library, within the minute before it.
const unlinkLibSQL = `SELECT 'lib:' || la.id FROM library_action la WHERE la.at BETWEEN a.created_at - 60000
	AND a.created_at AND la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NULL AND la.path = a.target
	AND COALESCE(la.version_key, '') = a.version_key AND ` + libTitleSQL + ` = COALESCE(a.title_id, 0)
	ORDER BY la.id DESC LIMIT 1`

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
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if a.Action == ActRemoveTitle || a.Action == ActRemoveVersion {
		store.HistoryRemoval(ctx, tx, id)
	}
	return id, nil
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

// actionWords are each action's label and the start of its summary (the release or file is added after it).
var actionWords = map[string][2]string{
	ActGrab:           {"Grabbed by hand", "Grabbed by hand"},
	ActRemoveDownload: {"Download removed", "Removed the download"},
	ActRetryDownload:  {"Retried", "Retried"},
	ActUnblock:        {"Unblocked", "Unblocked"},
	ActRemoveVersion:  {"Removed", "Removed from the library"},
	ActRemoveTitle:    {"Title removed", "Removed the title"},
	ActUnlinkFile:     {"Removed", "Removed"},
}

// actionDetail is an action's detail in plain words (in full for the entry page).
func actionDetail(a *Action, full bool) string {
	switch a.Action {
	case ActRemoveVersion, ActRemoveTitle:
		r := ParseRemovalDetail(a.Detail)
		files := map[string]string{RemovalFilesKept: "files kept", RemovalFilesDeleted: "files moved to the trash",
			RemovalFilesPartial: "some files moved to the trash"}[r.Files]
		left := ""
		if r.Left > 0 {
			left = fmt.Sprintf("%d left in place: %s", r.Left, plainText(r.Reason, full))
		}
		cleanup := ""
		if r.Cleanup != "" {
			cleanup = "debrid clean-up: " + plainText(r.Cleanup, full)
		}
		return join(files, left, cleanup)
	case ActUnlinkFile:
		d := plainText(a.Detail, full)
		if strings.HasSuffix(d, " when its library was removed") {
			return join(strings.TrimSuffix(d, " when its library was removed"), "library removed")
		}
		return d
	case ActGrab:
		d := strings.Replace(a.Detail, "routed although rejected", "although rejected", 1)
		return plainText(strings.TrimSuffix(strings.TrimSuffix(d, "routed"), ": "), full)
	}
	return plainText(a.Detail, full)
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
	words, ok := actionWords[a.Action]
	if !ok {
		words = [2]string{a.Action, a.Action}
	}
	what := clean(a.Release)
	if a.Action == ActUnlinkFile {
		what = baseName(a.Target)
		if a.Target != "" {
			e.Paths = []string{a.Target}
		}
	}
	head := words[1]
	if what != "" {
		head += " " + what
	}
	e.Label = words[0]
	e.Summary = join(head, "by "+orDash(a.By), actionDetail(a, b.full))
	return nil
}

// Removal is what a title removal (remove_title, remove_version: one action per library) did, kept in the action's
// detail (RemovalDetail) and read back for History (ParseRemovalDetail).
type Removal struct {
	// Files is what became of the library's files: RemovalFilesKept (they stay linked), RemovalFilesDeleted (taken
	// out of the library), RemovalFilesPartial (some left in place), or "" when the outcome is not known (a removal
	// recorded before outcomes were, or one whose unlinking did not finish).
	Files string `json:"files,omitempty"`
	// Left is how many library files were left in place although the files were to be deleted, and Reason why (the
	// first refusal or error).
	Left   int    `json:"left,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Cleanup is the debrid clean-up's outcome per provider, in words ("deleted on RD, TB; kept on AD: used by
	// <title>", "AD: failed, <reason>"); empty when none ran. Written into the action after the removal, by its id
	// (SetActionDetail).
	Cleanup string `json:"cleanup,omitempty"`
}

// Removal files.
const (
	RemovalFilesKept    = "kept"
	RemovalFilesDeleted = "deleted"
	RemovalFilesPartial = "partial"
)

// The parts of a removal's detail after its files.
const (
	removalCleanup = "; clean-up: "
	removalLeft    = "; left in place: "
)

// RemovalDetail is the action detail that records r: "files kept|deleted|partial|unknown", then "; left in place: <n>:
// <reason>" when files were left, then "; clean-up: <outcome>" when a clean-up ran.
func RemovalDetail(r Removal) string {
	d := "files " + cmp.Or(r.Files, "unknown")
	if r.Left > 0 {
		d += removalLeft + strconv.Itoa(r.Left) + ": " + r.Reason
	}
	if r.Cleanup != "" {
		d += removalCleanup + r.Cleanup
	}
	return d
}

// ParseRemovalDetail reads a removal action's detail (RemovalDetail). Removals recorded before it read "files left
// linked" (kept) or "its library links go to the trash next ..." (outcome unknown).
func ParseRemovalDetail(detail string) Removal {
	head, cleanup, _ := strings.Cut(detail, removalCleanup)
	files, left, _ := strings.Cut(head, removalLeft)
	r := Removal{Cleanup: cleanup}
	switch files {
	case "files left linked":
		r.Files = RemovalFilesKept
	case "files " + RemovalFilesKept, "files " + RemovalFilesDeleted, "files " + RemovalFilesPartial:
		r.Files = strings.TrimPrefix(files, "files ")
	}
	if n, why, ok := strings.Cut(left, ": "); ok {
		r.Left, _ = strconv.Atoi(n)
		r.Reason = why
	}
	return r
}

// SetActionDetail replaces a recorded action's detail (a removal's outcome once known), inside the caller's
// transaction.
func SetActionDetail(ctx context.Context, tx *sql.Tx, id int64, detail string) error {
	detail = trimText(detail, 1000)
	if _, err := tx.ExecContext(ctx, `UPDATE journal_action SET detail = ? WHERE id = ?`, detail, id); err != nil {
		return err
	}
	store.HistoryRemovalDetail(ctx, tx, id, detail)
	return nil
}
