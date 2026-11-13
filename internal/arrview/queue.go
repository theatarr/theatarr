package arrview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The queue comes from the router's download table (migration 00014, internal/router): downloads still in hand,
// i.e. queued, adding, added, downloading, ready and importing, for the view's versions. A ready import held for
// review remains in the queue until the user manually imports or removes it.

// queueResource is QueueResource (Radarr.Api.V3/Queue/QueueResource.cs @ a96bf7c; Sonarr's adds seriesId,
// episodeId, seasonNumber, series, episode and episodeHasFile). Enums are written camelCase as the arrs do. Seerr's
// download tracker reads movieId (or episode with includeEpisode), size, sizeleft, status, timeleft, title and
// downloadId; Homepage reads movieId, size, sizeleft, timeleft, status and trackedDownloadState.
type queueResource struct {
	ID                                  int64            `json:"id"`
	MovieID                             *int64           `json:"movieId,omitempty"`
	SeriesID                            *int64           `json:"seriesId,omitempty"`
	EpisodeID                           *int64           `json:"episodeId,omitempty"`
	SeasonNumber                        *int             `json:"seasonNumber,omitempty"`
	Episode                             *episodeResource `json:"episode,omitempty"`
	Movie                               *movieResource   `json:"movie,omitempty"`
	Series                              *seriesResource  `json:"series,omitempty"`
	Languages                           []language       `json:"languages"`
	Quality                             qualityModel     `json:"quality"`
	CustomFormats                       []any            `json:"customFormats"`
	CustomFormatScore                   int              `json:"customFormatScore"`
	Size                                float64          `json:"size"`
	Title                               string           `json:"title"`
	EstimatedCompletionTime             *arrTime         `json:"estimatedCompletionTime,omitempty"`
	Added                               *arrTime         `json:"added,omitempty"`
	Status                              string           `json:"status"`
	TrackedDownloadStatus               string           `json:"trackedDownloadStatus,omitempty"`
	TrackedDownloadState                string           `json:"trackedDownloadState,omitempty"`
	StatusMessages                      []any            `json:"statusMessages"`
	ErrorMessage                        string           `json:"errorMessage,omitempty"`
	DownloadID                          string           `json:"downloadId"`
	Protocol                            string           `json:"protocol"`
	DownloadClient                      string           `json:"downloadClient"`
	DownloadClientHasPostImportCategory bool             `json:"downloadClientHasPostImportCategory"`
	Indexer                             string           `json:"indexer"`
	EpisodeHasFile                      *bool            `json:"episodeHasFile,omitempty"`
	Sizeleft                            float64          `json:"sizeleft"`
	Timeleft                            *string          `json:"timeleft,omitempty"`
}

// queueRow is one download of the view's versions.
type queueRow struct {
	id, titleID          int64
	version, hash, title string
	indexer, quality     string
	score                int
	size                 int64
	episodes             []int64
	provider, name       string
	progress             float64
	state                string
	errMsg, importState  string
	importErr            string
	created              time.Time
	added, waiting       time.Time
}

// queueRows reads the downloads in hand for the view's versions, oldest first. titleIDs, when given, limits them.
// The rows come from the shared reader (internal/activity) that the native activity API also lists.
func (s *Service) queueRows(ctx context.Context, v *View, titleIDs []int64, selected ...[]queueIdentity) ([]queueRow, error) {
	if len(v.Versions) == 0 {
		return nil, nil
	}
	var downloadIDs []int64
	if len(selected) > 0 {
		downloadIDs = []int64{}
		for _, it := range selected[0] {
			if it.source > 0 {
				downloadIDs = append(downloadIDs, it.source)
			}
		}
	}
	ds, err := activity.Downloads(ctx, s.st.Reader(), activity.Filter{IDs: downloadIDs, Versions: v.Versions, TitleIDs: titleIDs,
		ArrQueueAt: s.now()})
	if err != nil {
		return nil, err
	}
	out := make([]queueRow, 0, len(ds))
	for _, d := range ds {
		out = append(out, queueRow{id: d.ID, titleID: d.TitleID, version: d.Version, hash: d.Hash, title: d.Release,
			indexer: d.Indexer, quality: d.Quality, score: d.Score, size: d.Size, episodes: d.Episodes,
			provider: d.Provider, name: d.Name, progress: d.Progress, state: d.State, errMsg: d.Error,
			importState: d.ImportState, importErr: d.ImportErr,
			created: d.Created, added: d.Added, waiting: d.WaitingSince})
	}
	return out, nil
}

// queueTracking uses the closest arr tracked-download state for each router/library wait or hold.
func (s *Service) queueTracking(x queueRow) (status, tracked, severity, message string) {
	status, tracked = queueStates(x.state)
	severity = "ok"
	message = x.errMsg
	if x.state == "failed" {
		status, tracked, severity = "failed", "failedPending", "error"
	}
	if x.importState == "rejected" || x.importState == "dry_run" ||
		strings.HasPrefix(x.importErr, library.TitleHoldPrefix) {
		status, tracked, severity, message = "completed", "importBlocked", "warning", x.importErr
		if message == "" {
			message = "Import held for review or disabled by the library gate"
		}
	} else if x.importState == "waiting_mount" {
		message = x.importErr
		if message == "" {
			message = "Waiting for the release on the mount"
		}
		if !x.waiting.IsZero() && s.now().Sub(x.waiting) >= s.pol.LibrarySettings().WaitTimeout {
			severity = "warning"
		}
	} else if x.importState == "partial" || x.importState == "failed" {
		severity, message = "error", x.importErr
		if x.importState == "partial" {
			tracked, severity = "importPending", "warning"
		}
		if message == "" {
			message = "Import failed; waiting for retry or manual resolution"
		}
	} else if message != "" {
		severity = "warning"
		if x.state == "failed" {
			severity = "error"
		}
	} else if x.state == "added" || x.state == "adding" || x.state == "queued" {
		message = "Waiting for the provider"
		stall := s.pol.ProviderStalledAfter(x.provider)
		if x.state == "added" && stall > 0 && !x.added.IsZero() && s.now().Sub(x.added) >= stall {
			severity, message = "warning", "Provider stalled; waiting for the release to become ready"
		}
	} else if x.state == "ready" {
		message = "Waiting for library import"
	} else if x.state == "importing" {
		message = "Importing library links"
	}
	return
}

// queueStates maps a download state to the arrs' queue status and tracked download state.
func queueStates(state string) (status, tracked string) {
	switch state {
	case "downloading":
		return "downloading", "downloading"
	case "ready":
		return "completed", "importPending"
	case "importing":
		return "completed", "importing"
	}
	return "queued", "downloading" // queued, adding, added
}

// queueItems builds the view's queue resources: one per download for Radarr, one per episode for Sonarr (as
// Sonarr's queue lists a pack once per episode). includeEpisode adds the episode resource (Seerr asks for it).
func (s *Service) queueItems(ctx context.Context, r *request, titleIDs []int64, includeEpisode bool, selected ...[]queueIdentity) ([]queueResource, error) {
	v := r.v
	rows, err := s.queueRows(ctx, v, titleIDs, selected...)
	if err != nil || len(rows) == 0 {
		return []queueResource{}, err
	}
	if len(selected) > 0 && v.Flavour == Sonarr {
		keep := map[queueIdentity]bool{}
		for _, it := range selected[0] {
			keep[it] = true
		}
		for i := range rows {
			rows[i].episodes = slices.DeleteFunc(rows[i].episodes, func(eid int64) bool { return !keep[queueIdentity{rows[i].id, eid}] })
		}
	}
	var eps map[int64]*episodeResource
	if v.Flavour == Sonarr {
		if eps, err = s.queueEpisodes(ctx, v, rows); err != nil {
			return nil, err
		}
	}
	// includeMovie / includeSeries embed the title (Ruddarr asks for both).
	titles := map[int64]any{}
	if r.queryBool("includeMovie", false) || r.queryBool("includeSeries", false) {
		var ids []int64
		for _, x := range rows {
			ids = append(ids, x.titleID)
		}
		es, err := s.load(ctx, v, ids)
		if err != nil {
			return nil, err
		}
		if v.Flavour == Radarr {
			for _, e := range es {
				m := s.movieResource(v, e)
				titles[e.id] = &m
			}
		} else {
			srs, err := s.seriesResources(ctx, v, es)
			if err != nil {
				return nil, err
			}
			for i := range srs {
				titles[srs[i].ID-IDBase] = &srs[i]
			}
		}
	}
	remainingByID, err := s.queueRemaining(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := []queueResource{}
	for _, x := range rows {
		status, tracked, severity, message := s.queueTracking(x)
		left := float64(x.size) * (1 - min(max(x.progress, 0), 100)/100)
		if status == "completed" {
			left = 0
		}
		item := queueResource{ID: queueItemID(x.id), Languages: []language{}, Quality: qualityModelFor(v.Flavour, x.quality, 1),
			CustomFormats: []any{}, CustomFormatScore: x.score, Size: float64(x.size), Title: x.title,
			Added: atPtr(x.created), Status: status, TrackedDownloadStatus: severity, TrackedDownloadState: tracked,
			StatusMessages: []any{}, DownloadID: strings.ToUpper(x.hash), Protocol: "torrent",
			DownloadClient: "theatarr router", Indexer: x.indexer, Sizeleft: left}
		remaining, estimated := time.Duration(0), status == "completed"
		if !estimated {
			remaining, estimated = remainingByID[x.id]
		}
		if estimated {
			seconds := int64(remaining / time.Second)
			if remaining%time.Second != 0 {
				seconds++
			}
			left := fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
			if seconds >= 86400 {
				left = fmt.Sprintf("%d.%02d:%02d:%02d", seconds/86400, seconds/3600%24, seconds/60%60, seconds%60)
			}
			item.Timeleft, item.EstimatedCompletionTime = &left, atPtr(s.now().Add(remaining))
		}
		if x.provider != "" {
			item.DownloadClient = "theatarr router (" + x.provider + ")"
		}
		if message != "" {
			item.StatusMessages = []any{map[string]any{"title": x.title, "messages": []string{message}}}
		}
		id := viewID(x.titleID)
		if v.Flavour == Radarr {
			item.MovieID = &id
			item.Movie, _ = titles[x.titleID].(*movieResource)
			out = append(out, item)
			continue
		}
		item.SeriesID = &id
		item.Series, _ = titles[x.titleID].(*seriesResource)
		if len(x.episodes) == 0 {
			out = append(out, item)
			continue
		}
		for _, eid := range x.episodes {
			it := item
			it.ID = queueEpisodeID(x.id, eid)
			e := eid
			it.EpisodeID = &e
			if ep := eps[eid]; ep != nil {
				season, has := ep.SeasonNumber, ep.HasFile
				it.SeasonNumber, it.EpisodeHasFile = &season, &has
				if includeEpisode {
					it.Episode = ep
				}
			}
			out = append(out, it)
		}
	}
	return out, nil
}

// queueEpisodes loads the episode resources the rows name, as the view sees them.
func (s *Service) queueEpisodes(ctx context.Context, v *View, rows []queueRow) (map[int64]*episodeResource, error) {
	var ids []int64
	for _, x := range rows {
		ids = append(ids, x.episodes...)
	}
	out := map[int64]*episodeResource{}
	if len(ids) == 0 {
		return out, nil
	}
	es, err := s.entriesForEpisodes(ctx, v, ids)
	if err != nil {
		return nil, err
	}
	all, err := s.loadEpisodesWith(ctx, v, es, true, ids)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		for _, x := range all[e.id] {
			r := episodeFrom(e, x)
			out[x.id] = &r
		}
	}
	return out, nil
}

// titleFilter resolves movieIds / seriesIds (view ids or aliases) to title ids; nil when none were given.
func (s *Service) titleFilter(r *request, names ...string) ([]int64, bool) {
	var ids []int64
	given := false
	for _, n := range names {
		for _, id := range r.queryInts(n) {
			given = true
			t := resolveID(id)
			if t > 0 {
				ids = append(ids, t)
			}
		}
	}
	return ids, given
}

// GET queue (QueueController.GetQueue): paged, default sort timeleft ascending, pageSize 10. Every sort key orders
// by its sort key, progress descending, then stable download/episode identity.
func (s *Service) queue(w http.ResponseWriter, r *request) {
	p := r.paging("timeleft", "ascending")
	selected, err := s.queuePage(r, &p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	items, err := s.queueItems(r.Context(), r, nil, r.queryBool("includeEpisode", false), selected)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byID := map[int64]queueResource{}
	for _, item := range items {
		byID[item.ID] = item
	}
	out := make([]queueResource, 0, len(selected))
	for _, it := range selected {
		id := queueItemID(it.source)
		if r.v.Flavour == Sonarr && it.episode != 0 {
			id = queueEpisodeID(it.source, it.episode)
		}
		if item, ok := byID[id]; ok {
			out = append(out, item)
		}
	}
	p.Records = out
	writeJSON(w, http.StatusOK, p)
}

// GET queue/details (QueueDetailsController.GetQueue): every item, unpaged; movieId (Radarr) or seriesId and
// episodeIds (Sonarr) filter it.
func (s *Service) queueDetails(w http.ResponseWriter, r *request) {
	ids, given := s.titleFilter(r, "movieId", "seriesId")
	items := []queueResource{}
	if !given || len(ids) > 0 {
		var err error
		if items, err = s.queueItems(r.Context(), r, ids, r.queryBool("includeEpisode", false)); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if want := r.queryInts("episodeIds"); len(want) > 0 {
		keep := map[int64]bool{}
		for _, id := range want {
			keep[id] = true
		}
		var out []queueResource
		for _, it := range items {
			if it.EpisodeID != nil && keep[*it.EpisodeID] {
				out = append(out, it)
			}
		}
		items = append([]queueResource{}, out...)
	}
	writeJSON(w, http.StatusOK, items)
}

// queueStatusResource is QueueStatusResource. Homepage's Radarr widget reads totalCount.
type queueStatusResource struct {
	ID              int64 `json:"id,omitempty"`
	TotalCount      int   `json:"totalCount"`
	Count           int   `json:"count"`
	UnknownCount    int   `json:"unknownCount"`
	Errors          bool  `json:"errors"`
	Warnings        bool  `json:"warnings"`
	UnknownErrors   bool  `json:"unknownErrors"`
	UnknownWarnings bool  `json:"unknownWarnings"`
}

// GET queue/status (QueueStatusController.GetQueueStatus): counts of the queue items; every item has a title, so
// none is unknown. Warnings and errors include held imports and failures awaiting manual resolution.
func (s *Service) queueStatus(w http.ResponseWriter, r *request) {
	items, err := s.queueItems(r.Context(), r, nil, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := queueStatusResource{TotalCount: len(items), Count: len(items)}
	for _, item := range items {
		out.Warnings = out.Warnings || item.TrackedDownloadStatus == "warning"
		out.Errors = out.Errors || item.TrackedDownloadStatus == "error"
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE queue/{id} (QueueController.RemoveAction) and DELETE queue/bulk (RemoveMany, body {ids}): the download
// leaves the queue. Our record of it ends (state failed, "removed from the queue"); nothing is removed on a debrid
// provider, whatever removeFromClient says (debrid torrents are shared by every library). blocklist=true blocklists
// the hash for the version (router_blocklist), and then, unless skipRedownload, a search starts for the title's
// active versions, as the arrs' failed-download handling does. Every member id of a pack removes the download
// (or pending approval release), so all its episode records leave together. changeCategory is ignored. 200 with no body.
func (s *Service) deleteQueue(w http.ResponseWriter, r *request) {
	var ids []int64
	if len(r.seg) == 2 && r.seg[1] == "bulk" {
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
		notFound(w, "Queue item not found")
		return
	}
	block := r.queryBool("blocklist", false) || r.queryBool("blacklist", false) // LunaSea sends the old name
	skip := r.queryBool("skipRedownload", false)
	single := len(ids) == 1 && !(len(r.seg) == 2 && r.seg[1] == "bulk")
	seen := map[int64]bool{}
	for _, id := range ids {
		source, err := s.queueSource(r.Context(), r.v, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if source == 0 {
			if single {
				notFound(w, "Queue item with ID "+strconv.FormatInt(id, 10)+" does not exist")
				return
			}
			continue
		}
		if seen[source] {
			continue
		}
		seen[source] = true
		ok, err := s.removeDownload(r.Context(), r, source, "removed from the queue by "+clientName(r), block,
			router.BlocklistQueue, block && !skip)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !ok && single {
			notFound(w, "Queue item with ID "+strconv.FormatInt(id, 10)+" does not exist")
			return
		}
	}
	if single {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// removeDownload ends one download of the view's versions (router.Dismiss), and with redownload starts a search for
// its title's active versions. false when the view has no such download.
func (s *Service) removeDownload(ctx context.Context, r *request, id int64, reason string, block bool, source string,
	redownload bool) (bool, error) {
	var version string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT version_key FROM download WHERE id = ?`, id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !slices.Contains(r.v.Versions, version) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d, err := router.Dismiss(ctx, s.st, id, reason, block, source, store.Millis(s.now()))
	if err != nil {
		return false, err
	}
	// Already-failed imports/stalls can now be shown for manual resolution. Dismiss leaves failed downloads
	// alone, so mark this queue removal too, retaining the original failure in the message.
	if !d.Ended && d.State == "failed" {
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE download SET error = ? || ' (was: ' || COALESCE(error, '') || ')',
			updated_at = ? WHERE id = ? AND state = 'failed'`, reason, store.Millis(s.now()), id); err != nil {
			return false, err
		}
	}
	s.log.Info("download dismissed", "view", r.v.Key, "download", id, "version", d.Version, "ended", d.Ended,
		"blocklisted", block)
	if redownload {
		if err := s.redownload(ctx, r, d.TitleID); err != nil {
			return true, err
		}
	}
	return true, nil
}

// clientName names the caller for messages: its User-Agent's product, else "a client".
func clientName(r *request) string {
	ua := strings.TrimSpace(r.Header.Get("User-Agent"))
	if ua == "" {
		return "a client"
	}
	if i := strings.IndexAny(ua, "/ "); i > 0 {
		ua = ua[:i]
	}
	if len(ua) > 40 {
		ua = ua[:40]
	}
	return ua
}
