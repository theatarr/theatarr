package importarr

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/router"
)

// fetchBlocklist reads every page on every import: unlike history, old entries can change or disappear.
func fetchBlocklist(ctx context.Context, g Getter, s *Snapshot) error {
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(historyPageSize)},
			"sortKey": {"id"}, "sortDirection": {"ascending"}}
		var p struct {
			TotalRecords int `json:"totalRecords"`
			Records      []struct {
				historyRecord
				Protocol        string `json:"protocol"`
				TorrentInfoHash string `json:"torrentInfoHash"`
			} `json:"records"`
		}
		if err := g.GetJSON(ctx, "blocklist", q, &p); err != nil {
			return err
		}
		for _, r := range p.Records {
			if r.Data == nil {
				r.Data = map[string]any{}
			}
			if r.Protocol != "" {
				r.Data["protocol"] = r.Protocol
			}
			if r.TorrentInfoHash != "" {
				r.Data["torrentInfoHash"] = r.TorrentInfoHash
			}
			e := toEvent(r.historyRecord)
			if e.InfoHash != "" || strings.TrimSpace(e.SourceTitle) != "" {
				s.Blocklist = append(s.Blocklist, BlockedRelease{ArrTitleID: e.ArrTitleID, InfoHash: e.InfoHash, SourceTitle: e.SourceTitle})
			}
		}
		if len(p.Records) == 0 || page*historyPageSize >= p.TotalRecords {
			return nil
		}
	}
}

// followerBlocklist mirrors the arr's list, without turning its failure reasons into new policy decisions.
// Local blocks take precedence and are never removed by an arr import.
func (w *wtx) followerBlocklist(ctx context.Context, src Source, snap *Snapshot, titles map[int]titleRef) error {
	for _, version := range src.Followers {
		live := map[string]bool{}
		for _, b := range snap.Blocklist {
			infoHash := b.InfoHash
			if infoHash == "" && b.SourceTitle != "" {
				// Blocklist resources omit torrent hashes. A grabbed history row is the arr's source of truth;
				// retain the title key when no matching torrent history exists (usenet/manual entries included).
				var historyHash string
				err := w.tx.QueryRowContext(ctx, `SELECT info_hash FROM arr_history
					WHERE instance = ? AND arr_title_id = ? AND event_type = ? AND source_title = ?
					AND info_hash IS NOT NULL AND info_hash <> '' ORDER BY date_at DESC, arr_id DESC LIMIT 1`,
					src.Instance, b.ArrTitleID, EventGrabbed, b.SourceTitle).Scan(&historyHash)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil {
					infoHash = historyHash
				}
			}
			key := router.BlocklistKey(infoHash, b.SourceTitle)
			if key == "" {
				continue
			}
			live[key] = true
			var titleID any
			if ref, ok := titles[b.ArrTitleID]; ok {
				titleID = ref.titleID
			}
			if _, err := w.upsert(ctx, "arr_blocklist", []kv{{"instance", src.Instance}, {"version_key", version}, {"info_hash", key}},
				[]kv{{"title_id", titleID}, {"release_title", b.SourceTitle}}, nil, nil); err != nil {
				return err
			}
		}
		rows, err := w.tx.QueryContext(ctx, `SELECT info_hash FROM arr_blocklist WHERE version_key = ? AND instance = ?`, version, src.Instance)
		if err != nil {
			return err
		}
		var gone []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			if !live[key] {
				gone = append(gone, key)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, key := range gone {
			n, err := w.exec(ctx, `DELETE FROM arr_blocklist WHERE version_key = ? AND info_hash = ? AND instance = ?`, version, key, src.Instance)
			if err != nil {
				return err
			}
			w.changes.add("arr_blocklist", opDelete, int(n))
		}
		if err := w.syncFollowerBlocklist(ctx, version); err != nil {
			return err
		}
	}
	return nil
}

func (w *wtx) syncFollowerBlocklist(ctx context.Context, version string) error {
	type block struct {
		instance, release string
		title             sql.NullInt64
	}
	want := map[string]block{}
	rows, err := w.tx.QueryContext(ctx, `SELECT info_hash, instance, release_title, title_id FROM arr_blocklist
		WHERE version_key = ? ORDER BY instance`, version)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		var b block
		if err := rows.Scan(&key, &b.instance, &b.release, &b.title); err != nil {
			rows.Close()
			return err
		}
		if _, ok := want[key]; !ok {
			want[key] = b
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = w.tx.QueryContext(ctx, `SELECT info_hash, source FROM router_blocklist WHERE version_key = ?`, version)
	if err != nil {
		return err
	}
	have := map[string]string{}
	for rows.Next() {
		var key, source string
		if err := rows.Scan(&key, &source); err != nil {
			rows.Close()
			return err
		}
		have[key] = source
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for key, source := range have {
		if _, ok := want[key]; ok || !strings.HasPrefix(source, "arr:") {
			continue
		}
		n, err := w.exec(ctx, `DELETE FROM router_blocklist WHERE version_key = ? AND info_hash = ?`, version, key)
		if err != nil {
			return err
		}
		w.changes.add("router_blocklist", opDelete, int(n))
	}
	for key, b := range want {
		if source, ok := have[key]; ok && !strings.HasPrefix(source, "arr:") {
			continue
		}
		var titleID any
		if b.title.Valid {
			titleID = b.title.Int64
		}
		if _, err := w.upsert(ctx, "router_blocklist", []kv{{"version_key", version}, {"info_hash", key}},
			[]kv{{"title_id", titleID}, {"release_title", b.release}, {"reason", "arr blocklist (" + b.instance + ")"},
				{"source", "arr:" + b.instance}}, []kv{{"created_at", w.now}}, nil); err != nil {
			return err
		}
	}
	return nil
}
