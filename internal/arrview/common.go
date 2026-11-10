package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/store"
)

// systemResource is SystemResource as SystemController.GetStatus fills it (src/Radarr.Api.V3/System/
// SystemController.cs @ a96bf7c; Sonarr's has the same fields but isContainerized). Read by: Seerr (base.ts L117-133:
// version, urlBase), decypharr (pkg/arr/client.go L149: appName decides Radarr or Sonarr), Prowlarr and autobrr
// (version).
type systemResource struct {
	AppName                string  `json:"appName"`
	InstanceName           string  `json:"instanceName"`
	Version                string  `json:"version"`
	BuildTime              arrTime `json:"buildTime"`
	IsDebug                bool    `json:"isDebug"`
	IsProduction           bool    `json:"isProduction"`
	IsAdmin                bool    `json:"isAdmin"`
	IsUserInteractive      bool    `json:"isUserInteractive"`
	StartupPath            string  `json:"startupPath"`
	AppData                string  `json:"appData"`
	OsName                 string  `json:"osName"`
	OsVersion              string  `json:"osVersion"`
	IsNetCore              bool    `json:"isNetCore"`
	IsLinux                bool    `json:"isLinux"`
	IsOsx                  bool    `json:"isOsx"`
	IsWindows              bool    `json:"isWindows"`
	IsDocker               bool    `json:"isDocker"`
	IsContainerized        *bool   `json:"isContainerized,omitempty"`
	Mode                   string  `json:"mode"`
	Branch                 string  `json:"branch"`
	DatabaseType           string  `json:"databaseType"`
	DatabaseVersion        string  `json:"databaseVersion"`
	Authentication         string  `json:"authentication"`
	MigrationVersion       int     `json:"migrationVersion"`
	URLBase                string  `json:"urlBase"`
	RuntimeVersion         string  `json:"runtimeVersion"`
	RuntimeName            string  `json:"runtimeName"`
	StartTime              arrTime `json:"startTime"`
	PackageUpdateMechanism string  `json:"packageUpdateMechanism"`
}

// GET system/status.
func (s *Service) systemStatus(w http.ResponseWriter, r *request) {
	app, branch, mig := "Radarr", "master", 242
	if r.v.Flavour == Sonarr {
		app, branch, mig = "Sonarr", "main", 217
	}
	var schema int64
	_ = s.st.Reader().QueryRowContext(r.Context(), `SELECT COALESCE(MAX(version_id), 0) FROM `+store.MigrationTable+
		` WHERE is_applied = 1`).Scan(&schema)
	sr := systemResource{AppName: app, InstanceName: "theatarr " + r.v.Key, Version: r.vers, BuildTime: at(s.started),
		IsProduction: true, IsUserInteractive: false, StartupPath: "/app", AppData: "/data", OsName: "linux",
		IsNetCore: true, IsLinux: true, IsDocker: true, Mode: "console", Branch: branch, DatabaseType: "sqLite",
		DatabaseVersion: "theatarr-" + strconv.FormatInt(schema, 10), Authentication: "external", MigrationVersion: mig,
		URLBase: r.v.Base, RuntimeVersion: "go", RuntimeName: "netcore", StartTime: at(s.started),
		PackageUpdateMechanism: "docker"}
	if r.v.Flavour == Radarr {
		f := false
		sr.IsContainerized = &f
	}
	writeJSON(w, http.StatusOK, sr)
}

// qualityProfileResource is QualityProfileResource (src/Radarr.Api.V3/Profiles/Quality/QualityProfileResource.cs).
// A view's profiles select version sets; the first version supplies the runtime quality ladder and scores.
// Read by Seerr (id, name: service.ts L53-133) and Radarr/Sonarr import lists (id).
type qualityProfileResource struct {
	ID                    int64         `json:"id,omitempty"`
	Name                  string        `json:"name"`
	UpgradeAllowed        bool          `json:"upgradeAllowed"`
	Cutoff                int           `json:"cutoff"`
	Items                 []profileItem `json:"items"`
	MinFormatScore        int           `json:"minFormatScore"`
	CutoffFormatScore     int           `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int           `json:"minUpgradeFormatScore"`
	FormatItems           []any         `json:"formatItems"`
	Language              *language     `json:"language,omitempty"`
}

type profileItem struct {
	Quality quality       `json:"quality"`
	Items   []profileItem `json:"items"`
	Allowed bool          `json:"allowed"`
}

func (s *Service) profileResource(v *View, p *Profile) any {
	if built := s.runtimeProfile(v, p.Versions); built != nil {
		j := built.Profile.JSON()
		j.ID, j.Name = int(p.ID), p.Name
		_, ids := s.runtimeFormats(v)
		for i := range j.RawFormatItems {
			j.RawFormatItems[i].Format = ids[built][j.RawFormatItems[i].Format]
		}
		return j
	}
	fl := format.Radarr
	if v.Flavour == Sonarr {
		fl = format.Sonarr
	}
	qr := qualityProfileResource{ID: p.ID, Name: p.Name, UpgradeAllowed: true, Items: []profileItem{},
		MinUpgradeFormatScore: 1, FormatItems: []any{}}
	for _, q := range profile.Qualities(fl) {
		qr.Items = append(qr.Items, profileItem{Quality: qualityFor(v.Flavour, q.Name), Items: []profileItem{},
			Allowed: q.ID != 0})
		qr.Cutoff = q.ID
	}
	if v.Flavour == Radarr {
		qr.Language = &language{ID: -1, Name: "Any"}
	}
	return qr
}

// GET qualityprofile.
func (s *Service) qualityProfiles(w http.ResponseWriter, r *request) {
	out := make([]any, 0, len(r.v.Profiles))
	profiles := append([]*Profile(nil), r.v.Profiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	for _, p := range profiles {
		out = append(out, s.profileResource(r.v, p))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET qualityprofile/{id}.
func (s *Service) qualityProfile(w http.ResponseWriter, r *request) {
	id, _ := r.pathID(1)
	if p := r.v.profileByID(id); p != nil {
		writeJSON(w, http.StatusOK, s.profileResource(r.v, p))
		return
	}
	notFound(w, "QualityProfile with ID "+strconv.FormatInt(id, 10)+" does not exist")
}

// roots are the view's root folders: the libraries of its versions, in view order, id = position + 1.
func (s *Service) roots(v *View) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range v.Versions {
		if vp := s.pol.Versions[k]; vp != nil && vp.Library != "" && !seen[vp.Library] {
			seen[vp.Library] = true
			out = append(out, vp.Library)
		}
	}
	return out
}

// rootProbeTimeout bounds the stat and statfs of one root for rootfolder and diskspace; rootStat and rootDiskFree are
// those calls (tests replace them). A root on a network share that stops answering is reported inaccessible
// (rootfolder) or left out (diskspace) instead of holding the request, and while its call is stuck, later requests
// skip it without another call (rootprobe, shared with the settings root checks).
var (
	rootProbeTimeout = 3 * time.Second
	rootStat         = os.Stat
	rootDiskFree     = diskFree
)

// probeRoot stats root and its filesystem through the root prober.
func probeRoot(ctx context.Context, root string) (dir bool, free, total int64, ok bool) {
	type answer struct {
		dir, ok     bool
		free, total int64
	}
	var got answer
	err := rootprobe.Default.Do(ctx, []string{root}, rootProbeTimeout, func() error {
		var a answer
		if st, err := rootStat(root); err == nil && st.IsDir() {
			a.dir = true
			a.free, a.total, a.ok = rootDiskFree(root)
		}
		got = a
		return nil
	})
	if err != nil { // gave up or refused: a call left behind may still write got
		return false, 0, 0, false
	}
	return got.dir, got.free, got.total, got.ok
}

// GET rootfolder. Read by Seerr (id, path, freeSpace) and the import lists (path).
func (s *Service) rootFolders(w http.ResponseWriter, r *request) {
	out := []rootFolderResource{}
	for i, p := range s.roots(r.v) {
		rf := rootFolderResource{ID: int64(i + 1), Path: p, UnmappedFolders: []unmappedFolder{}}
		if dir, free, _, ok := probeRoot(r.Context(), p); dir {
			rf.Accessible = true
			if ok {
				rf.FreeSpace = &free
			}
		}
		out = append(out, rf)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET diskspace: one entry per root folder that exists (the arrs list mounts; a root's filesystem is what Homepage
// and the phone apps want, and statfs follows the symlinks in it to nothing, so the debrid mount is not counted).
func (s *Service) diskSpace(w http.ResponseWriter, r *request) {
	out := []diskSpaceResource{}
	for _, p := range s.roots(r.v) {
		if _, free, total, ok := probeRoot(r.Context(), p); ok {
			out = append(out, diskSpaceResource{Path: p, FreeSpace: free, TotalSpace: total})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Tags.

var tagLabel = regexp.MustCompile(`(?i)^[a-z0-9-]+$`)

func (s *Service) allTags(r *request) ([]tagResource, error) {
	rows, err := s.st.Reader().QueryContext(r.Context(), `SELECT id, label FROM arrview_tag ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := []tagResource{}
	for rows.Next() {
		var t tagResource
		if err := rows.Scan(&t.ID, &t.Label); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	return out, closeRows(rows)
}

// GET tag.
func (s *Service) tags(w http.ResponseWriter, r *request) {
	ts, err := s.allTags(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// GET tag/{id}.
func (s *Service) tag(w http.ResponseWriter, r *request) {
	id, _ := r.pathID(1)
	var t tagResource
	err := s.st.Reader().QueryRowContext(r.Context(), `SELECT id, label FROM arrview_tag WHERE id = ?`, id).
		Scan(&t.ID, &t.Label)
	if err == sql.ErrNoRows {
		notFound(w, "Tag with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// POST tag (TagController.Create, TagService.Add): an existing label (any case) returns the existing tag; a new one
// is stored lower-case. 201 with the tag. Labels must match ^[a-z0-9-]+$ (TagController's validator).
func (s *Service) createTag(w http.ResponseWriter, r *request) {
	var in tagResource
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if in.Label == "" || !tagLabel.MatchString(in.Label) {
		badRequest(w, "Label", "Allowed characters a-z, 0-9 and -", in.Label)
		return
	}
	var t tagResource
	err := s.st.WriteTx(r.Context(), func(tx *sqlTx) error {
		err := tx.QueryRowContext(r.Context(), `SELECT id, label FROM arrview_tag WHERE label = ?`, in.Label).
			Scan(&t.ID, &t.Label)
		if err != sql.ErrNoRows {
			return err
		}
		t.Label = strings.ToLower(in.Label)
		res, err := tx.ExecContext(r.Context(), `INSERT INTO arrview_tag (label, created_at) VALUES (?, ?)`, t.Label,
			store.Millis(s.now()))
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// PUT tag/{id} (TagController.Update): rename. Seerr's tag migration uses it (0007_migrate_arr_tags.ts). 202.
func (s *Service) updateTag(w http.ResponseWriter, r *request) {
	var in tagResource
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if id, ok := r.pathID(1); ok {
		in.ID = id
	}
	if in.Label == "" || !tagLabel.MatchString(in.Label) {
		badRequest(w, "Label", "Allowed characters a-z, 0-9 and -", in.Label)
		return
	}
	res, err := s.st.Writer().ExecContext(r.Context(), `UPDATE arrview_tag SET label = ? WHERE id = ?`, in.Label, in.ID)
	if store.IsUniqueViolation(err) {
		badRequest(w, "Label", "A tag with this label already exists", in.Label)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "Tag with ID "+strconv.FormatInt(in.ID, 10)+" does not exist")
		return
	}
	writeJSON(w, http.StatusAccepted, in)
}

// DELETE tag/{id} (TagController.DeleteTag, TagService.Delete): a tag still in use is refused with 409 and the arrs'
// message; a free one is deleted, 200 with no body. Tags are global here, so "in use" spans every view: a title that
// any view shows with the tag, or a notification of any view that carries it (TagDetails.InUse; Theatarr has no delay or
// release profiles, import lists, indexer or download client tags, nor auto tags). An assignment on a title that no
// view shows any more (all its versions removed through DELETE) does not hold the tag: it is invisible in every
// tag/detail, as the arr's tags went with its deleted movie, and it is deleted with the tag.
func (s *Service) deleteTag(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	id, _ := r.pathID(1)
	var label string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT label FROM arrview_tag WHERE id = ?`, id).Scan(&label)
	if err == sql.ErrNoRows {
		notFound(w, "Tag with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Checked and deleted under the one writer, so no assignment can be made between the check and the delete. The
	// views' titles are read through the reader pool, which sees every committed write.
	inUse := false
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		inUse = false
		for _, v := range s.views {
			es, err := s.load(ctx, v, nil)
			if err != nil {
				return err
			}
			for _, e := range es {
				inUse = inUse || slices.Contains(e.tags, id)
			}
		}
		if inUse {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT resource FROM arrview_notification`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var n struct {
				Tags []int64 `json:"tags"`
			}
			if json.Unmarshal([]byte(raw), &n) == nil && slices.Contains(n.Tags, id) {
				inUse = true
			}
		}
		if err := closeRows(rows); err != nil || inUse {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM arrview_tag WHERE id = ?`, id)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if inUse {
		// ModelConflictException(typeof(Tag), id, message): "{0} with ID {1} {2}", mapped to 409.
		writeJSON(w, http.StatusConflict, errorBody{Message: fmt.Sprintf(
			"Tag with ID %d '%s' cannot be deleted since it's still in use", id, label)})
		return
	}
	s.log.Info("tag deleted", "view", r.v.Key, "tag", label)
	w.WriteHeader(http.StatusOK)
}

// The queue handlers are in queue.go.

// GET wanted/missing: Radarr pages monitored movies without a file (MissingController.GetMissingMovies: sort
// movieMetadata.sortTitle ascending); Sonarr pages monitored, aired episodes without a file of monitored series
// (sort episodes.airDateUtc ascending). Homepage's Sonarr widget reads totalRecords.
func (s *Service) wantedMissing(w http.ResponseWriter, r *request) {
	s.wantedPage(w, r, false)
}

// GET health (HealthResource; type is HealthCheckResult): one notice while the view's versions are in shadow mode
// (ShadowModeCheck), and one warning while router.enabled is off and some of them are active (RouterDisabledCheck:
// their grabs and pushes are decided and planned as dry runs, never added); then the service's own checks that are
// not ok, worst first (internal/health: mount, metadata, jobs, backups, providers, indexers, VPN egress, disk), as the
// arrs list their failing checks.
func (s *Service) getHealth(w http.ResponseWriter, r *request) {
	out := []healthResource{}
	for _, h := range s.healthOf(r.Context(), r.v.Versions) {
		out = append(out, healthResource{Source: h.Source, Type: h.Type, Message: h.Message})
	}
	writeJSON(w, http.StatusOK, out)
}

// healthOf is the health of a set of versions, as GET health and the Health webhook report it.
func (s *Service) healthOf(ctx context.Context, keys []string) []notify.HealthItem {
	out := s.versionHealth(keys)
	if s.health != nil {
		for _, h := range s.health.Issues(ctx) {
			out = append(out, notify.HealthItem{Source: h.Source, Type: h.Type, Message: h.Message})
		}
	}
	return out
}

// versionHealth is the health of the versions themselves: shadow mode and the router switch.
func (s *Service) versionHealth(keys []string) []notify.HealthItem {
	out := []notify.HealthItem{}
	if sh := s.shadowVersions(keys); len(sh) > 0 {
		out = append(out, notify.HealthItem{Source: "ShadowModeCheck", Type: "notice",
			Message: "Versions in shadow mode acquire nothing: " + strings.Join(sh, ", ")})
	}
	if s.pol.Router == nil || !s.pol.Router.Enabled {
		var act []string
		for _, k := range keys {
			if vp := s.pol.Versions[k]; vp != nil && vp.Mode == policy.ModeActive {
				act = append(act, k)
			}
		}
		if len(act) > 0 {
			out = append(out, notify.HealthItem{Source: "RouterDisabledCheck", Type: "warning",
				Message: "Router disabled: grabs are dry runs: " + strings.Join(act, ", ")})
		}
	} else {
		var off []string
		for _, k := range keys {
			if vp := s.pol.Versions[k]; vp != nil && vp.Mode == policy.ModeActive && !vp.Grab {
				off = append(off, k)
			}
		}
		if len(off) > 0 {
			out = append(out, notify.HealthItem{Source: "RouterDisabledCheck", Type: "notice",
				Message: "Grab off: grabs are dry runs: " + strings.Join(off, ", ")})
		}
	}
	return out
}

func (s *Service) shadowVersions(keys []string) []string {
	var out []string
	for _, k := range keys {
		if vp := s.pol.Versions[k]; vp != nil && vp.Mode != policy.ModeActive {
			out = append(out, k)
		}
	}
	return out
}

// downloadClientConfigResource is DownloadClientConfigResource; ConfigController sets id 1. decypharr reads
// enableCompletedDownloadHandling and the auto-redownload flags (pkg/arr/media.go L11-59).
type downloadClientConfigResource struct {
	ID                                        int64  `json:"id"`
	DownloadClientWorkingFolders              string `json:"downloadClientWorkingFolders"`
	EnableCompletedDownloadHandling           bool   `json:"enableCompletedDownloadHandling"`
	CheckForFinishedDownloadInterval          *int   `json:"checkForFinishedDownloadInterval,omitempty"`
	AutoRedownloadFailed                      bool   `json:"autoRedownloadFailed"`
	AutoRedownloadFailedFromInteractiveSearch bool   `json:"autoRedownloadFailedFromInteractiveSearch"`
}

func (s *Service) downloadClientConfig(w http.ResponseWriter, r *request) {
	c := downloadClientConfigResource{ID: 1, DownloadClientWorkingFolders: "_UNPACK_|_FAILED_",
		EnableCompletedDownloadHandling: true, AutoRedownloadFailed: true, AutoRedownloadFailedFromInteractiveSearch: true}
	if r.v.Flavour == Radarr {
		one := 1
		c.CheckForFinishedDownloadInterval = &one
	}
	writeJSON(w, http.StatusOK, c)
}

// historyResource is HistoryResource (src/Radarr.Api.V3/History/HistoryResource.cs; Sonarr's adds episodeId and
// seriesId). Records come from the imported arr history of the view's versions; the id is the row's own.
type historyResource struct {
	Movie               *movieResource    `json:"movie,omitempty"`
	Series              *seriesResource   `json:"series,omitempty"`
	Episode             *episodeResource  `json:"episode,omitempty"`
	ID                  int64             `json:"id,omitempty"`
	MovieID             *int64            `json:"movieId,omitempty"`
	SeriesID            *int64            `json:"seriesId,omitempty"`
	EpisodeID           *int64            `json:"episodeId,omitempty"`
	SourceTitle         string            `json:"sourceTitle,omitempty"`
	Languages           []language        `json:"languages"`
	Quality             qualityModel      `json:"quality"`
	CustomFormats       []any             `json:"customFormats"`
	CustomFormatScore   int               `json:"customFormatScore"`
	QualityCutoffNotMet bool              `json:"qualityCutoffNotMet"`
	Date                arrTime           `json:"date"`
	DownloadID          string            `json:"downloadId,omitempty"`
	EventType           string            `json:"eventType"`
	Data                map[string]string `json:"data"`
}

func (s *Service) arrHistoryRows(r *request, ids []int64) ([]historyResource, error) {
	where, args := scope(r.v, nil)
	q := `SELECT h.rowid, h.title_id, COALESCE((SELECT a.target_id FROM arr_alias a WHERE a.instance=h.instance AND a.kind='episode' AND a.arr_id=h.arr_episode_id), h.arr_episode_id), h.event_type, h.date_at, COALESCE(h.download_id, ''),
		h.source_title, h.quality, COALESCE(h.custom_format_score, 0), h.languages, h.data
		FROM arr_history h JOIN version v ON v.id = h.version_id JOIN title t ON t.id = v.title_id WHERE ` + where
	q += ` AND h.rowid IN (SELECT value FROM json_each(?))`
	args = append(args, jsonIDs(ids))
	rows, err := s.st.Reader().QueryContext(r.Context(), q, args...)
	if err != nil {
		return nil, err
	}
	out := []historyResource{}
	for rows.Next() {
		var h historyResource
		var tid, date int64
		var ep sql.NullInt64
		var quality, langs, data string
		if err := rows.Scan(&h.ID, &tid, &ep, &h.EventType, &date, &h.DownloadID, &h.SourceTitle, &quality,
			&h.CustomFormatScore, &langs, &data); err != nil {
			rows.Close()
			return nil, err
		}
		id := viewID(tid)
		if r.v.Flavour == Sonarr {
			if ep.Valid {
				eid := ep.Int64
				h.EpisodeID = &eid
			}
			h.SeriesID = &id
		} else {
			h.MovieID = &id
		}
		var names []string
		_ = json.Unmarshal([]byte(langs), &names)
		h.Languages = languagesFrom(r.v.Flavour, names)
		h.Quality = qualityModelFor(r.v.Flavour, quality, 1)
		h.CustomFormats = []any{}
		h.Date = at(millis(date))
		h.Data = map[string]string{}
		var raw map[string]any
		if json.Unmarshal([]byte(data), &raw) == nil {
			for k, v := range raw {
				switch x := v.(type) {
				case string:
					h.Data[k] = x
				case nil:
				default:
					b, _ := json.Marshal(x)
					h.Data[k] = string(b)
				}
			}
		}
		out = append(out, h)
	}
	return out, closeRows(rows)
}

// historyEventName maps the numeric eventType filter to the stored name (Radarr MovieHistoryEventType, Sonarr
// EpisodeHistoryEventType).
func historyEventName(flavour, v string) string {
	n, err := strconv.Atoi(v)
	if err != nil {
		return v
	}
	radarr := map[int]string{0: "unknown", 1: "grabbed", 3: "downloadFolderImported", 4: "downloadFailed", 6: "movieFileDeleted",
		7: "movieFolderImported", 8: "movieFileRenamed", 9: "downloadIgnored"}
	sonarr := map[int]string{0: "unknown", 1: "grabbed", 2: "seriesFolderImported", 3: "downloadFolderImported",
		4: "downloadFailed", 5: "episodeFileDeleted", 6: "episodeFileRenamed", 7: "downloadIgnored"}
	if flavour == Sonarr {
		return sonarr[n]
	}
	return radarr[n]
}

// GET history: paged, date descending (HistoryController.GetHistory).
func (s *Service) history(w http.ResponseWriter, r *request) {
	p := r.paging("date", "descending")
	hs, err := s.historyPage(r, 0, &p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	p.Records = hs
	writeJSON(w, http.StatusOK, p)
}

// GET history/movie and history/series return plain lists, as do the arrs.
func (s *Service) titleHistory(w http.ResponseWriter, r *request) {
	key := "movieId"
	if r.v.Flavour == Sonarr {
		key = "seriesId"
	}
	tid, err := s.resolve(r.Context(), r.v, int64(r.queryInt(key, 0)))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if tid == 0 {
		writeJSON(w, http.StatusOK, []historyResource{})
		return
	}
	hs, err := s.historyPage(r, tid, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hs)
}

func (s *Service) historySince(w http.ResponseWriter, r *request) {
	date, _ := r.query("date")
	if _, ok := parseDate(date); !ok {
		badRequest(w, "date", "A valid date is required", date)
		return
	}
	hs, err := s.historyPage(r, 0, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hs)
}
