package importarr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
)

// seedRegistry seeds the content registry from the instance's stored history (docs/design/library.md):
// one content per info hash, and one content_version per hash and version with the grab's release title
// and the first grab, import and failure. It works from everything stored, so a re-run converges on the same rows.
// It returns the content id per hash for the instance's hashes.
func (w *wtx) seedRegistry(ctx context.Context, instance string, sum *Summary) (map[string]int64, error) {
	type pair struct {
		releaseTitle, importTitle string
		grabbed, imported, failed int64
	}
	type agg struct {
		name, fallbackName string
		size               int64
		first, last        int64
		versions           map[int64]*pair
	}
	byHash := map[string]*agg{}
	rows, err := w.tx.QueryContext(ctx, `SELECT info_hash, event_type, date_at, version_id, source_title, data
		FROM arr_history WHERE instance = ? AND info_hash IS NOT NULL AND event_type IN (?, ?, ?)
		ORDER BY date_at, arr_id`, instance, EventGrabbed, EventImported, EventFailed)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hash, et, title, data string
		var at int64
		var vid sql.NullInt64
		if err := rows.Scan(&hash, &et, &at, &vid, &title, &data); err != nil {
			rows.Close()
			return nil, err
		}
		a := byHash[hash]
		if a == nil {
			a = &agg{first: at, versions: map[int64]*pair{}}
			byHash[hash] = a
		}
		a.last = at
		if a.fallbackName == "" {
			a.fallbackName = title
		}
		if et == EventGrabbed {
			if a.name == "" {
				a.name = title
			}
			if a.size == 0 {
				var d map[string]string
				_ = json.Unmarshal([]byte(data), &d)
				a.size, _ = strconv.ParseInt(d["size"], 10, 64)
			}
		}
		if !vid.Valid {
			continue
		}
		p := a.versions[vid.Int64]
		if p == nil {
			p = &pair{}
			a.versions[vid.Int64] = p
		}
		switch et {
		case EventGrabbed:
			if p.grabbed == 0 {
				p.grabbed, p.releaseTitle = at, title
			}
		case EventImported:
			if p.imported == 0 {
				p.imported, p.importTitle = at, title
			}
		case EventFailed:
			if p.failed == 0 {
				p.failed = at
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hashes := make([]string, 0, len(byHash))
	for h := range byHash {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	ids := map[string]int64{}
	reg := &sum.Registry
	reg.Hashes = len(hashes)
	for _, h := range hashes {
		a := byHash[h]
		name := a.name
		if name == "" {
			name = a.fallbackName
		}
		id, err := w.content(ctx, h, name, a.size, a.first, a.last)
		if err != nil {
			return nil, err
		}
		ids[h] = id
		vids := make([]int64, 0, len(a.versions))
		for v := range a.versions {
			vids = append(vids, v)
		}
		sort.Slice(vids, func(i, j int) bool { return vids[i] < vids[j] })
		for _, v := range vids {
			p := a.versions[v]
			title := p.releaseTitle
			if title == "" {
				title = p.importTitle
			}
			if err := w.contentVersion(ctx, id, v, title, instance, p.grabbed, p.imported, p.failed); err != nil {
				return nil, err
			}
			reg.ContentVersions++
			switch {
			case p.imported != 0:
				reg.Imported++
			case p.failed != 0:
				reg.Failed++
			default:
				reg.GrabbedOnly++
			}
		}
	}
	return ids, nil
}

// content finds or creates the content for hash. It fills an empty name or size and widens the seen dates; it never
// overwrites what the registry learnt elsewhere (provider lists, the mount).
func (w *wtx) content(ctx context.Context, hash, name string, size, first, last int64) (int64, error) {
	var (
		id                int64
		curName           string
		curSize           sql.NullInt64
		curFirst, curLast int64
	)
	err := w.tx.QueryRowContext(ctx, `SELECT id, name, size_bytes, first_seen_at, last_seen_at FROM content
		WHERE info_hash = ?`, hash).Scan(&id, &curName, &curSize, &curFirst, &curLast)
	if errors.Is(err, sql.ErrNoRows) {
		var sz any
		if size > 0 {
			sz = size
		}
		res, err := w.tx.ExecContext(ctx, `INSERT INTO content (info_hash, name, size_bytes, state, first_seen_at,
			last_seen_at) VALUES (?, ?, ?, 'unknown', ?, ?)`, hash, name, sz, first, last)
		if err != nil {
			return 0, err
		}
		w.changes.add("content", opInsert, 1)
		return res.LastInsertId()
	}
	if err != nil {
		return 0, err
	}
	var sets []kv
	if curName == "" && name != "" {
		sets = append(sets, kv{"name", name})
	}
	if !curSize.Valid && size > 0 {
		sets = append(sets, kv{"size_bytes", size})
	}
	if first < curFirst {
		sets = append(sets, kv{"first_seen_at", first})
	}
	if last > curLast {
		sets = append(sets, kv{"last_seen_at", last})
	}
	if len(sets) > 0 {
		if _, err := w.upsert(ctx, "content", []kv{{"id", id}}, sets, nil, nil); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// contentVersion merges one (content, version) pair: the first release title and source stay, dates take the
// earliest. Two instances mapped to one version therefore converge on the same row whatever order they run in.
func (w *wtx) contentVersion(ctx context.Context, contentID, versionID int64, title, source string, grabbed, imported,
	failed int64) error {
	var (
		curTitle, curSource string
		curG, curI, curF    sql.NullInt64
	)
	err := w.tx.QueryRowContext(ctx, `SELECT release_title, source, grabbed_at, imported_at, failed_at FROM content_version
		WHERE content_id = ? AND version_id = ?`, contentID, versionID).Scan(&curTitle, &curSource, &curG, &curI, &curF)
	if errors.Is(err, sql.ErrNoRows) {
		_, err := w.exec(ctx, `INSERT INTO content_version (content_id, version_id, release_title, source, grabbed_at,
			imported_at, failed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, contentID, versionID, title, source,
			nullMillis(grabbed), nullMillis(imported), nullMillis(failed))
		if err == nil {
			w.changes.add("content_version", opInsert, 1)
		}
		return err
	}
	if err != nil {
		return err
	}
	var sets []kv
	if curTitle == "" && title != "" {
		sets = append(sets, kv{"release_title", title})
	}
	for _, c := range []struct {
		col string
		cur sql.NullInt64
		v   int64
	}{{"grabbed_at", curG, grabbed}, {"imported_at", curI, imported}, {"failed_at", curF, failed}} {
		if c.v != 0 && (!c.cur.Valid || c.v < c.cur.Int64) {
			sets = append(sets, kv{c.col, c.v})
		}
	}
	if len(sets) == 0 {
		return nil
	}
	_, err = w.upsert(ctx, "content_version", []kv{{"content_id", contentID}, {"version_id", versionID}}, sets, nil, nil)
	return err
}

func nullMillis(ms int64) any {
	if ms == 0 {
		return nil
	}
	return ms
}
