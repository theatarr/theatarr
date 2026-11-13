package arrview

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

func jsonIDs(ids []int64) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

// historyPage pages Theatarr's download events before reading release data or embedding titles.
// The plain-list routes intentionally have no limit: their array shape and complete result match the arrs.
func (s *Service) historyPage(r *request, titleID int64, p *pagingResource) ([]historyResource, error) {
	var args []any
	q := `WITH events AS (`
	for i, e := range []struct{ code, name, date, condition string }{
		{"1", "grabbed", "added_at", "added_at IS NOT NULL"},
		{"3", "downloadFolderImported", "imported_at", "imported_at IS NOT NULL"},
		{"4", "downloadFailed", "updated_at", "state = 'failed'"},
	} {
		// Sonarr writes one record per episode: a pack has one per member, its position in the download's
		// episodes giving the id (historyID). A download with no episodes keeps one record (LEFT JOIN on []).
		id, eid, join := `0`, `json_extract(d.episodes, '$[0]')`, ``
		if r.v.Flavour == Sonarr {
			id, eid, join = `COALESCE(j.key, 0)*`+strconv.FormatInt(historyEpisodeStride, 10), `j.value`, ` LEFT JOIN json_each(d.episodes) j ON 1=1`
		}
		if i > 0 {
			q += ` UNION ALL`
		}
		q += ` SELECT ` + e.code + ` + ` + strconv.Itoa(HistoryBase) + ` + d.id*10 + ` + id + ` id, d.title_id tid,
  ` + eid + ` eid, '` + e.name + `' event, d.` + e.date + ` date, upper(d.info_hash) download, d.quality quality,
  '[]' languages
  FROM download d` + join + ` WHERE d.version_key IN (` + store.Placeholders(len(r.v.Versions)) + `) AND (d.added_at IS NOT NULL OR d.state='failed') AND d.` + e.condition
		args = append(args, toAny(r.v.Versions)...)
	}
	q += `), filtered AS (SELECT h.* FROM events h LEFT JOIN title t ON t.id=h.tid WHERE 1=1`
	if titleID != 0 {
		q += ` AND h.tid=?`
		args = append(args, titleID)
	}
	key := "movieIds"
	if r.v.Flavour == Sonarr {
		key = "seriesIds"
	}
	ids, given := s.titleFilter(r, key)
	if given {
		q += ` AND h.tid IN (SELECT value FROM json_each(?))`
		args = append(args, jsonIDs(ids))
	}
	var events []string
	for k, vs := range r.URL.Query() {
		if !strings.EqualFold(k, "eventType") {
			continue
		}
		for _, v := range vs {
			for _, x := range strings.Split(v, ",") {
				events = append(events, historyEventName(r.v.Flavour, strings.TrimSpace(x)))
			}
		}
	}
	if len(events) > 0 {
		q += ` AND h.event IN (` + store.Placeholders(len(events)) + `)`
		args = append(args, toAny(events)...)
	}
	if d, ok := r.query("downloadId"); ok && strings.TrimSpace(d) != "" {
		q += ` AND upper(h.download)=upper(?)`
		args = append(args, d)
	}
	if r.v.Flavour == Sonarr {
		if ep, ok := r.query("episodeId"); ok {
			q += ` AND h.eid=CAST(? AS INTEGER)`
			args = append(args, ep)
		}
		if season, ok := r.query("seasonNumber"); ok {
			q += ` AND EXISTS(SELECT 1 FROM episode e WHERE e.id=h.eid AND e.season=?)`
			args = append(args, season)
		}
	}
	since := len(r.seg) > 1 && r.seg[1] == "since"
	if since {
		date, _ := r.query("date")
		t, _ := parseDate(date)
		q += ` AND h.date>=?`
		args = append(args, store.Millis(t))
	}
	q += `)`
	order := "date DESC, id DESC"
	if since {
		order = "date ASC, id ASC"
	}
	if p != nil {
		if err := s.st.Reader().QueryRowContext(r.Context(), q+` SELECT COUNT(*) FROM filtered`, args...).Scan(&p.TotalRecords); err != nil {
			return nil, err
		}
		dir := " DESC"
		if p.SortDirection == "ascending" {
			dir = " ASC"
		}
		sortKey, _ := r.query("sortKey")
		switch strings.ToLower(sortKey) {
		case "series.sorttitle", "moviemetadata.sorttitle":
			p.SortKey = sortKey
			// Only the compact title key is computed; SQL still selects the page before hydration.
			order = `(SELECT theatarr_sort_title(sort_name,name) FROM title WHERE id=tid)` + dir + `, id` + dir
		case "quality", "languages":
			if r.v.Flavour == Radarr {
				p.SortKey = sortKey
				order = `theatarr_history_languages(languages)`
				if strings.EqualFold(sortKey, "quality") {
					order = `theatarr_history_quality('radarr',quality)`
				}
				order += dir + `, id` + dir
			} else {
				order = "date" + dir + ", id" + dir
			}
		default:
			order = "date" + dir + ", id" + dir
		}
	}
	query := q + ` SELECT id FROM filtered ORDER BY ` + order
	if p != nil {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, p.PageSize, p.offset())
	}
	rows, err := s.st.Reader().QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	var ordered []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ordered = append(ordered, id)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	out := make([]historyResource, 0, len(ordered))
	for start := 0; start < len(ordered); start += 256 {
		batch := ordered[start:min(start+256, len(ordered))]
		var downloads []int64
		for _, id := range batch {
			downloads = append(downloads, historyDownload(id))
		}
		ds, err := s.downloadHistory(r, downloads)
		if err != nil {
			return nil, err
		}
		byID := map[int64]historyResource{}
		for _, h := range ds {
			byID[h.ID] = h
		}
		for _, id := range batch {
			if h, ok := byID[id]; ok {
				out = append(out, h)
			}
		}
	}
	if err := s.embedHistory(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) embedHistory(r *request, hs []historyResource) error {
	if !r.queryBool("includeSeries", false) && !r.queryBool("includeMovie", false) && !r.queryBool("includeEpisode", false) {
		return nil
	}
	var ids []int64
	for _, h := range hs {
		if h.MovieID != nil {
			ids = append(ids, *h.MovieID-IDBase)
		}
		if h.SeriesID != nil {
			ids = append(ids, *h.SeriesID-IDBase)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	es, err := s.load(r.Context(), r.v, ids)
	if err != nil {
		return err
	}
	movies, series := map[int64]*movieResource{}, map[int64]*seriesResource{}
	if r.queryBool("includeMovie", false) {
		for _, e := range es {
			m := s.movieResource(r.v, e)
			movies[m.ID] = &m
		}
	}
	if r.queryBool("includeSeries", false) {
		ss, err := s.seriesResources(r.Context(), r.v, es)
		if err != nil {
			return err
		}
		for i := range ss {
			series[ss[i].ID] = &ss[i]
		}
	}
	eps := map[int64]*episodeResource{}
	if r.queryBool("includeEpisode", false) {
		var eids []int64
		for _, h := range hs {
			if h.EpisodeID != nil {
				eids = append(eids, *h.EpisodeID)
			}
		}
		all, err := s.loadEpisodesWith(r.Context(), r.v, es, true, eids)
		if err != nil {
			return err
		}
		for _, e := range es {
			for _, x := range all[e.id] {
				ep := episodeFrom(e, x)
				eps[x.id] = &ep
			}
		}
	}
	for i := range hs {
		h := &hs[i]
		if h.MovieID != nil {
			h.Movie = movies[*h.MovieID]
		}
		if h.SeriesID != nil {
			h.Series = series[*h.SeriesID]
		}
		if h.EpisodeID != nil {
			h.Episode = eps[*h.EpisodeID]
		}
	}
	return nil
}
