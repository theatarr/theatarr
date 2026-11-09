package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

const activityAttemptPreview = 5

type downloadEvidence struct {
	attempts []journal.Attempt
	more     bool
	cache    []journal.Evidence
}

// activityEvidence reads only what the download cards show, in two queries for the complete page. The journal
// detail (decisions, previous files, related events) is loaded when its link is opened. Attempts are limited
// to the latest five plus one row to detect more; Inspect loads the complete history.
func (w *Web) activityEvidence(ctx context.Context, ds []activity.Download) (map[int64]*downloadEvidence, error) {
	out := map[int64]*downloadEvidence{}
	if len(ds) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(ds))
	byHash := map[string][]int64{}
	for _, d := range ds {
		ids = append(ids, d.ID)
		out[d.ID] = &downloadEvidence{}
		byHash[strings.ToLower(d.Hash)] = append(byHash[strings.ToLower(d.Hash)], d.ID)
	}
	b, _ := json.Marshal(ids)
	rows, err := w.st.Reader().QueryContext(ctx, `SELECT a.download_id,a.provider,a.outcome,a.code,a.message,a.retry_at,a.attempted_at
 FROM json_each(?) d JOIN download_attempt a ON a.id IN
  (SELECT id FROM download_attempt WHERE download_id=d.value ORDER BY id DESC LIMIT ?)
 ORDER BY a.id`, string(b), activityAttemptPreview+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		id, a, err := scanActivityAttempt(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out[id].attempts = append(out[id].attempts, a)
	}
	if err := closeActivityRows(rows); err != nil {
		return nil, err
	}
	for _, e := range out {
		if len(e.attempts) > activityAttemptPreview {
			e.more = true
			e.attempts = e.attempts[len(e.attempts)-activityAttemptPreview:]
		}
	}
	hashes := make([]string, 0, len(byHash))
	for hash := range byHash {
		if hash != "" {
			hashes = append(hashes, hash)
		}
	}
	b, _ = json.Marshal(hashes)
	rows, err = w.st.Reader().QueryContext(ctx, `SELECT info_hash,provider,state,source,checked_at,expires_at FROM provider_cache
 WHERE info_hash IN (SELECT value FROM json_each(?)) ORDER BY provider`, string(b))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hash string
		var at int64
		var expiry sql.NullInt64
		var e journal.Evidence
		if err := rows.Scan(&hash, &e.Provider, &e.State, &e.Source, &at, &expiry); err != nil {
			rows.Close()
			return nil, err
		}
		e.CheckedAt = store.FromMillis(at)
		if expiry.Valid {
			v := store.FromMillis(expiry.Int64)
			e.ExpiresAt = &v
		}
		for _, id := range byHash[hash] {
			out[id].cache = append(out[id].cache, e)
		}
	}
	return out, closeActivityRows(rows)
}

func closeActivityRows(rows *sql.Rows) error {
	err := rows.Err()
	if e := rows.Close(); err == nil {
		err = e
	}
	return err
}

func activityMessage(s string) string {
	s = provider.CleanMessage(s)
	if arr.UnsafeValue(s) {
		return "[redacted]"
	}
	return s
}

// ActivityAttempts is the full history loaded when a download's Inspect/Details panel opens.
type ActivityAttempts struct {
	Attempts []journal.Attempt `json:"attempts"`
}

func (w *Web) apiActivityAttempts(rw http.ResponseWriter, r *http.Request) {
	if !w.viewerAdmin(r) {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "provider attempts require see_files permission"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		badRequest(rw, "invalid download id")
		return
	}
	var exists bool
	if err := w.st.Reader().QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM download WHERE id=?)", id).Scan(&exists); err != nil {
		w.internal(rw, err)
		return
	}
	if !exists {
		notFound(rw)
		return
	}
	rows, err := w.st.Reader().QueryContext(r.Context(), `SELECT download_id,provider,outcome,code,message,retry_at,attempted_at FROM download_attempt WHERE download_id=? ORDER BY id`, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	defer rows.Close()
	out := ActivityAttempts{Attempts: []journal.Attempt{}}
	for rows.Next() {
		_, a, err := scanActivityAttempt(rows)
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Attempts = append(out.Attempts, a)
	}
	if err := rows.Err(); err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

func scanActivityAttempt(rows *sql.Rows) (int64, journal.Attempt, error) {
	var id, at int64
	var retry sql.NullInt64
	var a journal.Attempt
	if err := rows.Scan(&id, &a.Provider, &a.Outcome, &a.Code, &a.Message, &retry, &at); err != nil {
		return 0, a, err
	}
	a.Message = activityMessage(a.Message)
	a.At = store.FromMillis(at)
	if retry.Valid {
		v := store.FromMillis(retry.Int64)
		a.RetryAt = &v
	}
	return id, a, nil
}
