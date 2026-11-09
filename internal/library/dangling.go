package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The dangling-link report: every dangling zurg link, taken at start and every
// 6 hours. Here it is a read-only walk of the version roots plus
// library.dangling.roots, recorded in library_dangling (migration 00021) and printed by theatarr library dangling. It
// writes nothing to the filesystem, so it runs whatever the gate says. With zurg's API (library.mount.zurg_api) each
// row also says when the link's torrent has no video upstream (cause no_video, 00063), and the summary counts them.

// DanglingRow is one dangling link of the latest report.
type DanglingRow struct {
	Path    string `json:"path"`
	Target  string `json:"target"`
	Root    string `json:"root"`
	Version string `json:"version,omitempty"`
	Folder  string `json:"folder"`
	// Cause is CauseNoVideo when the link's torrent has no video upstream (zurg lists it with size 0, or its folder
	// holds no video file); "" when unknown.
	Cause     string    `json:"cause,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// DanglingSummary is one report.
type DanglingSummary struct {
	At       time.Time      `json:"at"`
	Roots    []string       `json:"roots"`
	Links    int            `json:"links"` // links into the mount seen
	Dangling int            `json:"dangling"`
	ByRoot   map[string]int `json:"by_root"`
	Errors   []string       `json:"errors,omitempty"` // roots that could not be walked
	Took     time.Duration  `json:"took"`
	// Zurg is what zurg's API added; nil without it.
	Zurg *DanglingZurg `json:"zurg,omitempty"`
}

// DanglingZurg is what zurg's API said during a report.
type DanglingZurg struct {
	// NoVideo counts the dangling links whose torrent has no video upstream.
	NoVideo int `json:"no_video"`
	// NoVideoTorrents counts zurg's torrents with no video upstream, linked or not.
	NoVideoTorrents int  `json:"no_video_torrents"`
	Repairing       bool `json:"repairing"`
	// Error is why the listing could not be read: the rows are then unclassified.
	Error string `json:"error,omitempty"`
}

type reportRoot struct{ root, version string }

// DanglingReport walks the roots, records the dangling links (replacing the last report) and returns the summary.
// A mount outage stops it before anything is recorded: nothing is read as dangling then.
func (l *Library) DanglingReport(ctx context.Context) (*DanglingSummary, error) {
	if l.set.MountPath == "" {
		return nil, errors.New("library: library.mount.path is not set")
	}
	start := time.Now()
	if err := l.mountHealthy(ctx); err != nil {
		return nil, err
	}
	ix, err := l.newMountIndex(ctx)
	if err != nil {
		return nil, err
	}
	var roots []reportRoot
	seenRoot := map[string]bool{}
	for _, k := range l.gate.Versions() {
		for _, r := range l.gate.Roots(k) {
			if !seenRoot[r] {
				seenRoot[r] = true
				roots = append(roots, reportRoot{r, k})
			}
		}
	}
	for _, r := range l.set.DanglingRoots {
		if !seenRoot[r] {
			seenRoot[r] = true
			roots = append(roots, reportRoot{r, ""})
		}
	}
	// A version root inside an extra root is walked once, as the version's.
	versionOf := func(path string) string {
		best, v := "", ""
		for _, r := range roots {
			if r.version != "" && policy.Within(path, r.root) && len(r.root) > len(best) {
				best, v = r.root, r.version
			}
		}
		return v
	}
	sum := &DanglingSummary{At: l.now(), ByRoot: map[string]int{}}
	zv := l.zurgView(ctx)
	if zv != nil {
		zv.ix = ix
	}
	if zv != nil {
		sum.Zurg = &DanglingZurg{NoVideoTorrents: zv.list.NoVideoCount(), Repairing: zv.repair.Running, Error: zv.err}
	}
	found := map[string]DanglingRow{}
	seen := map[string]bool{}
	for _, r := range roots {
		sum.Roots = append(sum.Roots, r.root)
		var sc LinkScan
		if err := l.gate.Reachable(ctx, r.root); err != nil {
			sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", r.root, err))
			continue
		}
		links, err := l.scanRoot(ctx, r.root, &sc)
		if err != nil {
			sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", r.root, err))
			continue
		}
		for _, ml := range links {
			if seen[ml.path] {
				continue
			}
			seen[ml.path] = true
			sum.Links++
			ok, err := ix.has(ml.folder, ml.inner)
			if errors.Is(err, errFolderUnreadable) {
				continue // no verdict on a link into a folder the mount would not list
			}
			if err != nil {
				return nil, err
			}
			if ok {
				continue
			}
			v := r.version
			if v == "" {
				v = versionOf(ml.path)
			}
			row := DanglingRow{Path: ml.path, Target: ml.target, Root: r.root, Version: v, Folder: ml.folder}
			if zv != nil && zv.list != nil {
				hash, err := linkHash(ctx, l.st.Reader(), ml.path)
				if err != nil {
					return nil, err
				}
				if zv.noVideo(hash, ml.folder) {
					row.Cause = CauseNoVideo
					sum.Zurg.NoVideo++
				}
			}
			found[ml.path] = row
			sum.ByRoot[r.root]++
		}
	}
	sum.Dangling = len(found)
	sum.Took = time.Since(start)
	now := store.Millis(sum.At)
	rootsJSON, _ := json.Marshal(sum.Roots)
	err = l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT path FROM library_dangling`)
		if err != nil {
			return err
		}
		var gone []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			if _, still := found[p]; !still {
				gone = append(gone, p)
			}
		}
		rows.Close()
		for _, p := range gone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM library_dangling WHERE path = ?`, p); err != nil {
				return err
			}
		}
		for _, d := range found {
			if _, err := tx.ExecContext(ctx, `INSERT INTO library_dangling (path, target, root, version_key, folder,
				cause, first_seen_at, last_seen_at) VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?)
				ON CONFLICT (path) DO UPDATE SET target = excluded.target, root = excluded.root,
				version_key = excluded.version_key, folder = excluded.folder, cause = excluded.cause,
				last_seen_at = excluded.last_seen_at`,
				d.Path, d.Target, d.Root, d.Version, d.Folder, d.Cause, now, now); err != nil {
				return err
			}
		}
		var errText any
		if len(sum.Errors) > 0 {
			errText = strings.Join(sum.Errors, "; ")
		}
		var noVideo, noVideoTorrents, repairing, zurgErr any
		if z := sum.Zurg; z != nil {
			noVideo, noVideoTorrents, repairing = z.NoVideo, z.NoVideoTorrents, z.Repairing
			if z.Error != "" {
				zurgErr = z.Error
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO library_dangling_scan (at, roots, links, dangling, error, took_ms,
			no_video, no_video_torrents, repairing, zurg_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, now,
			string(rootsJSON), sum.Links, sum.Dangling, errText, sum.Took.Milliseconds(), noVideo, noVideoTorrents,
			repairing, zurgErr)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("library: dangling report: %w", err)
	}
	return sum, nil
}

// Dangling reads the latest report: its rows (by path) and when it ran (zero when never).
func Dangling(ctx context.Context, db *sql.DB) ([]DanglingRow, *DanglingSummary, error) {
	sum := &DanglingSummary{ByRoot: map[string]int{}}
	var at, took int64
	var roots string
	var errText, zurgErr sql.NullString
	var noVideo, noVideoTorrents sql.NullInt64
	var repairing sql.NullBool
	err := db.QueryRowContext(ctx, `SELECT at, roots, links, dangling, error, took_ms, no_video, no_video_torrents,
		repairing, zurg_error FROM library_dangling_scan ORDER BY id DESC LIMIT 1`).Scan(&at, &roots, &sum.Links,
		&sum.Dangling, &errText, &took, &noVideo, &noVideoTorrents, &repairing, &zurgErr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	sum.At, sum.Took = store.FromMillis(at), time.Duration(took)*time.Millisecond
	_ = json.Unmarshal([]byte(roots), &sum.Roots)
	if errText.Valid && errText.String != "" {
		sum.Errors = strings.Split(errText.String, "; ")
	}
	if noVideo.Valid {
		sum.Zurg = &DanglingZurg{NoVideo: int(noVideo.Int64), NoVideoTorrents: int(noVideoTorrents.Int64),
			Repairing: repairing.Bool, Error: zurgErr.String}
	}
	rows, err := db.QueryContext(ctx, `SELECT path, target, root, COALESCE(version_key, ''), folder, COALESCE(cause, ''),
		first_seen_at, last_seen_at FROM library_dangling ORDER BY path`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []DanglingRow
	for rows.Next() {
		var d DanglingRow
		var f, ls int64
		if err := rows.Scan(&d.Path, &d.Target, &d.Root, &d.Version, &d.Folder, &d.Cause, &f, &ls); err != nil {
			return nil, nil, err
		}
		d.FirstSeen, d.LastSeen = store.FromMillis(f), store.FromMillis(ls)
		sum.ByRoot[d.Root]++
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, sum, rows.Err()
}
