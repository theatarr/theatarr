package router

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// Hooks for the triggers outside this package (internal/acquire, internal/shadow): read-only helpers. None of them
// writes to a provider; the only ways to a provider stay Route and the poller, through Gate.

// InFlight reports whether a version already has a download in hand (queued to importing) for a title: for a movie
// version any such download; for a series version one that holds any of episodes (every in-flight download when
// episodes is empty). A trigger skips such a target instead of searching again. db may be the reader pool.
func InFlight(ctx context.Context, db *sql.DB, titleID, versionID int64, episodes []int64) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT episodes FROM download WHERE title_id = ? AND version_id = ?
		AND state IN `+inFlightStates, titleID, versionID)
	if err != nil {
		return false, fmt.Errorf("router: in-flight check: %w", err)
	}
	defer rows.Close()
	want := make(map[int64]bool, len(episodes))
	for _, e := range episodes {
		want[e] = true
	}
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return false, err
		}
		if len(episodes) == 0 {
			return true, nil
		}
		var held []int64
		if json.Unmarshal([]byte(js), &held) != nil || len(held) == 0 {
			return true, nil // a download with no episode list holds the whole title, as far as we know
		}
		for _, e := range held {
			if want[e] {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// PlanSummary says in one line what a route result or a recorded plan means: "would add X on P", "would wait for a
// provider", or why nothing would be added.
func PlanSummary(outcome, release, provider string, steps []Step) string {
	switch outcome {
	case string(OutcomeWouldAdd), string(OutcomeAdded):
		verb := "would add"
		if outcome == string(OutcomeAdded) {
			verb = "added"
		}
		for _, s := range steps {
			if s.Provider == provider && s.Code == "reused" {
				return fmt.Sprintf("%s %s on %s (the provider holds it already)", verb, release, provider)
			}
		}
		return fmt.Sprintf("%s %s on %s", verb, release, provider)
	case string(OutcomeDeferred):
		return fmt.Sprintf("would wait to add %s: every usable provider is paced or backing off", release)
	case string(OutcomeInFlight):
		return fmt.Sprintf("%s is in flight already", release)
	case string(OutcomeCancelled):
		return "route cancelled: no release is still wanted"
	}
	if len(steps) == 0 {
		return "would add nothing: no accepted release"
	}
	var codes []string
	for _, s := range steps {
		if s.Outcome == outcomeOrdered {
			continue
		}
		c := s.Code
		if c == "" {
			c = s.Outcome
		}
		if s.Provider != "" && c != "miss" {
			c = s.Provider + " " + c
		}
		if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	if len(codes) > 6 {
		codes = append(codes[:6], "…")
	}
	return "would add nothing: " + strings.Join(codes, ", ")
}

// Blocklist sources.
const BlocklistLibrary = "library"

// BlocklistKey uses the hash when known; hashless entries use a disjoint, stable title key in info_hash.
// The prefix keeps title keys separate from torrent hashes.
func BlocklistKey(hash, release string) string {
	if hash = strings.ToLower(strings.TrimSpace(hash)); hash != "" {
		return hash
	}
	if release = strings.ToLower(strings.TrimSpace(release)); release != "" {
		return fmt.Sprintf("title:%x", sha256.Sum256([]byte(release)))
	}
	return ""
}

// Blocklist records that version must never route hash again (the library's guard rejected its content), inside
// the caller's transaction. It writes only our database; nothing is removed on a provider.
func Blocklist(ctx context.Context, tx *sql.Tx, version, hash string, titleID, downloadID int64, release, reason,
	source string, at int64) error {
	hash = BlocklistKey(hash, release)
	if hash == "" || version == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title,
		reason, source, download_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (version_key, info_hash) DO UPDATE SET reason = excluded.reason, source = excluded.source,
		download_id = excluded.download_id, created_at = excluded.created_at`,
		version, hash, nullID(titleID), release, reason, source, nullID(downloadID), at)
	return err
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// blocklisted says why a version must not route hash, or "".
func (r *Router) blocklisted(ctx context.Context, version, hash, release string) (string, error) {
	var reason, source string
	err := r.st.Reader().QueryRowContext(ctx, `SELECT reason, source FROM router_blocklist WHERE version_key = ?
		AND info_hash IN (?, ?) ORDER BY info_hash LIMIT 1`, version, hash, BlocklistKey("", release)).Scan(&reason, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("router: blocklist: %w", err)
	}
	return "blocklisted for " + version + " by the " + source + ": " + reason, nil
}

// Blocklist sources used by the arr views.
const (
	BlocklistQueue   = "queue"
	BlocklistHistory = "history"
)

// Dismissed is a download Dismiss ended.
type Dismissed struct {
	ID      int64
	TitleID int64
	Version string
	Hash    string
	Release string
	// State is the state the download had; Ended is false when it was already failed or imported (nothing changed
	// but the blocklist).
	State string
	Ended bool
}

// Dismiss ends a download in our database only (an arr client removed it from the queue or marked it failed): a
// download still in hand becomes failed with the reason; with block, its hash is blocklisted for the version. A
// failed or imported download keeps its state. The route jobs queued to resume it are cancelled in the same
// transaction, and a route already running finds it ended at its next write (transition): an ended download is never
// queued or added again, and no new download replaces it. Nothing is sent to a provider and
// nothing is deleted there (debrid torrents are shared by every library). sql.ErrNoRows when there is no such
// download.
func Dismiss(ctx context.Context, st *store.Store, id int64, reason string, block bool, source string,
	at int64) (*Dismissed, error) {
	var d *Dismissed
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		d, err = dismissTx(ctx, tx, id, reason, block, source, at)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// dismissTx is Dismiss inside tx.
func dismissTx(ctx context.Context, tx *sql.Tx, id int64, reason string, block bool, source string,
	at int64) (*Dismissed, error) {
	d := &Dismissed{ID: id}
	if err := tx.QueryRowContext(ctx, `SELECT title_id, version_key, info_hash, release_title, state FROM download
		WHERE id = ?`, id).Scan(&d.TitleID, &d.Version, &d.Hash, &d.Release, &d.State); err != nil {
		return nil, err
	}
	if d.State != StateFailed && d.State != StateImported {
		if _, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ?
			WHERE id = ?`, reason, at, id); err != nil {
			return nil, err
		}
		d.Ended = true
	}
	key := routeJobKey(id)
	if _, err := tx.ExecContext(ctx, `UPDATE job SET state = 'done', last_error = ?, finished_at = ?,
		updated_at = ? WHERE kind = ? AND state = 'queued' AND substr(dedupe_key, 1, ?) = ?`,
		"cancelled: download "+strconv.FormatInt(id, 10)+" was ended ("+reason+")", at, at, JobRoute, len(key),
		key); err != nil {
		return nil, err
	}
	if block {
		if err := Blocklist(ctx, tx, d.Version, d.Hash, d.TitleID, id, d.Release, reason, source, at); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// EndRemoved ends the downloads in hand of a title's removed versions (by policy key), as Dismiss does each: they
// become failed with reason in our records, their queued route jobs are cancelled, and the provider keeps its
// torrents. The web UI's remove and an arr view's DELETE run it in the transaction that hides the versions:
// a removed copy's download is never imported back into the library, an import running meanwhile finds its download
// ended at its commit and takes back the links it made (library.ErrRemoved), and the remove never half-succeeds. It
// returns how many downloads it ended.
func EndRemoved(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, reason string, at int64) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	args := []any{titleID}
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.id FROM download d JOIN version v ON v.id = d.version_id
		WHERE v.title_id = ? AND v.policy_key IN (`+store.Placeholders(len(keys))+`)
		AND d.state NOT IN ('failed', 'imported') ORDER BY d.id`, args...)
	if err != nil {
		return 0, fmt.Errorf("router: removed downloads: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		d, err := dismissTx(ctx, tx, id, reason, false, "", at)
		if err != nil {
			return n, err
		}
		if d.Ended {
			n++
		}
	}
	return n, nil
}
