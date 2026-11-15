package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// StoredHistory returns one page of History from the stored rows (table history, written when each event happened,
// internal/store history.go), newest first, in the shape and with the filters of History. The one difference: an
// upgrade's replaced files are one deleted row per download and library, its files in Replaced and its Release set
// only when they all held one release. Not yet read by Activity or the title page; the comparison test holds it to
// History.
func (j *Journal) StoredHistory(ctx context.Context, f Filter) (*HistoryPage, error) {
	page := &HistoryPage{Items: []*HistoryRow{}}
	where, wargs, none, err := j.historyFilter(ctx, &f)
	if err != nil {
		return nil, err
	}
	if none {
		return page, nil
	}
	// A grab's kind is grab unless kinds are filtered, as History reads it.
	kind := `CASE kind WHEN 'upgrade' THEN 'grab' ELSE kind END`
	if len(f.Kinds) > 0 {
		kind = `kind`
	}
	l := &historyLoader{ctx: ctx, db: j.st.Reader()}
	type stored struct {
		h                                            head
		release, formats, replaced                   sql.NullString
		event, hash, indexer, quality, eps, provider string
		reason                                       string
		source, importSt, by, detail                 string
		dl, score                                    sql.NullInt64
		size, files                                  int64
		episodes                                     []int64
		replacedFiles                                []store.HistoryReplacedFile
	}
	var rows []*stored
	err = l.query(`SELECT id, at, title_id, version_key, kind, event, download_id, release, info_hash, indexer, size,
		quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail
		FROM (SELECT ref AS id, at, title_id, version_key, `+kind+` AS kind, event, download_id, release, info_hash,
			indexer, size, quality, score, formats, episodes, files, replaced, provider, reason, source, import_state,
			by, detail FROM history)
		WHERE `+where+` ORDER BY at DESC, id DESC LIMIT ?`, append(wargs, f.Limit+1), func(r *sql.Rows) error {
		var s stored
		if err := r.Scan(&s.h.id, &s.h.at, &s.h.titleID, &s.h.version, &s.h.kind, &s.event, &s.dl, &s.release,
			&s.hash, &s.indexer, &s.size, &s.quality, &s.score, &s.formats, &s.eps, &s.files, &s.replaced,
			&s.provider, &s.reason, &s.source, &s.importSt, &s.by, &s.detail); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(s.eps), &s.episodes)
		l.epIDs = append(l.epIDs, s.episodes...)
		if s.replaced.Valid {
			_ = json.Unmarshal([]byte(s.replaced.String), &s.replacedFiles)
			slices.SortFunc(s.replacedFiles, func(a, b store.HistoryReplacedFile) int { return int(a.ID - b.ID) })
			for _, rf := range s.replacedFiles {
				var eps []int64
				_ = json.Unmarshal(rf.Episodes, &eps)
				l.epIDs = append(l.epIDs, eps...)
			}
		}
		rows = append(rows, &s)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("journal: stored history: %w", err)
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[f.Limit-1].h)
	}

	var titleIDs, failed []int64
	for _, s := range rows {
		titleIDs = append(titleIDs, s.h.titleID)
		if strings.HasSuffix(s.h.id, ":fail") && s.dl.Valid {
			failed = append(failed, s.dl.Int64)
		}
	}
	attempts := map[int64][]Attempt{}
	if len(failed) > 0 {
		if err := l.query(`SELECT download_id, provider, outcome, code, attempted_at FROM download_attempt
			WHERE download_id IN (`+ph(failed)+`) ORDER BY id`, anys(failed), func(r *sql.Rows) error {
			var dl, at int64
			var a Attempt
			if err := r.Scan(&dl, &a.Provider, &a.Outcome, &a.Code, &at); err != nil {
				return err
			}
			a.At = store.FromMillis(at)
			attempts[dl] = append(attempts[dl], a)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("journal: stored history: %w", err)
		}
	}
	titles, animes := map[int64]TitleRef{}, map[int64]bool{}
	if len(titleIDs) > 0 {
		if err := l.query(`SELECT id, name, COALESCE(year, 0), kind, anime FROM title WHERE id IN (`+
			ph(titleIDs)+`)`, anys(titleIDs), func(r *sql.Rows) error {
			var t TitleRef
			var anime bool
			if err := r.Scan(&t.ID, &t.Name, &t.Year, &t.Kind, &anime); err != nil {
				return err
			}
			titles[t.ID], animes[t.ID] = t, anime
			return nil
		}); err != nil {
			return nil, fmt.Errorf("journal: stored history: %w", err)
		}
	}
	if err := l.loadLabels(); err != nil {
		return nil, fmt.Errorf("journal: stored history: %w", err)
	}

	for _, s := range rows {
		x := &HistoryRow{ID: s.h.id, At: store.FromMillis(s.h.at), Kind: s.h.kind, Event: s.event,
			Title: titles[s.h.titleID], Anime: animes[s.h.titleID], Version: s.h.version,
			Episodes: l.labelsOf(s.episodes), Files: int(s.files), Provider: s.provider, Source: s.source,
			Import: s.importSt, By: s.by}
		if x.Title.ID == 0 {
			x.Title.ID = s.h.titleID
		}
		if s.release.Valid {
			x.Release = &Release{Title: clean(s.release.String), Indexer: s.indexer, InfoHash: s.hash, Size: s.size,
				Quality: s.quality}
			if s.score.Valid {
				sc := int(s.score.Int64)
				x.Release.Score = &sc
			}
			if s.formats.Valid {
				_ = json.Unmarshal([]byte(s.formats.String), &x.Release.Formats)
			}
		}
		switch {
		case x.Event == EvFailed && x.Kind == KindImport:
			x.Reason = "Import held for review: " + cleanMessage(s.reason)
		case x.Event == EvRemoved:
			// "unlinked (<reason>), moved aside to …"
			if r, ok := strings.CutPrefix(s.detail, "unlinked ("); ok {
				if i := strings.LastIndex(r, "), moved aside"); i >= 0 {
					x.Reason = cleanMessage(r[:i])
				}
			}
		case s.reason != "":
			x.Reason = cleanMessage(s.reason)
		}
		if strings.HasSuffix(x.ID, ":fail") && s.dl.Valid {
			x.Attempts = attempts[s.dl.Int64]
		}
		if x.Event == EvTitleRemoved {
			rm := ParseRemovalDetail(s.detail)
			x.Removal = &rm
		}
		if x.Event == EvDeleted {
			// The row's release is what replaced the files; its own the files' one release, when they share one.
			x.ReplacedBy, x.Release = x.Release, nil
			same := true
			for _, rf := range s.replacedFiles {
				f := ReplacedFile{ID: fmt.Sprint("lib:", rf.ID), At: store.FromMillis(rf.At), Path: rf.Path}
				if rf.Release != "" || rf.Quality != nil {
					f.Release = &Release{Title: clean(rf.Release)}
					if rf.Quality != nil {
						f.Release.Quality = *rf.Quality
					}
					if rf.Score != nil {
						sc := int(*rf.Score)
						f.Release.Score = &sc
					}
				}
				var eps []int64
				_ = json.Unmarshal(rf.Episodes, &eps)
				f.Episodes = l.labelsOf(eps)
				x.Replaced = append(x.Replaced, f)
				same = same && rf.Release == s.replacedFiles[0].Release
			}
			if same && len(x.Replaced) > 0 {
				x.Release = x.Replaced[0].Release
			}
		}
		page.Items = append(page.Items, x)
	}
	return page, nil
}
