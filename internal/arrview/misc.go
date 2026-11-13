package arrview

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/store"
)

// parseDate reads a calendar bound: ISO 8601 (Ruddarr) or a plain date (LunaSea).
func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// tagFilter reads the calendar's tags parameter: comma-separated ids or labels (TagService.GetTags).
func (s *Service) tagFilter(ctx context.Context, r *request) (map[int64]bool, error) {
	raw, _ := r.query("tags")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	out := map[int64]bool{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if n, err := strconv.ParseInt(p, 10, 64); err == nil {
			out[n] = true
			continue
		}
		var id int64
		if err := s.st.Reader().QueryRowContext(ctx, `SELECT id FROM arrview_tag WHERE label = ?`, p).Scan(&id); err == nil {
			out[id] = true
		}
	}
	return out, nil
}

func hasTag(tags []int64, want map[int64]bool) bool {
	if want == nil {
		return true
	}
	for _, t := range tags {
		if want[t] {
			return true
		}
	}
	return false
}

// GET calendar (CalendarController.GetCalendar): start defaults to today, end to two days later; unmonitored=false
// keeps monitored items only. Radarr lists the movies with a release date (in cinemas, digital, physical) in the
// window, ordered by those dates; Sonarr the episodes airing in it, by airDateUtc, with includeSeries and
// includeEpisodeFile.
func (s *Service) calendar(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	now := s.now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 2)
	if v, ok := r.query("start"); ok {
		if t, ok := parseDate(v); ok {
			start = t
		}
	}
	if v, ok := r.query("end"); ok {
		if t, ok := parseDate(v); ok {
			end = t
		}
	}
	unmon := r.queryBool("unmonitored", false)
	tags, err := s.tagFilter(ctx, r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	titleIDs, episodeIDs, err := s.calendarIDs(ctx, r.v, start, end)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	es, err := s.load(ctx, r.v, titleIDs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	in := func(t time.Time) bool { return !t.Before(start) && !t.After(end) }
	if r.v.Flavour == Radarr {
		type item struct {
			e   *entry
			key time.Time
		}
		var items []item
		for _, e := range es {
			if !unmon && !e.monitored() || !hasTag(e.tags, tags) {
				continue
			}
			var first time.Time
			for _, k := range []string{"theatrical", "digital", "physical"} {
				if d, ok := e.dates[k]; ok && in(d) && (first.IsZero() || d.Before(first)) {
					first = d
				}
			}
			if !first.IsZero() {
				items = append(items, item{e, first})
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].key.Before(items[j].key) })
		out := make([]movieResource, 0, len(items))
		for _, it := range items {
			out = append(out, s.movieResource(r.v, it.e))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	eps, err := s.loadEpisodesWith(ctx, r.v, es, false, episodeIDs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	withSeries, withFile := r.queryBool("includeSeries", false), r.queryBool("includeEpisodeFile", false)
	type item struct {
		e *entry
		x *ep
	}
	var items []item
	for _, e := range es {
		if !hasTag(e.tags, tags) {
			continue
		}
		for _, x := range eps[e.id] {
			if !x.airUTC.Valid || !in(millis(x.airUTC.Int64)) || !unmon && !(x.monitored && e.monitored()) {
				continue
			}
			items = append(items, item{e, x})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].x.airUTC.Int64 < items[j].x.airUTC.Int64 })
	series := map[int64]*seriesResource{}
	if withSeries {
		var need []*entry
		for _, it := range items {
			if series[it.e.id] == nil {
				series[it.e.id] = &seriesResource{}
				need = append(need, it.e)
			}
		}
		srs, err := s.seriesResources(ctx, r.v, need)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for i := range srs {
			series[srs[i].ID-IDBase] = &srs[i]
		}
	}
	files := map[int64]*episodeFileResource{}
	if withFile {
		var tids []int64
		for _, it := range items {
			if !slices.Contains(tids, it.e.id) {
				tids = append(tids, it.e.id)
			}
		}
		if len(tids) > 0 {
			fs, ents, err := s.filesFor(ctx, r.v, tids, nil)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			seasons, err := s.episodeSeasons(ctx, tids)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			for _, x := range fs {
				f := s.episodeFileFrom(r.v, ents[x.titleID], x, seasons)
				files[x.id] = &f
			}
		}
	}
	out := make([]calendarEpisode, 0, len(items))
	for _, it := range items {
		c := calendarEpisode{episodeResource: episodeFrom(it.e, it.x)}
		if withSeries {
			c.Series = series[it.e.id]
		}
		if withFile && it.x.fileID != 0 {
			c.EpisodeFile = files[it.x.fileID]
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

// calendarIDs narrows the resource readers before they hydrate files, versions or episodes. Movie minima
// must be chosen before applying the window: an old canonical date cannot become a later release date.
func (s *Service) calendarIDs(ctx context.Context, v *View, start, end time.Time) ([]int64, []int64, error) {
	where, args := scope(v, nil)
	q := `SELECT DISTINCT t.id, e.id FROM episode e JOIN title t ON t.id=e.title_id
 JOIN version v ON v.title_id=t.id WHERE ` + where + ` AND e.removed_at IS NULL AND e.air_date_utc_at>=? AND e.air_date_utc_at<=?`
	args = append(args, store.Millis(start), store.Millis(end))
	if v.Flavour == Radarr {
		where, args = scope(v, nil)
		q = `WITH dates AS (SELECT d.title_id,d.type,MIN(d.date) date FROM title_release_date d
   WHERE d.country='' AND d.type IN ('theatrical','digital','physical') AND (d.source='tmdb-art' OR NOT EXISTS
    (SELECT 1 FROM title_meta m WHERE m.title_id=d.title_id AND m.movie_dates_at IS NOT NULL))
   GROUP BY d.title_id,d.type)
   SELECT DISTINCT t.id,0 FROM dates d JOIN title t ON t.id=d.title_id JOIN version v ON v.title_id=t.id
   WHERE ` + where + ` AND d.date>=? AND d.date<=?`
		args = append(args, start.UTC().Format(time.DateOnly), end.UTC().Format(time.DateOnly))
	}
	rows, err := s.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	titles, episodes := []int64{}, []int64{}
	seen := map[int64]bool{}
	for rows.Next() {
		var tid, eid int64
		if err := rows.Scan(&tid, &eid); err != nil {
			return nil, nil, err
		}
		if !seen[tid] {
			titles = append(titles, tid)
			seen[tid] = true
		}
		if eid > 0 {
			episodes = append(episodes, eid)
		}
	}
	return titles, episodes, rows.Err()
}

// calendarEpisode is EpisodeResource with its optional series and episodeFile.
type calendarEpisode struct {
	episodeResource
	EpisodeFile *episodeFileResource `json:"episodeFile,omitempty"`
}

// GET wanted/cutoff (CutoffController.GetCutoffUnmetMovies / GetCutoffUnmetEpisodes): what has a file below the
// quality cutoff in a view version. The arrs list by quality only, the format score aside: Radarr
// MovieCutoffService.MoviesWhereCutoffUnmet and Sonarr EpisodeCutoffService.EpisodesWhereCutoffUnmet take the
// profile's qualities below the cutoff (the first allowed quality when upgrades are off) and select the files of
// those qualities (MovieRepository / EpisodeRepository BuildQualityCutoffWhereClause). So an item is listed when its
// version is filled (below the full cutoff) and its file's quality is below the quality cutoff (s.cutoff; without
// it every filled item). Radarr pages movies (sort movieMetadata.sortTitle), Sonarr episodes (sort
// episodes.airDateUtc); monitored=true (default) keeps monitored ones.
func (s *Service) wantedCutoff(w http.ResponseWriter, r *request) {
	s.wantedPage(w, r, true)
}

// tagDetailsResource is TagDetailsResource: Radarr has releaseProfileIds and movieIds, Sonarr restrictionIds and
// seriesIds. Our tags hold titles only; the other lists are empty, except notificationIds.
type tagDetailsResource struct {
	ID                int64    `json:"id,omitempty"`
	Label             string   `json:"label"`
	DelayProfileIDs   []int64  `json:"delayProfileIds"`
	ImportListIDs     []int64  `json:"importListIds"`
	NotificationIDs   []int64  `json:"notificationIds"`
	ReleaseProfileIDs *[]int64 `json:"releaseProfileIds,omitempty"`
	RestrictionIDs    *[]int64 `json:"restrictionIds,omitempty"`
	IndexerIDs        []int64  `json:"indexerIds"`
	DownloadClientIDs []int64  `json:"downloadClientIds"`
	AutoTagIDs        []int64  `json:"autoTagIds"`
	MovieIDs          *[]int64 `json:"movieIds,omitempty"`
	SeriesIDs         *[]int64 `json:"seriesIds,omitempty"`
}

// GET tag/detail and tag/detail/{id} (TagDetailsController): each tag with the view's titles that carry it
// (upgrade scripts read movieIds / seriesIds) and the view's notifications that do.
func (s *Service) tagDetails(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	ts, err := s.allTags(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	es, err := s.load(ctx, r.v, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byTag := map[int64][]int64{}
	for _, e := range es {
		for _, t := range e.tags {
			byTag[t] = append(byTag[t], e.viewID())
		}
	}
	notes, err := s.notificationTags(ctx, r.v)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]tagDetailsResource, 0, len(ts))
	for _, t := range ts {
		d := tagDetailsResource{ID: t.ID, Label: t.Label, DelayProfileIDs: []int64{}, ImportListIDs: []int64{},
			NotificationIDs: nonNilIDs(notes[t.ID]), IndexerIDs: []int64{}, DownloadClientIDs: []int64{},
			AutoTagIDs: []int64{}}
		ids := nonNilIDs(byTag[t.ID])
		empty := []int64{}
		if r.v.Flavour == Radarr {
			d.ReleaseProfileIDs, d.MovieIDs = &empty, &ids
		} else {
			d.RestrictionIDs, d.SeriesIDs = &empty, &ids
		}
		out = append(out, d)
	}
	if id, ok := r.pathID(2); ok {
		for _, d := range out {
			if d.ID == id {
				writeJSON(w, http.StatusOK, d)
				return
			}
		}
		notFound(w, "Tag with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNilIDs(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

// indexerResource is IndexerResource (ProviderResource plus the indexer switches): a read-only list of the policy
// indexers the view's versions search. Upgrade scripts read name, id and protocol; the ids are what release/push and
// release take as indexerId.
type indexerResource struct {
	ID                      int64   `json:"id,omitempty"`
	Name                    string  `json:"name"`
	Fields                  []field `json:"fields"`
	ImplementationName      string  `json:"implementationName"`
	Implementation          string  `json:"implementation"`
	ConfigContract          string  `json:"configContract"`
	InfoLink                string  `json:"infoLink,omitempty"`
	Tags                    []int64 `json:"tags"`
	EnableRss               bool    `json:"enableRss"`
	EnableAutomaticSearch   bool    `json:"enableAutomaticSearch"`
	EnableInteractiveSearch bool    `json:"enableInteractiveSearch"`
	SupportsRss             bool    `json:"supportsRss"`
	SupportsSearch          bool    `json:"supportsSearch"`
	Protocol                string  `json:"protocol"`
	Priority                int     `json:"priority"`
	DownloadClientID        int     `json:"downloadClientId"`
	SeasonSearchMaxAge      *int    `json:"seasonSearchMaximumSingleEpisodeAge,omitempty"`
}

// field is the arrs' provider Field.
type field struct {
	Order    int    `json:"order"`
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Value    any    `json:"value,omitempty"`
	Type     string `json:"type"`
	Advanced bool   `json:"advanced"`
	Privacy  string `json:"privacy,omitempty"`
	HelpText string `json:"helpText,omitempty"`
	IsFloat  bool   `json:"isFloat"`
}

// GET indexer and indexer/{id}: no URLs or keys are written; the fields carry the kind and privacy only.
func (s *Service) indexers(w http.ResponseWriter, r *request) {
	ids := s.indexerIDs()
	used := map[string]bool{}
	for _, k := range r.v.Versions {
		if vp := s.pol.Versions[k]; vp != nil {
			for _, ix := range vp.Indexers {
				used[ix] = true
			}
		}
	}
	out := []indexerResource{}
	for _, k := range sortedKeys(s.pol.Indexers) {
		if !used[k] {
			continue
		}
		ix := s.pol.Indexers[k]
		impl := "Torznab"
		res := indexerResource{ID: ids[k], Name: k, ImplementationName: impl, Implementation: impl,
			ConfigContract: "TorznabSettings", Tags: []int64{}, EnableRss: true, EnableAutomaticSearch: true,
			EnableInteractiveSearch: true, SupportsRss: true, SupportsSearch: true, Protocol: "torrent", Priority: 25,
			Fields: []field{{Order: 0, Name: "kind", Label: "Kind", Value: ix.Kind, Type: "textbox"},
				{Order: 1, Name: "privacy", Label: "Privacy", Value: string(ix.Privacy), Type: "textbox"}}}
		if r.v.Flavour == Sonarr {
			zero := 0
			res.SeasonSearchMaxAge = &zero
		}
		out = append(out, res)
	}
	if id, ok := r.pathID(1); ok {
		for _, x := range out {
			if x.ID == id {
				writeJSON(w, http.StatusOK, x)
				return
			}
		}
		notFound(w, "Indexer with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// editorIn is MovieEditorResource / SeriesEditorResource as PUT movie|series/editor reads it.
type editorIn struct {
	MovieIDs            []int64  `json:"movieIds"`
	SeriesIDs           []int64  `json:"seriesIds"`
	Monitored           *bool    `json:"monitored"`
	QualityProfileID    *int64   `json:"qualityProfileId"`
	MinimumAvailability enumText `json:"minimumAvailability"`
	RootFolderPath      string   `json:"rootFolderPath"`
	Tags                []int64  `json:"tags"`
	ApplyTags           enumText `json:"applyTags"`
	MonitorNewItems     enumText `json:"monitorNewItems"`
	SeriesType          enumText `json:"seriesType"`
	SeasonFolder        *bool    `json:"seasonFolder"`
}

// PUT movie/editor and series/editor (MovieEditorController / SeriesEditorController.SaveAll): bulk changes to the
// view's versions of the titles, as a full PUT would make them (except that a supplied minimum availability is
// always stored, where a PUT skips the value the view shows): monitored, profile (turns on its versions, never
// turns any off), minimum availability, root folder, series type, season folder, monitor new items, and tags with
// applyTags add (default), remove or replace. Files never move. 202 with the titles.
func (s *Service) editor(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var in editorIn
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	ids := in.MovieIDs
	if r.v.Flavour == Sonarr {
		ids = in.SeriesIDs
	}
	// Non-nil: load reads nil as every title, and ids that resolve to none must edit none.
	tids := []int64{}
	for _, id := range ids {
		tid := resolveID(id)
		if tid > 0 && !slices.Contains(tids, tid) {
			tids = append(tids, tid)
		}
	}
	if in.QualityProfileID != nil && r.v.profileByID(*in.QualityProfileID) == nil {
		badRequest(w, "QualityProfileId", "Quality Profile does not exist", *in.QualityProfileID)
		return
	}
	if len(tids) == 0 {
		// The arrs' GetMovies / GetSeries of unknown ids is empty, so SaveAll changes nothing and answers [].
		writeJSON(w, http.StatusAccepted, []struct{}{})
		return
	}
	es, err := s.load(ctx, r.v, tids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	apply := in.ApplyTags.value("add", "remove", "replace")
	if apply == "" {
		apply = "add"
	}
	v := r.v
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		now := store.Millis(s.now())
		for _, e := range es {
			mon := e.monitored()
			if in.Monitored != nil {
				mon = *in.Monitored
			}
			ti := titleIn{RootFolderPath: in.RootFolderPath, SeasonFolder: in.SeasonFolder, SeriesType: in.SeriesType,
				MonitorNewItems: in.MonitorNewItems}
			if v.Flavour == Radarr {
				// A supplied minimum availability is a deliberate edit, as Radarr applies it only when the field
				// is present (MovieEditorController.cs:64): a choice for each library the view shows for the movie,
				// even when it equals the value shown. Without the field nothing changes.
				if err := s.chooseMinAvail(ctx, tx, e.id, e.versionKeys(), in.MinimumAvailability.minAvail()); err != nil {
					return err
				}
			}
			if in.QualityProfileID != nil {
				ti.QualityProfileID = *in.QualityProfileID
				if p := v.profileByID(ti.QualityProfileID); p != nil {
					// Anime as acquisition will see the title after this edit: its flag, or its type.
					after := e.seriesType
					if typ := in.SeriesType.value(seriesTypes...); v.Flavour == Sonarr && typ != "" {
						after = typ
					}
					keys, err := s.chooseVersions(v, &ti, e.anime || after == "anime")
					if err != nil && err != errNoVersion {
						return err
					}
					if _, err := s.turnOn(ctx, tx, v, e.id, keys, mon); err != nil {
						return err
					}
				}
			}
			if in.Monitored != nil {
				vids, err := viewVersionIDs(ctx, tx, v, e.id)
				if err != nil {
					return err
				}
				for _, id := range vids {
					if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = ?, updated_at = ? WHERE id = ?
						AND monitored <> ?`, mon, now, id, mon); err != nil {
						return err
					}
				}
			}
			if v.Flavour == Sonarr {
				// A series type the editor is given is an edit of the title's type, also when it is the one shown.
				if err := catalog.SetSeriesType(ctx, tx, e.id, in.SeriesType.value(seriesTypes...)); err != nil {
					return err
				}
			}
			if err := s.saveRecord(ctx, tx, v, e.id, &ti); err != nil {
				return err
			}
			// monitorNewItems is the versions' intent too, as in PUT series (applySeasons).
			if mn := in.MonitorNewItems.value("all", "none"); v.Flavour == Sonarr && mn != "" {
				if err := catalog.SetMonitorNew(ctx, tx, e.id, v.Versions, mn, s.now()); err != nil {
					return err
				}
			}
			if in.Tags != nil {
				switch apply {
				case "replace":
					err = setTags(ctx, tx, v, e.id, in.Tags, true)
				case "remove":
					err = removeTags(ctx, tx, v, e.id, in.Tags)
				default:
					err = setTags(ctx, tx, v, e.id, in.Tags, false)
				}
				if err != nil {
					return err
				}
			}
			if err := syncTitleMonitored(ctx, tx, e.id, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	es, err = s.load(ctx, r.v, tids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.v.Flavour == Radarr {
		out := make([]movieResource, 0, len(es))
		for _, e := range es {
			out = append(out, s.movieResource(r.v, e))
		}
		writeJSON(w, http.StatusAccepted, out)
		return
	}
	srs, err := s.seriesResources(ctx, r.v, es)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, srs)
}
