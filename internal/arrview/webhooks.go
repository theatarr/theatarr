package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/store"
)

// Outbound webhooks, arr-shaped (docs/design/devices.md "Webhooks"): the bodies Radarr's and Sonarr's Webhook
// notification sends (src/NzbDrone.Core/Notifications/Webhook/WebhookBase.cs @ a96bf7c and cab419a), per view.
// internal/notify records and sends them; Payloads builds them. The arrs serialise these with Newtonsoft
// (camelCase, nulls left out, enums camelCase) except eventType, which is PascalCase ("Grab").
//

func (s *Service) emit(ctx context.Context, ev notify.Event) {
	if s.notifier == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = s.now()
	}
	s.notifier.Emit(ctx, ev)
}

func (s *Service) emitFileDelete(ctx context.Context, v *View, x *fileRow) {
	s.emit(ctx, notify.Event{Kind: notify.FileDelete, View: v.Key, TitleID: x.titleID, Version: x.version,
		Episodes: x.episodes, Reason: "manual", File: &notify.File{ID: x.id, Path: x.f.path,
			RelativePath: x.f.relPath, Size: x.f.size, Quality: x.f.quality, SceneName: x.f.scene,
			ReleaseGroup: x.f.group}})
}

// obj is a JSON object that leaves out nil values and empty strings, as Newtonsoft's NullValueHandling.Ignore does
// for the arrs' null references.
type obj map[string]any

func (o obj) set(k string, v any) obj {
	switch x := v.(type) {
	case nil:
		return o
	case string:
		if x == "" {
			return o
		}
	}
	o[k] = v
	return o
}

// Payloads builds the arr-shaped bodies of an event, one per view it concerns: the event's own view (deletes,
// tests), or every view that holds the event's version (pipeline events), or every view (health).
func (s *Service) Payloads(ctx context.Context, ev notify.Event) ([]notify.Payload, error) {
	var out []notify.Payload
	for _, v := range s.views {
		switch {
		case ev.View != "":
			if v.Key != ev.View {
				continue
			}
		case ev.Kind == notify.Health || ev.Kind == notify.HealthRestored:
			if strings.HasPrefix(v.Key, "v/") {
				continue // the per-version views repeat the configured ones' health
			}
		case !slices.Contains(v.Versions, ev.Version):
			continue
		}
		p, err := s.payload(ctx, v, ev)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *Service) payload(ctx context.Context, v *View, ev notify.Event) (*notify.Payload, error) {
	base := func(eventType string) obj {
		return obj{"eventType": eventType, "instanceName": "theatarr " + v.Key}
	}
	movie := v.Flavour == Radarr
	switch ev.Kind {
	case notify.Test:
		return &notify.Payload{View: v.Key, EventType: "Test", Body: testPayload(v)}, nil
	case notify.Health, notify.HealthRestored:
		if len(ev.Health) == 0 {
			return nil, nil
		}
		h := ev.Health[0]
		eventType, flag := "Health", "onHealthIssue"
		if ev.Kind == notify.HealthRestored {
			eventType, flag = "HealthRestored", "onHealthRestored"
		}
		b := base(eventType).set("level", h.Type).set("message", h.Message).set("type", h.Source)
		b["wikiUrl"] = h.WikiURL
		return &notify.Payload{View: v.Key, EventType: eventType, Flag: flag, Body: b}, nil
	}
	e, err := s.loadOne(ctx, v, ev.TitleID)
	if err != nil || e == nil {
		return nil, err
	}
	media, key := s.webhookMedia(ctx, v, e)
	b := base("")
	b[key] = media
	var eps []obj
	if !movie {
		if eps, err = s.webhookEpisodes(ctx, e, ev.Episodes); err != nil {
			return nil, err
		}
		b["episodes"] = eps
	}
	switch ev.Kind {
	case notify.SeriesAdd, notify.MovieAdded:
		delete(b, "episodes")
		eventType, flag := "SeriesAdd", "onSeriesAdd"
		if movie {
			eventType, flag = "MovieAdded", "onMovieAdded"
			var method string
			if err := s.st.Reader().QueryRowContext(ctx, `SELECT add_method FROM version WHERE title_id = ? AND policy_key = ?`,
				ev.TitleID, ev.Version).Scan(&method); err != nil {
				return nil, err
			}
			b["addMethod"] = method
		}
		b["eventType"] = eventType
		return &notify.Payload{View: v.Key, EventType: eventType, Flag: flag, Body: b}, nil
	case notify.Delete:
		if movie {
			b["eventType"] = "MovieDelete"
			b["deletedFiles"], b["movieFolderSize"] = false, 0
			return &notify.Payload{View: v.Key, EventType: "MovieDelete", Flag: "onMovieDelete", Body: b}, nil
		}
		delete(b, "episodes")
		b["eventType"], b["deletedFiles"] = "SeriesDelete", false
		return &notify.Payload{View: v.Key, EventType: "SeriesDelete", Flag: "onSeriesDelete", Body: b}, nil
	case notify.FileDelete:
		f := webhookFile(v, ev.File)
		b.set("deleteReason", ev.Reason)
		if movie {
			b["eventType"], b["movieFile"] = "MovieFileDelete", f
			return &notify.Payload{View: v.Key, EventType: "MovieFileDelete", Flag: "onMovieFileDelete", Body: b}, nil
		}
		b["eventType"], b["episodeFile"] = "EpisodeFileDelete", f
		return &notify.Payload{View: v.Key, EventType: "EpisodeFileDelete", Flag: "onEpisodeFileDelete", Body: b}, nil
	case notify.Rename:
		b["eventType"] = "Rename"
		delete(b, "episodes")
		files := []obj{}
		for _, renamed := range ev.RenamedFiles {
			f := webhookFile(v, &renamed.File)
			f["qualityVersion"] = max(1, renamed.File.QualityVersion)
			f.set("previousPath", renamed.PreviousPath).set("previousRelativePath", renamed.PreviousRelativePath)
			files = append(files, f)
		}
		fileKey := "renamedEpisodeFiles"
		if movie {
			fileKey = "renamedMovieFiles"
		}
		b[fileKey] = files
		return &notify.Payload{View: v.Key, EventType: "Rename", Flag: "onRename", Body: b}, nil
	}
	// Grab, Download, Upgrade: from our download.
	dl, err := s.webhookDownload(ctx, ev.DownloadID)
	if err != nil || dl == nil {
		return nil, err
	}
	if movie {
		b["remoteMovie"] = obj{"tmdbId": e.tmdb, "imdbId": e.imdb, "title": e.name, "year": e.year}
	}
	b["downloadClient"], b["downloadClientType"] = dl.client, "theatarr"
	b["downloadId"] = strings.ToUpper(dl.hash)
	b["customFormatInfo"] = obj{"customFormats": formatIDs(dl.formats), "customFormatScore": dl.score}
	if ev.Kind == notify.ManualInteractionRequired {
		delete(b, "remoteMovie")
		b["eventType"] = "ManualInteractionRequired"
		// As the arrs' WebhookDownloadClientItem and WebhookGrabbedRelease: the release name and indexer travel
		// with a held import.
		b["downloadInfo"] = obj{"quality": dl.quality, "qualityVersion": 1, "title": dl.title, "indexer": dl.indexer,
			"size": dl.size}
		b["release"] = obj{"releaseTitle": dl.title, "indexer": dl.indexer, "size": dl.size}
		b["downloadStatus"] = "Warning"
		b["downloadStatusMessages"] = []obj{{"title": "Import requires manual interaction",
			"messages": []string{ev.Reason}}}
		return &notify.Payload{View: v.Key, EventType: "ManualInteractionRequired",
			Flag: "onManualInteractionRequired", Body: b}, nil
	}
	if ev.Kind == notify.Grab {
		rel := obj{"quality": dl.quality, "qualityVersion": 1, "languages": []language{},
			"releaseTitle": dl.title, "indexer": dl.indexer, "size": dl.size, "customFormatScore": dl.score,
			"customFormats": formatNames(dl.formats)}
		if movie {
			rel["indexerFlags"] = []string{}
		}
		b["eventType"], b["release"] = "Grab", rel
		return &notify.Payload{View: v.Key, EventType: "Grab", Flag: "onGrab", Body: b}, nil
	}
	upgrade := ev.Kind == notify.Upgrade
	b["eventType"], b["isUpgrade"] = "Download", upgrade
	rel := obj{"releaseTitle": dl.title, "indexer": dl.indexer, "size": dl.size}
	if movie {
		rel["indexerFlags"] = []string{}
	}
	b["release"] = rel
	file := ev.File
	if file == nil {
		file = s.downloadFile(ctx, ev.DownloadID)
	}
	if file != nil {
		f := webhookFile(v, file)
		f["quality"], f["qualityVersion"] = dl.quality, 1
		if movie {
			b["movieFile"] = f
		} else {
			b["episodeFile"] = f
		}
	}
	flag := "onDownload"
	if upgrade {
		flag = "onUpgrade"
		b["deletedFiles"] = []obj{}
	}
	return &notify.Payload{View: v.Key, EventType: "Download", Flag: flag, Body: b}, nil
}

// webhookMedia is WebhookMovie (Radarr) or WebhookSeries (Sonarr), and the key it goes under.
func (s *Service) webhookMedia(ctx context.Context, v *View, e *entry) (obj, string) {
	_, p := s.rootAndPath(v, e)
	tags := s.tagLabels(ctx, e.tags)
	if v.Flavour == Radarr {
		m := obj{"id": e.viewID(), "title": e.name, "year": e.year, "tmdbId": e.tmdb, "genres": []string{},
			"images": []any{}, "tags": tags}
		m.set("folderPath", p).set("imdbId", e.imdb)
		if d, ok := e.dates["physical"]; ok {
			m["releaseDate"] = d.Format("2006-01-02")
		}
		return m, "movie"
	}
	m := obj{"id": e.viewID(), "title": e.name, "titleSlug": slug(e.name), "tvdbId": e.tvdb, "tvMazeId": 0,
		"tmdbId": 0, "type": e.seriesType, "year": e.year, "genres": []string{}, "images": []any{}, "tags": tags}
	m.set("path", p).set("imdbId", e.imdb)
	return m, "series"
}

func (s *Service) tagLabels(ctx context.Context, ids []int64) []string {
	out := []string{}
	for _, id := range ids {
		var l string
		if s.st.Reader().QueryRowContext(ctx, `SELECT label FROM arrview_tag WHERE id = ?`, id).Scan(&l) == nil {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

// webhookEpisodes is the WebhookEpisode list of the given episodes.
func (s *Service) webhookEpisodes(ctx context.Context, e *entry, ids []int64) ([]obj, error) {
	out := []obj{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, season, number, name, COALESCE(air_date, ''),
		air_date_utc_at FROM episode WHERE id IN (`+store.Placeholders(len(ids))+`) ORDER BY season, number`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var season, number int
		var name, air string
		var utc sql.NullInt64
		if err := rows.Scan(&id, &season, &number, &name, &air, &utc); err != nil {
			rows.Close()
			return nil, err
		}
		o := obj{"id": id, "episodeNumber": number, "seasonNumber": season, "seriesId": e.viewID(), "tvdbId": 0}
		o.set("title", name).set("airDate", air)
		if utc.Valid {
			o["airDateUtc"] = at(millis(utc.Int64))
		}
		out = append(out, o)
	}
	return out, closeRows(rows)
}

// webhookFile is the WebhookMovieFile / WebhookEpisodeFile of a file.
func webhookFile(v *View, f *notify.File) obj {
	if f == nil {
		return obj{}
	}
	o := obj{"id": f.ID, "size": f.Size}
	o.set("quality", f.Quality)
	o.set("relativePath", f.RelativePath).set("path", f.Path).set("releaseGroup", f.ReleaseGroup).
		set("sceneName", f.SceneName)
	if v.Flavour == Radarr {
		o["indexerFlags"] = "0"
	}
	return o
}

type webhookDL struct {
	hash, title, indexer, quality, client string
	score                                 int
	size                                  int64
	formats                               []decide.FormatScore
}

func (s *Service) webhookDownload(ctx context.Context, id int64) (*webhookDL, error) {
	d := &webhookDL{}
	var formats sql.NullString
	var provider string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT info_hash, release_title, indexer, quality, score,
		COALESCE(size_bytes, 0), COALESCE(provider, ''), formats FROM download WHERE id = ?`, id).Scan(&d.hash, &d.title,
		&d.indexer, &d.quality, &d.score, &d.size, &provider, &formats)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.client = "theatarr router"
	if provider != "" {
		d.client += " (" + provider + ")"
	}
	if formats.Valid {
		_ = json.Unmarshal([]byte(formats.String), &d.formats)
	}
	return d, nil
}

func formatNames(fs []decide.FormatScore) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// testPayload is the arrs' Test body (WebhookBase.Test): fixed sample data.
func testPayload(v *View) obj {
	b := obj{"eventType": "Test", "instanceName": "theatarr " + v.Key}
	if v.Flavour == Radarr {
		b["movie"] = obj{"id": 1, "title": "Test Title", "year": 1970, "folderPath": "C:\\testpath",
			"releaseDate": "1970-01-01", "tmdbId": 0, "genres": []string{}, "images": []any{}, "tags": []string{"test-tag"}}
		b["remoteMovie"] = obj{"tmdbId": 1234, "imdbId": "5678", "title": "Test title", "year": 1970}
		b["release"] = obj{"quality": "Test Quality", "qualityVersion": 1, "releaseGroup": "Test Group",
			"releaseTitle": "Test Title", "indexer": "Test Indexer", "size": 9999999, "customFormatScore": 0}
		return b
	}
	b["series"] = obj{"id": 1, "title": "Test Title", "path": "C:\\testpath", "tvdbId": 1234, "tvMazeId": 0,
		"tmdbId": 0, "type": "standard", "year": 0, "genres": []string{}, "images": []any{}, "tags": []string{"test-tag"}}
	b["episodes"] = []obj{{"id": 123, "episodeNumber": 1, "seasonNumber": 1, "title": "Test title", "seriesId": 0,
		"tvdbId": 0}}
	return b
}

// downloadFile is the first library link an import of the download made, as a file of the views.
func (s *Service) downloadFile(ctx context.Context, downloadID int64) *notify.File {
	f := &notify.File{}
	var id int64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT l.id, l.path, COALESCE(cf.size_bytes, 0), COALESCE(l.quality, ''),
		COALESCE(l.release_title, '') FROM link l LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE l.download_id = ? ORDER BY l.id LIMIT 1`, downloadID).Scan(&id, &f.Path, &f.Size, &f.Quality, &f.SceneName)
	if err != nil {
		return nil
	}
	f.ID, f.RelativePath = LinkFileBase+id, path.Base(f.Path)
	return f
}

// HealthItems is the health the views report (GET health), for the Health webhook, over the configured views'
// versions: the shadow-mode notice and the router-disabled warning (healthOf).
func (s *Service) HealthItems(ctx context.Context) []notify.HealthItem {
	var keys []string
	for _, v := range s.views {
		if strings.HasPrefix(v.Key, "v/") {
			continue
		}
		for _, k := range v.Versions {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	slices.Sort(keys)
	return s.healthOf(ctx, keys)
}
