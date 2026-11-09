package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/store"
)

// shadowGrab is an arr grab re-decided in shadow mode: the arr's release as Theatarr decided it, Theatarr's own pick and
// results, the diff row's bucket, and the router's dry run.
func (b *builder) shadowGrab(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	var instance, release, source, arrIndexer, arrQuality, state, errText, note, arrDec, results, eps string
	var pick sql.NullString
	var arrScore, planID sql.NullInt64
	var resultCount, acceptedCount int
	var updated int64
	if err := b.db.QueryRowContext(b.ctx, `SELECT instance, release_title, release_source, arr_indexer, arr_quality,
		arr_score, state, error, search_note, result_count, accepted_count, arr_decision, pick, results, episodes,
		router_plan_id, updated_at FROM shadow_grab WHERE id = ?`, id).Scan(&instance, &release, &source, &arrIndexer,
		&arrQuality, &arrScore, &state, &errText, &note, &resultCount, &acceptedCount, &arrDec, &pick, &results, &eps,
		&planID, &updated); err != nil {
		return err
	}
	e.Source = "shadow"
	e.Release = &Release{Title: clean(release), Indexer: clean(arrIndexer), Quality: arrQuality, Score: intp(arrScore),
		How: "arr:" + instance + " " + source}
	var epIDs []int64
	_ = json.Unmarshal([]byte(eps), &epIDs)
	e.f.episodeIDs = epIDs
	if ti, _ := b.title(e.Title.ID); ti != nil {
		e.Release.Episodes = ti.labels(epIDs)
	}
	r := &Reasoning{Basis: "shadow", Results: resultCount, Accepted: acceptedCount, Notes: clean(note)}
	var arr snapshot.Candidate
	if json.Unmarshal([]byte(arrDec), &arr) == nil && arr.Release != "" {
		cleanCandidate(&arr)
		r.Arr = &arr
		r.Existing = arr.Existing
		e.Release.InfoHash, e.Release.Size, e.Release.Formats, e.Release.Languages = arr.InfoHash, arr.Size,
			arr.Formats, arr.Languages
		if arr.Existing != nil {
			s := arr.Existing.Score
			e.f.prevScore, e.f.prevQuality = &s, arr.Existing.Quality
		}
	}
	if pick.Valid {
		var p snapshot.Candidate
		if json.Unmarshal([]byte(pick.String), &p) == nil && p.Release != "" {
			cleanCandidate(&p)
			r.Chosen = &p
		}
	}
	var rs []snapshot.Candidate
	_ = json.Unmarshal([]byte(results), &rs)
	for _, c := range rs {
		if r.Chosen != nil && c.Release == r.Chosen.Release && c.InfoHash == r.Chosen.InfoHash {
			continue
		}
		cleanCandidate(&c)
		r.RunnersUp = append(r.RunnersUp, c)
	}
	var bucket, why, detail string
	var diffID int64
	err = b.db.QueryRowContext(b.ctx, `SELECT id, bucket, reason, detail FROM shadow_diff WHERE grab_id = ?`, id).Scan(
		&diffID, &bucket, &why, &detail)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	r.Bucket, r.Why, r.Detail = bucket, why, clean(detail)
	e.f.shadowBucket, e.f.shadowWhy = bucket, why
	if p, err := b.shadowPlan(planID, e.Title.ID, e.Version, "shadow.grab", updated); err != nil {
		return err
	} else {
		r.Plan = p
	}
	e.Reasoning = r
	switch {
	case state != "done":
		e.Summary = fmt.Sprintf("arr %s grabbed %s; shadow decision %s %s", instance, e.Release.Title, state,
			cleanMessage(errText))
	case bucket == "":
		e.Summary = fmt.Sprintf("arr %s grabbed %s; not classified yet", instance, e.Release.Title)
	default:
		ours := "nothing"
		if r.Chosen != nil {
			ours = r.Chosen.Release
		}
		e.Summary = fmt.Sprintf("arr %s grabbed %s; Theatarr: %s/%s (Theatarr's pick: %s)", instance, e.Release.Title, bucket, why,
			ours)
	}
	if diffID > 0 {
		r.Detail = strings.TrimSpace(r.Detail + fmt.Sprintf(" (shadow diff %d)", diffID))
	}
	return b.arrFollowUps(e, instance, e.Release.InfoHash)
}

// shadowPlan is the grab's or pick's router plan: by id, else the plan of the same source recorded just after.
func (b *builder) shadowPlan(planID sql.NullInt64, titleID int64, version, source string, after int64) (*Plan, error) {
	id := planID.Int64
	if !planID.Valid {
		err := b.db.QueryRowContext(b.ctx, `SELECT id FROM router_plan WHERE title_id = ? AND version_key = ?
			AND source = ? AND created_at BETWEEN ? AND ? ORDER BY created_at LIMIT 1`, titleID, version, source,
			after-5_000, after+120_000).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	p, err := b.planRow(id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// shadowPick is an RSS pick of ours no arr grab followed within the link window.
func (b *builder) shadowPick(e *Entry, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	var bucket, why, detail, ourRelease, ourQuality string
	var ourScore, pickID sql.NullInt64
	if err := b.db.QueryRowContext(b.ctx, `SELECT bucket, reason, detail, our_release, our_quality, our_score,
		rss_pick_id FROM shadow_diff WHERE id = ?`, id).Scan(&bucket, &why, &detail, &ourRelease, &ourQuality,
		&ourScore, &pickID); err != nil {
		return err
	}
	e.Source = "shadow"
	e.Release = &Release{Title: clean(ourRelease), Quality: ourQuality, Score: intp(ourScore), How: "theatarr rss"}
	r := &Reasoning{Basis: "shadow rss", Bucket: bucket, Why: why, Detail: clean(detail)}
	e.f.shadowBucket, e.f.shadowWhy = bucket, why
	if pickID.Valid {
		var dec, indexer, hash, eps string
		var planID sql.NullInt64
		var seen int64
		err := b.db.QueryRowContext(b.ctx, `SELECT decision, indexer, info_hash, episodes, router_plan_id, seen_at
			FROM shadow_rss_pick WHERE id = ?`, pickID.Int64).Scan(&dec, &indexer, &hash, &eps, &planID, &seen)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var c snapshot.Candidate
			if json.Unmarshal([]byte(dec), &c) == nil && c.Release != "" {
				cleanCandidate(&c)
				r.Chosen = &c
				r.Existing = c.Existing
				e.Release.Size, e.Release.Formats, e.Release.Languages = c.Size, c.Formats, c.Languages
				if c.Existing != nil {
					s := c.Existing.Score
					e.f.prevScore, e.f.prevQuality = &s, c.Existing.Quality
				}
			}
			e.Release.Indexer, e.Release.InfoHash = indexer, hash
			var epIDs []int64
			_ = json.Unmarshal([]byte(eps), &epIDs)
			e.f.episodeIDs = epIDs
			if ti, _ := b.title(e.Title.ID); ti != nil {
				e.Release.Episodes = ti.labels(epIDs)
			}
			if r.Plan, err = b.shadowPlan(planID, e.Title.ID, e.Version, "shadow.rss", seen); err != nil {
				return err
			}
		}
	}
	e.Reasoning = r
	e.Summary = fmt.Sprintf("theatarr would grab %s from RSS; no arr grab followed (%s/%s)", e.Release.Title, bucket, why)
	return nil
}

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
