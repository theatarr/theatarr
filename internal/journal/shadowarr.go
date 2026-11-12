package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// arrRow is one arr history row.
type arrRow struct {
	arrID, date                  int64
	event, downloadID, hash      string
	source, quality, langs, data string
	score, episodeID, arrTitle   sql.NullInt64
	titleID, versionID           sql.NullInt64
}

func (b *builder) arrRows(instance string, first int64) ([]arrRow, error) {
	var x arrRow
	var dl sql.NullString
	err := b.db.QueryRowContext(b.ctx, `SELECT event_type, download_id, date_at, arr_title_id FROM arr_history
		WHERE instance = ? AND arr_id = ?`, instance, first).Scan(&x.event, &dl, &x.date, &x.arrTitle)
	if err != nil {
		return nil, err
	}
	// The group of the head query: same instance, event, download id and hour (and so the same arr title).
	hour := x.date / 3600000 * 3600000
	q := `SELECT arr_id, date_at, event_type, COALESCE(download_id, ''), COALESCE(info_hash, ''), source_title, quality,
		languages, data, custom_format_score, arr_episode_id, arr_title_id, title_id, version_id FROM arr_history
		WHERE instance = ? AND arr_title_id = ? AND event_type = ? AND date_at >= ? AND date_at < ? AND `
	args := []any{instance, x.arrTitle.Int64, x.event, hour, hour + 3600000}
	if dl.Valid {
		q += `download_id = ?`
		args = append(args, dl.String)
	} else {
		q += `arr_id = ?`
		args = append(args, first)
	}
	rows, err := b.db.QueryContext(b.ctx, q+` ORDER BY arr_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arrRow
	for rows.Next() {
		var r arrRow
		if err := rows.Scan(&r.arrID, &r.date, &r.event, &r.downloadID, &r.hash, &r.source, &r.quality, &r.langs,
			&r.data, &r.score, &r.episodeID, &r.arrTitle, &r.titleID, &r.versionID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, sql.ErrNoRows
	}
	return out, rows.Err()
}

// arrData is the sanitised history data the journal reads.
type arrData struct {
	Indexer       string `json:"indexer"`
	Size          string `json:"size"`
	ReleaseSource string `json:"releaseSource"`
	ReleaseType   string `json:"releaseType"`
	ReleaseGroup  string `json:"releaseGroup"`
	Message       string `json:"message"`
	MovieMatch    string `json:"movieMatchType"`
	SeriesMatch   string `json:"seriesMatchType"`
	Reason        string `json:"reason"`
}

func (b *builder) arr(e *Entry, rest string) error {
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		return sql.ErrNoRows
	}
	instance := rest[:i]
	first, err := strconv.ParseInt(rest[i+1:], 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	rows, err := b.arrRows(instance, first)
	if err != nil {
		return err
	}
	r0 := rows[0]
	var data arrData
	_ = json.Unmarshal([]byte(r0.data), &data)
	e.Source = "arr:" + instance
	size, _ := strconv.ParseInt(data.Size, 10, 64)
	e.Release = &Release{Title: clean(r0.source), Indexer: clean(data.Indexer), InfoHash: r0.hash, Size: size,
		Quality: r0.quality, Score: intp(r0.score), Group: clean(data.ReleaseGroup), How: data.ReleaseSource}
	_ = json.Unmarshal([]byte(r0.langs), &e.Release.Languages)
	e.f.matchType = data.MovieMatch + data.SeriesMatch
	var epIDs []int64
	for _, r := range rows {
		if !r.episodeID.Valid {
			continue
		}
		var id int64
		err := b.db.QueryRowContext(b.ctx, `SELECT target_id FROM arr_alias WHERE instance = ? AND kind = 'episode'
			AND arr_id = ?`, instance, r.episodeID.Int64).Scan(&id)
		if err == nil && !slices.Contains(epIDs, id) {
			epIDs = append(epIDs, id)
		}
	}
	e.f.episodeIDs = epIDs
	ti, _ := b.title(e.Title.ID)
	if ti != nil {
		e.Release.Episodes = ti.labels(epIDs)
	}
	switch r0.event {
	case "grabbed":
		verb := "grabbed"
		if e.Kind == KindUpgrade {
			verb = "grabbed as an upgrade"
		}
		e.Summary = fmt.Sprintf("arr %s %s %s (%s)", instance, verb, e.Release.Title, orDash(data.ReleaseSource))
		e.Reasoning = &Reasoning{Basis: "arr", Missing: "the arr's candidate list is not recorded; " +
			"its own decision is the score it recorded"}
		if err := b.arrPrev(e, instance, r0); err != nil {
			return err
		}
		e.f.sizeEpisodes = len(epIDs)
	case "downloadFailed":
		e.Summary = fmt.Sprintf("arr %s download failed: %s", instance, cleanMessage(orDash(data.Message)))
		if err := b.failures(e, e.Title.ID, e.Version, r0.date); err != nil {
			return err
		}
	default:
		e.Summary = fmt.Sprintf("arr %s imported %s", instance, e.Release.Title)
		if len(rows) > 1 {
			e.Summary += fmt.Sprintf(" (%d files)", len(rows))
		}
		if err := b.arrFiles(e, instance, r0.downloadID); err != nil {
			return err
		}
	}
	// The other events of the same download.
	if r0.downloadID != "" {
		if err := b.arrSiblings(e, instance, r0); err != nil {
			return err
		}
	}
	return nil
}

// arrPrev is the version's file before an arr grab: the latest import of the same movie or episode before it.
func (b *builder) arrPrev(e *Entry, instance string, r arrRow) error {
	if e.Kind != KindUpgrade {
		return nil
	}
	q := `SELECT source_title, quality, custom_format_score FROM arr_history WHERE instance = ? AND arr_title_id = ?
		AND event_type = 'downloadFolderImported' AND date_at < ?`
	args := []any{instance, r.arrTitle.Int64, r.date}
	if r.episodeID.Valid {
		q += ` AND arr_episode_id = ?`
		args = append(args, r.episodeID.Int64)
	}
	var rel, quality string
	var score sql.NullInt64
	err := b.db.QueryRowContext(b.ctx, q+` ORDER BY date_at DESC LIMIT 1`, args...).Scan(&rel, &quality, &score)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e.f.prevScore, e.f.prevQuality, e.f.prevRelease = intp(score), quality, clean(rel)
	sc := ""
	if score.Valid {
		sc = fmt.Sprintf(" %d", score.Int64)
	}
	e.Reasoning.Detail = fmt.Sprintf("replaces %s (%s%s)", clean(rel), quality, sc)
	return nil
}

// arrFiles reads the version's current files of an arr import (the arr's media info).
func (b *builder) arrFiles(e *Entry, instance, downloadID string) error {
	if downloadID == "" {
		return nil
	}
	rows, err := b.db.QueryContext(b.ctx, `SELECT path, size_bytes, media_info, custom_format_score, quality
		FROM version_file WHERE instance = ? AND download_id = ? ORDER BY id LIMIT 50`, instance, downloadID)
	if err != nil {
		return err
	}
	defer rows.Close()
	im := &Import{State: "imported"}
	for rows.Next() {
		var f File
		var mi string
		var score sql.NullInt64
		if err := rows.Scan(&f.Path, &f.Size, &mi, &score, &f.Quality); err != nil {
			return err
		}
		f.Source, f.Score = "arr", intp(score)
		var m struct {
			RunTime        string `json:"runTime"`
			Resolution     string `json:"resolution"`
			VideoCodec     string `json:"videoCodec"`
			AudioLanguages string `json:"audioLanguages"`
		}
		if json.Unmarshal([]byte(mi), &m) == nil {
			f.RuntimeSeconds, f.Resolution, f.VideoCodec, f.AudioLanguages = parseRunTime(m.RunTime), m.Resolution,
				m.VideoCodec, m.AudioLanguages
		}
		if e.f.fileRuntime == 0 && f.RuntimeSeconds > 0 {
			e.f.fileRuntime = f.RuntimeSeconds
			e.f.fileWidth, e.f.fileHeight = resolution(f.Resolution)
			if f.AudioLanguages != "" {
				e.f.fileAudio = strings.Split(f.AudioLanguages, "/")
			}
		}
		im.Files = append(im.Files, f)
	}
	if len(im.Files) == 0 {
		im.State = "imported (file since replaced or deleted)"
	}
	e.Import = im
	return rows.Err()
}

// parseRunTime reads the arr's "H:MM:SS" or "MM:SS".
func parseRunTime(s string) int {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

func resolution(s string) (int, int) {
	w, h, _ := strings.Cut(s, "x")
	wn, _ := strconv.Atoi(w)
	hn, _ := strconv.Atoi(h)
	return wn, hn
}

// arrSiblings lists the other events of the same arr download: the import or failure after a grab, the grab before
// an import or failure.
func (b *builder) arrSiblings(e *Entry, instance string, r arrRow) error {
	rows, err := b.db.QueryContext(b.ctx, `SELECT MIN(arr_id), MIN(date_at), event_type, COUNT(*), MAX(data)
		FROM arr_history WHERE instance = ? AND download_id = ? AND event_type IN ('grabbed', 'downloadFolderImported',
		'downloadFailed', 'downloadIgnored') GROUP BY event_type, date_at / 3600000 ORDER BY 2`, instance, r.downloadID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		var event, data string
		var n int
		if err := rows.Scan(&id, &at, &event, &n, &data); err != nil {
			return err
		}
		ref := "arr:" + instance + ":" + strconv.FormatInt(id, 10)
		if ref == e.ID {
			continue
		}
		text := map[string]string{"grabbed": "the arr grabbed it", "downloadFolderImported": "the arr imported it",
			"downloadFailed": "the arr's download failed", "downloadIgnored": "the arr ignored the download"}[event]
		if n > 1 {
			text += fmt.Sprintf(" (%d episodes)", n)
		}
		var d arrData
		if json.Unmarshal([]byte(data), &d) == nil && d.Message != "" {
			text += ": " + cleanMessage(d.Message)
		}
		if event == "downloadIgnored" {
			ref = ""
		}
		e.After = append(e.After, Event{At: store.FromMillis(at), Kind: event, Text: text, Ref: ref})
		if ref != "" {
			e.Related = append(e.Related, ref)
		}
	}
	return rows.Err()
}

// arrFollowUps lists the arr's events for a shadowed grab's hash.
func (b *builder) arrFollowUps(e *Entry, instance, hash string) error {
	if hash == "" {
		return nil
	}
	var first int64
	var dl string
	err := b.db.QueryRowContext(b.ctx, `SELECT arr_id, COALESCE(download_id, '') FROM arr_history WHERE instance = ?
		AND info_hash = ? AND event_type = 'grabbed' ORDER BY arr_id LIMIT 1`, instance, strings.ToLower(hash)).Scan(
		&first, &dl)
	if errors.Is(err, sql.ErrNoRows) || dl == "" {
		return nil
	}
	if err != nil {
		return err
	}
	return b.arrSiblings(e, instance, arrRow{arrID: first, downloadID: dl})
}
