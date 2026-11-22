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

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// HistoryBase starts the history ids of the router's own downloads: HistoryBase + download id × 10 + the event
// (1 grabbed, 3 imported, 4 failed, as the arrs number them), plus, in the Sonarr view, the episode's position in
// the download's episodes × historyEpisodeStride (0 for the first, so a single episode keeps the plain id).
// Imported arr history keeps its row ids, far below.
const HistoryBase = 1_000_000_000

// historyEpisodeStride separates the records of a pack's episodes: below it sit HistoryBase + download id × 10 +
// event for any download id under ~10^11; 2^40 leaves 8191 positions within JSON's exact integer range.
const historyEpisodeStride = 1 << 40

// historyID is the history id of a download's event for the episode at position i of its episodes.
func historyID(download int64, code, i int) int64 {
	return HistoryBase + download*10 + int64(code) + int64(i)*historyEpisodeStride
}

// historyDownload is the download a local history id belongs to.
func historyDownload(id int64) int64 {
	return (id - HistoryBase) % historyEpisodeStride / 10
}

const (
	histGrabbed  = 1
	histImported = 3
	histFailed   = 4
)

// downloadHistory turns the router's downloads of the view's versions into history records: grabbed when a
// provider took it (added_at), downloadFolderImported when the library linked it, downloadFailed when it failed,
// after the grab or before any provider took it (the router's reason, e.g. "no provider of the version has an API
// key set" or "miss on every provider", is data.message), except a candidate the router passed over (passedOverSQL). In the Sonarr view each event has one record per episode of the
// download (the arr writes one per episode), with its own id (historyID).
//
// The failed record carries the keys of the arrs' HistoryService.Handle(DownloadFailedEvent): downloadClient,
// downloadClientName, message, releaseGroup, size, and (Radarr) indexer.
func (s *Service) downloadHistory(r *request, ids []int64) ([]historyResource, error) {
	v := r.v
	if len(v.Versions) == 0 {
		return nil, nil
	}
	q := `SELECT id, title_id, info_hash, release_title, indexer, quality, score, COALESCE(size_bytes, 0), episodes,
		COALESCE(provider, ''), state, COALESCE(error, ''), source, added_at, imported_at, updated_at FROM download
		WHERE (added_at IS NOT NULL OR state = 'failed') AND version_key IN (` + store.Placeholders(len(v.Versions)) + `)`
	args := make([]any, 0, len(v.Versions)+2)
	for _, k := range v.Versions {
		args = append(args, k)
	}
	q += ` AND id IN (SELECT value FROM json_each(?))`
	args = append(args, jsonIDs(ids))
	rows, err := s.st.Reader().QueryContext(r.Context(), q, args...)
	if err != nil {
		return nil, err
	}
	var out []historyResource
	for rows.Next() {
		var id, tid, score, size int64
		var hash, rel, idx, quality, eps, provider, state, errMsg, source string
		var added, imported sql.NullInt64
		var updated int64
		if err := rows.Scan(&id, &tid, &hash, &rel, &idx, &quality, &score, &size, &eps, &provider, &state, &errMsg,
			&source, &added, &imported, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		var epIDs []int64
		_ = json.Unmarshal([]byte(eps), &epIDs)
		base := historyResource{Languages: []language{}, Quality: qualityModelFor(v.Flavour, quality, 1),
			CustomFormats: []any{}, CustomFormatScore: int(score), SourceTitle: rel, DownloadID: strings.ToUpper(hash)}
		vid := viewID(tid)
		if v.Flavour == Sonarr {
			base.SeriesID = &vid
		} else {
			base.MovieID = &vid
		}
		client := "theatarr router"
		if provider != "" {
			client += " (" + provider + ")"
		}
		releaseSource := "Search"
		if strings.HasPrefix(source, "arrview:release") {
			releaseSource = "InteractiveSearch"
		} else if strings.HasPrefix(source, "arrview:push") {
			releaseSource = "ReleasePush"
		}
		add := func(code int, name string, when int64, data map[string]string) {
			h := base
			h.EventType, h.Date, h.Data = name, at(millis(when)), data
			if v.Flavour != Sonarr || len(epIDs) == 0 {
				h.ID = historyID(id, code, 0)
				out = append(out, h)
				return
			}
			for i, e := range epIDs {
				h.ID, h.EpisodeID = historyID(id, code, i), &e
				out = append(out, h)
			}
		}
		if added.Valid {
			add(histGrabbed, "grabbed", added.Int64, map[string]string{"indexer": idx,
				"size": strconv.FormatInt(size, 10), "downloadClient": "theatarr router", "downloadClientName": client,
				"guid": releaseGUID(hash, idx), "protocol": "2", "releaseSource": releaseSource,
				"torrentInfoHash": strings.ToUpper(hash)})
		}
		if imported.Valid {
			add(histImported, "downloadFolderImported", imported.Int64, map[string]string{
				"downloadClient": "theatarr router", "downloadClientName": client, "importedPath": ""})
		}
		if state == router.StateFailed {
			data := map[string]string{"downloadClient": "theatarr router", "downloadClientName": client, "message": errMsg,
				"releaseGroup": releaseGroupOf(v.Flavour, rel), "size": strconv.FormatInt(size, 10)}
			if v.Flavour == Radarr {
				data["indexer"] = idx
			}
			add(histFailed, "downloadFailed", updated, data)
		}
	}
	return out, closeRows(rows)
}

// passedOverSQL is a download (alias d) every provider missed before any took it that the router chose itself
// (activity.MissedSQL): a candidate passed over for the next one, not a failed download, so History has no
// downloadFailed for it. A release the client chose (an interactive grab or a push, source arrview:...) keeps it: the
// client waits for that release's outcome. historyPage selects the events, so downloadHistory needs no test of its own.
var passedOverSQL = "(" + activity.MissedSQL("d") + " AND d.source NOT GLOB 'arrview:*')"

// releaseGroupOf is the release group our parser port reads from a release name, or "".
func releaseGroupOf(flavour, rel string) string {
	g := radarr.ParseReleaseGroup(rel)
	if flavour == Sonarr {
		g = sonarr.ParseReleaseGroup(rel)
	}
	if g == nil {
		return ""
	}
	return *g
}

// releaseGUID is the guid the views give a release: never the indexer's own (it can be a URL with a passkey), but
// "theatarr:<indexer>:<info hash>", stable across searches.
func releaseGUID(hash, idx string) string {
	return "theatarr:" + idx + ":" + strings.ToLower(hash)
}

// POST history/failed/{id} (HistoryController.MarkAsFailed): the grab failed. The download's record ends (state
// failed) and its hash is blocklisted for the version. Then, as the arrs' autoRedownloadFailed (on in config/downloadclient), a search
// starts for the title's active versions. Nothing is removed on a provider. 200 with no body; 404 when the view has
// no such record.
func (s *Service) historyFailed(w http.ResponseWriter, r *request) {
	id, ok := r.pathID(2)
	if !ok {
		notFound(w, "History not found")
		return
	}
	ctx := r.Context()
	reason := "marked failed by " + clientName(r)
	if id >= HistoryBase {
		dl := historyDownload(id)
		// A later episode's record exists only in the Sonarr view, for a download with that many episodes.
		if i := (id - HistoryBase) / historyEpisodeStride; i > 0 {
			var n int64
			err := s.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(json_array_length(episodes), 0) FROM download WHERE id = ?`, dl).Scan(&n)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				s.internal(w, r, err)
				return
			}
			if r.v.Flavour != Sonarr || i >= n {
				notFound(w, "History with ID "+strconv.FormatInt(id, 10)+" does not exist")
				return
			}
		}
		found, err := s.removeDownload(ctx, r, dl, reason, true, router.BlocklistHistory, true)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !found {
			notFound(w, "History with ID "+strconv.FormatInt(id, 10)+" does not exist")
			return
		}
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	notFound(w, "History with ID "+strconv.FormatInt(id, 10)+" does not exist")
}

// redownload starts the search the arrs' failed-download handling starts for a failed download (Sonarr's
// RedownloadFailedDownloadService), in the download's library (version) only, never another library of the title
// (Selection.NoJoin): a movie its title (MoviesSearch); a series download its one episode (EpisodeSearch), its whole
// season (SeasonSearch), else its episodes; several seasons those seasons together, as a series search of just
// them; no episodes known the series (SeriesSearch).
func (s *Service) redownload(ctx context.Context, r *request, titleID int64, version string, episodes []int64) error {
	name := "MoviesSearch"
	body := map[string]any{"name": name, "movieIds": []any{float64(viewID(titleID))}}
	var seasons []int
	if r.v.Flavour == Sonarr {
		name = "SeriesSearch"
		body = map[string]any{"name": name, "seriesId": float64(viewID(titleID))}
		var err error
		if name, seasons, err = s.retryScope(ctx, titleID, episodes, body); err != nil {
			return err
		}
	}
	_, err := s.runScopedCommand(ctx, r.v, name, body, r.Header.Get("User-Agent"), func(sc *commandScope) {
		sc.versions = slices.DeleteFunc(sc.versions, func(k string) bool { return k != version })
		sc.sel.NoJoin, sc.sel.Seasons = true, seasons
	})
	return err
}

// retryScope fills body for a series download's retry (redownload) and names its command: EpisodeSearch for one
// episode or part of one season, SeasonSearch for the whole of one season (every current episode of it), and
// SeriesSearch with the seasons for several; SeriesSearch alone when no episode is known.
func (s *Service) retryScope(ctx context.Context, titleID int64, episodes []int64, body map[string]any) (string, []int, error) {
	if len(episodes) == 0 {
		return "SeriesSearch", nil, nil
	}
	ids := make([]any, len(episodes))
	for i, id := range episodes {
		ids[i] = id
	}
	var seasons []int
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT DISTINCT season FROM episode WHERE id IN (`+
		store.Placeholders(len(ids))+`) ORDER BY season`, ids...)
	if err != nil {
		return "", nil, err
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return "", nil, err
		}
		seasons = append(seasons, n)
	}
	if err := closeRows(rows); err != nil {
		return "", nil, err
	}
	if len(seasons) > 1 {
		return "SeriesSearch", seasons, nil
	}
	episodeSearch := func() (string, []int, error) {
		delete(body, "seriesId")
		list := make([]any, len(episodes))
		for i, id := range episodes {
			list[i] = float64(id)
		}
		body["name"], body["episodeIds"] = "EpisodeSearch", list
		return "EpisodeSearch", nil, nil
	}
	if len(episodes) == 1 || len(seasons) == 0 {
		return episodeSearch()
	}
	var inSeason int
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM episode WHERE title_id = ? AND season = ?
		AND removed_at IS NULL`, titleID, seasons[0]).Scan(&inSeason); err != nil {
		return "", nil, err
	}
	if len(episodes) != inSeason {
		return episodeSearch()
	}
	body["name"], body["seasonNumber"] = "SeasonSearch", float64(seasons[0])
	return "SeasonSearch", nil, nil
}
