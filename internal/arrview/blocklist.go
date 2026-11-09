package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// blocklistResource is the arrs' BlocklistResource (Radarr @ a96bf7c src/Radarr.Api.V3/Blocklist/BlocklistResource.cs,
// Sonarr @ cab419a src/Sonarr.Api.V3/Blocklist/BlocklistResource.cs) over one router_blocklist row. The id is the
// row's rowid (the journal names blocklist entries by it too). Radarr writes movieId and movie; Sonarr seriesId,
// episodeIds and series. The release name is shown as the views show it in history and queue.
type blocklistResource struct {
	ID            int64            `json:"id"`
	MovieID       *int64           `json:"movieId,omitempty"`
	SeriesID      *int64           `json:"seriesId,omitempty"`
	EpisodeIDs    *[]int64         `json:"episodeIds,omitempty"` // Sonarr only, always written there
	SourceTitle   string           `json:"sourceTitle,omitempty"`
	Languages     []language       `json:"languages"`
	Quality       qualityModel     `json:"quality"`
	CustomFormats []customFormatID `json:"customFormats"`
	Date          arrTime          `json:"date"`
	Protocol      string           `json:"protocol"`
	Indexer       string           `json:"indexer,omitempty"`
	Message       string           `json:"message,omitempty"`
	Movie         *movieResource   `json:"movie,omitempty"`
	Series        *seriesResource  `json:"series,omitempty"`

	titleID int64
}

// blocklistRows reads the router_blocklist rows of the view's versions (only those: a view never shows or removes
// another version's entries), with what their download recorded (quality, indexer, formats, episodes) when the
// entry came from one of ours; else the quality and languages are parsed from the release name. titleID limits them
// to one title (0: all).
func (s *Service) blocklistRows(ctx context.Context, v *View, titleID int64, selected ...[]int64) ([]blocklistResource, error) {
	if len(v.Versions) == 0 {
		return nil, nil
	}
	q := `SELECT b.rowid, COALESCE(b.title_id, 0), b.release_title, b.reason, b.created_at, COALESCE(d.quality, ''),
		COALESCE(d.indexer, ''), COALESCE(d.formats, ''), COALESCE(d.episodes, '[]')
		FROM router_blocklist b LEFT JOIN download d ON d.id = b.download_id
		WHERE b.version_key IN (` + store.Placeholders(len(v.Versions)) + `)`
	args := toAny(v.Versions)
	if titleID != 0 {
		q += ` AND b.title_id = ?`
		args = append(args, titleID)
	}
	if len(selected) > 0 {
		q += ` AND b.rowid IN (SELECT value FROM json_each(?))`
		args = append(args, jsonIDs(selected[0]))
	}
	rows, err := s.st.Reader().QueryContext(ctx, q+` ORDER BY b.created_at DESC, b.rowid DESC`, args...)
	if err != nil {
		return nil, err
	}
	var out []blocklistResource
	for rows.Next() {
		var b blocklistResource
		var created int64
		var qual, formats, eps string
		if err := rows.Scan(&b.ID, &b.titleID, &b.SourceTitle, &b.Message, &created, &qual, &b.Indexer, &formats,
			&eps); err != nil {
			rows.Close()
			return nil, err
		}
		b.Date, b.Protocol = at(store.FromMillis(created)), "torrent"
		var langs []string
		if v.Flavour == Sonarr {
			if qual == "" {
				qual = sonarr.ParseQuality(b.SourceTitle).Quality.Name
			}
			if p := sonarr.ParseTitle(b.SourceTitle); p != nil {
				for _, l := range p.Languages {
					langs = append(langs, l.Name)
				}
			}
			sid := int64(0)
			if b.titleID > 0 {
				sid = viewID(b.titleID)
			}
			epIDs := []int64{}
			_ = json.Unmarshal([]byte(eps), &epIDs)
			b.SeriesID, b.EpisodeIDs = &sid, &epIDs
		} else {
			if qual == "" {
				qual = radarr.ParseQuality(b.SourceTitle).Quality.Name
			}
			if p := radarr.ParseMovieTitle(b.SourceTitle, false); p != nil {
				for _, l := range p.Languages {
					langs = append(langs, l.Name)
				}
			}
			mid := int64(0)
			if b.titleID > 0 {
				mid = viewID(b.titleID)
			}
			b.MovieID = &mid
		}
		b.Quality, b.Languages = qualityModelFor(v.Flavour, qual, 1), languagesFrom(langs)
		var fs []decide.FormatScore
		_ = json.Unmarshal([]byte(formats), &fs)
		b.CustomFormats = formatIDs(fs)
		out = append(out, b)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return out, s.embedBlocklistTitles(ctx, v, out)
}

// embedBlocklistTitles sets each row's movie or series (BlocklistResourceMapper: model.Movie.ToResource), when the
// view has the title.
func (s *Service) embedBlocklistTitles(ctx context.Context, v *View, bs []blocklistResource) error {
	var ids []int64
	for _, b := range bs {
		if b.titleID > 0 && !slices.Contains(ids, b.titleID) {
			ids = append(ids, b.titleID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	es, err := s.load(ctx, v, ids)
	if err != nil {
		return err
	}
	movies, series := map[int64]*movieResource{}, map[int64]*seriesResource{}
	if v.Flavour == Sonarr {
		srs, err := s.seriesResources(ctx, v, es)
		if err != nil {
			return err
		}
		for i := range srs {
			series[srs[i].ID-IDBase] = &srs[i]
		}
	} else {
		for _, e := range es {
			m := s.movieResource(v, e)
			movies[e.id] = &m
		}
	}
	for i := range bs {
		bs[i].Movie, bs[i].Series = movies[bs[i].titleID], series[bs[i].titleID]
	}
	return nil
}

// GET blocklist (BlocklistController.GetBlocklist): paged, default date descending; sortKey date, indexer,
// sourceTitle, or the title's sort title (Radarr movieMetadata.sortTitle, Sonarr series.sortTitle); filters
// movieIds / seriesIds and protocols (torrent only here).
func (s *Service) blocklist(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	p := r.paging("date", "descending")
	q := ` FROM router_blocklist b LEFT JOIN download d ON d.id=b.download_id LEFT JOIN title t ON t.id=b.title_id
 WHERE b.version_key IN (` + store.Placeholders(len(r.v.Versions)) + `)`
	args := toAny(r.v.Versions)
	key := "movieIds"
	if r.v.Flavour == Sonarr {
		key = "seriesIds"
	}
	ids, given, err := s.titleFilter(ctx, r, key)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if given {
		q += ` AND b.title_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonIDs(ids))
	}
	if protos, ok := r.query("protocols"); ok {
		torrent := false
		for _, vs := range r.URL.Query()["protocols"] {
			for _, v := range strings.Split(vs, ",") {
				torrent = torrent || v == "2" || strings.EqualFold(v, "torrent")
			}
		}
		if protos != "" && !torrent {
			q += ` AND 0`
		}
	}
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*)`+q, args...).Scan(&p.TotalRecords); err != nil {
		s.internal(w, r, err)
		return
	}
	order := "b.created_at"
	if k, ok := r.query("sortKey"); ok {
		switch strings.ToLower(k) {
		case "indexer":
			order = `lower(COALESCE(d.indexer,''))`
			p.SortKey = k
		case "sourcetitle":
			order = `lower(b.release_title)`
			p.SortKey = k
		case "moviemetadata.sorttitle", "series.sorttitle":
			order = titleSortSQL
			p.SortKey = k
		}
	}
	dir := " DESC"
	if p.SortDirection == "ascending" {
		dir = " ASC"
	}
	// Preserve the old stable tie order (newest blocklist id first).
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT b.rowid`+q+` ORDER BY `+order+dir+`, b.created_at DESC, b.rowid DESC LIMIT ? OFFSET ?`, append(args, p.PageSize, p.offset())...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var pageIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		pageIDs = append(pageIDs, id)
	}
	if err := closeRows(rows); err != nil {
		s.internal(w, r, err)
		return
	}
	bs, err := s.blocklistRows(ctx, r.v, 0, pageIDs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byID := map[int64]blocklistResource{}
	for _, b := range bs {
		byID[b.ID] = b
	}
	out := make([]blocklistResource, 0, len(pageIDs))
	for _, id := range pageIDs {
		if b, ok := byID[id]; ok {
			out = append(out, b)
		}
	}
	p.Records = out
	writeJSON(w, http.StatusOK, p)
}

// GET blocklist/movie?movieId= (Radarr's GetMovieBlocklist): a plain list.
func (s *Service) movieBlocklist(w http.ResponseWriter, r *request) {
	tid, err := s.resolve(r.Context(), r.v, int64(r.queryInt("movieId", 0)))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	bs := []blocklistResource{}
	if tid != 0 {
		rows, err := s.blocklistRows(r.Context(), r.v, tid)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		bs = append(bs, rows...)
	}
	writeJSON(w, http.StatusOK, bs)
}

// DELETE blocklist/{id} and DELETE blocklist/bulk {ids} (BlocklistController.DeleteBlocklist / Remove): each entry
// of the view's versions is taken off through the web UI's unblock (router.Unblock with a journal action, by
// key:<the API key's name>), so routes may place the release again. Our database only: the download it came from
// stays as it is, and no provider is called. A single id the view does not hold is a 404; bulk skips such ids, as
// the arrs' Delete does. 200, with {} for bulk.
func (s *Service) deleteBlocklist(w http.ResponseWriter, r *request) {
	bulk := len(r.seg) == 2 && r.seg[1] == "bulk"
	var ids []int64
	if bulk {
		var body struct {
			IDs []int64 `json:"ids"`
		}
		if err := decodeBody(r, &body); err != nil {
			badRequest(w, "", err.Error(), nil)
			return
		}
		ids = body.IDs
	} else if id, ok := r.pathID(1); ok {
		ids = []int64{id}
	} else {
		notFound(w, "Blocklist item not found")
		return
	}
	for _, id := range ids {
		ok, err := s.unblock(r.Context(), r, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !ok && !bulk {
			notFound(w, "Blocklist item with ID "+strconv.FormatInt(id, 10)+" does not exist")
			return
		}
	}
	if bulk {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// unblock removes one blocklist entry of the view's versions, audited as the web UI's unblock. false when the view
// has no entry with that id.
func (s *Service) unblock(ctx context.Context, r *request, id int64) (bool, error) {
	var b *router.Blocked
	err := s.st.WriteTx(ctx, func(tx *sqlTx) error {
		var version, hash string
		err := tx.QueryRowContext(ctx, `SELECT version_key, info_hash FROM router_blocklist WHERE rowid = ?`,
			id).Scan(&version, &hash)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !slices.Contains(r.v.Versions, version) {
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
		if b, err = router.Unblock(ctx, tx, version, hash); err != nil {
			return err
		}
		_, err = journal.RecordAction(ctx, tx, journal.Action{Action: journal.ActUnblock, TitleID: b.TitleID,
			Version: b.Version, InfoHash: b.Hash, Release: b.Release, DownloadID: b.DownloadID, Target: b.Hash,
			Detail: "was blocked by " + b.Source + ": " + b.Reason, By: "key:" + r.client, At: s.now()})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.log.Info("release unblocked through a view", "view", r.v.Key, "version", b.Version, "hash", b.Hash,
		"by", r.client)
	return true, nil
}
