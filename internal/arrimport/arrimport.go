// Package arrimport is the one-time import from a Radarr or Sonarr instance (docs/migration.md "Import from
// Sonarr/Radarr"): it reads the instance once with GETs only, adds its titles to one Theatarr library with the
// instance's monitoring, and links the instance's files that are symlinks into the debrid mount. The run is held in
// memory by its caller; nothing here stores the API key.
package arrimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Request is one Preview (DryRun) or Import.
type Request struct {
	Flavour string `json:"flavour"` // radarr or sonarr
	URL     string `json:"url"`
	// APIKey is used to read the instance and dropped after; it is never stored or answered.
	APIKey  string `json:"api_key,omitempty"`
	Library string `json:"library"` // the library's version key
	// Profiles are the instance's quality profile ids whose titles come in; at least one.
	Profiles []int `json:"profiles"`
	// Paths maps an instance root folder to where Theatarr reads it; a folder not listed is read at its own path.
	Paths  map[string]string `json:"paths,omitempty"`
	DryRun bool              `json:"dry_run"`
}

// Progress is where a run is: phase reading (the instance) or importing (titles), done of total.
type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// Titles is the catalogue side of the import (catalog.MovieIntake).
type Titles interface {
	ImportArrTitle(ctx context.Context, key string, t catalog.ArrImportTitle, dryRun bool) (string, int64, error)
	ImportEpisodeMonitoring(ctx context.Context, titleID int64, key string, t catalog.ArrImportTitle,
		eps []catalog.EpisodeMonitor) (bool, error)
	SetImportedMonitored(ctx context.Context, titleID int64, key string, monitored bool) error
}

// Series refreshes a series' episode list (tvrefresh.Service).
type Series interface {
	RefreshTitle(ctx context.Context, titleID int64, force bool) (*tvrefresh.Result, error)
}

// Library links the files (library.Library).
type Library interface {
	Gate() *library.Gate
	AdoptImported(ctx context.Context, version string, files []library.ImportFile) (*library.AdoptReport, error)
	CheckImportFile(ctx context.Context, p string) (reason, detail string, err error)
	DirExists(ctx context.Context, p string) (bool, error)
}

// Deps are the services of the settings generation a run starts under.
type Deps struct {
	Store  *store.Store
	Policy *policy.Policy
	// Arr opens the read-only client of the instance (arr.New with the default options in serve).
	Arr func(url, key string) (importarr.Getter, error)
	// Titles is nil without TMDb: the import then refuses.
	Titles Titles
	// Series is nil when series metadata is off.
	Series Series
	// TVDB says whether a TVDB key is set: without it episodes cannot be matched to Sonarr's numbering.
	TVDB    bool
	Library Library
	Log     *slog.Logger
	Now     func() time.Time
}

// InputError is a request the import cannot use (answered 400).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Refusal is an import the library or the instance's folders do not allow (answered 409).
type Refusal struct{ Msg string }

func (e *Refusal) Error() string { return e.Msg }

// Reasons a file is not linked, besides adopt's (library.Adopt*).
const (
	ReasonNumbering     = "numbering"
	ReasonNoEpisodeList = "no_episode_list"
	ReasonFailed        = "failed"
)

func kindOf(flavour string) policy.Kind {
	if flavour == "sonarr" {
		return policy.KindSeries
	}
	return policy.KindMovie
}

func appName(flavour string) string {
	if flavour == "sonarr" {
		return "Sonarr"
	}
	return "Radarr"
}

// Validate makes the checks that need no read of the instance: the request, the library, TMDb, the library's write
// gate (a real run) and its folders against the folders the request maps instance folders to. It returns the gate's
// plain refusal ("" when the library can write links) for a Preview to report.
func Validate(d Deps, req Request) (writeBlock string, err error) {
	if req.Flavour != "radarr" && req.Flavour != "sonarr" {
		return "", &InputError{Msg: "Choose Radarr or Sonarr."}
	}
	if strings.TrimSpace(req.URL) == "" || req.APIKey == "" {
		return "", &InputError{Msg: "Enter the instance's address and API key."}
	}
	if len(req.Profiles) == 0 {
		return "", &InputError{Msg: "Tick at least one quality profile."}
	}
	vp := d.Policy.Versions[req.Library]
	if vp == nil {
		return "", &InputError{Msg: fmt.Sprintf("There is no library %q.", req.Library)}
	}
	if vp.Kind != kindOf(req.Flavour) {
		return "", &InputError{Msg: fmt.Sprintf("%s titles go into a %s library; %s is a %s library.",
			appName(req.Flavour), kindOf(req.Flavour), req.Library, vp.Kind)}
	}
	if d.Titles == nil {
		return "", &Refusal{Msg: "Set up TMDb first (Settings › Metadata & artwork): Theatarr needs it to add titles."}
	}
	writeBlock = gateBlock(d, req.Library)
	if writeBlock != "" && !req.DryRun {
		return writeBlock, &Refusal{Msg: "Import needs a Live library and Pause off: " + writeBlock + "."}
	}
	// Where Theatarr reads each mapped instance folder; an unmapped folder is checked at its own path after the read.
	var folders []string
	for from, to := range req.Paths {
		if to == "" {
			to = from
		}
		folders = append(folders, to)
	}
	if err := checkFolders(d, req.Library, folders); err != nil {
		return writeBlock, err
	}
	return writeBlock, nil
}

// gateBlock is the gate's refusal of library key in plain words, "" when it may write.
func gateBlock(d Deps, key string) string {
	if d.Library == nil || d.Library.Gate() == nil {
		return "Theatarr is paused"
	}
	err := d.Library.Gate().CheckVersion(key)
	var r *library.Refused
	switch {
	case err == nil:
		return ""
	case errors.As(err, &r) && r.Reason == "disabled":
		return "Theatarr is paused"
	case errors.As(err, &r) && r.Reason == "shadow":
		return "it is Watch only"
	case errors.As(err, &r) && r.Reason == "unlisted":
		return "it has no write folder"
	case errors.As(err, &r):
		return r.Detail
	}
	return err.Error()
}

// checkFolders refuses a library whose folder or write roots are equal to, inside or containing one of folders.
func checkFolders(d Deps, key string, folders []string) error {
	roots := []string{d.Policy.Versions[key].Library}
	if d.Library != nil && d.Library.Gate() != nil {
		roots = append(roots, d.Library.Gate().Roots(key)...)
	}
	for _, r := range roots {
		if r == "" {
			continue
		}
		for _, f := range folders {
			if f == "" {
				continue
			}
			if under(r, f) || under(f, r) {
				return &Refusal{Msg: fmt.Sprintf("The library's folder %s and the instance's folder %s overlap: the "+
					"library's folder must be separate from the instance's folders. If Theatarr sees the instance's folder at "+
					"another path, enter that path under Folders.", r, f)}
			}
		}
	}
	return nil
}

// under reports whether p is dir or inside it.
func under(p, dir string) bool {
	p, dir = path.Clean(p), path.Clean(dir)
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// folderMap rewrites an instance path to where Theatarr reads it: by the longest instance root folder that holds
// the path, when that folder is mapped.
type folderMap struct {
	roots []string          // cleaned
	to    map[string]string // cleaned root folder -> where Theatarr reads it
}

func newFolderMap(roots []string, paths map[string]string) folderMap {
	m := folderMap{to: map[string]string{}}
	for _, r := range roots {
		m.roots = append(m.roots, path.Clean(r))
	}
	for from, to := range paths {
		if to != "" {
			m.to[path.Clean(from)] = to
		}
	}
	return m
}

func (m folderMap) apply(p string) string {
	best := ""
	for _, r := range m.roots {
		if under(p, r) && len(r) > len(best) {
			best = r
		}
	}
	to, ok := m.to[best]
	if best == "" || !ok {
		return p
	}
	return path.Join(to, strings.TrimPrefix(path.Clean(p), best))
}

// Run reads the instance and imports (or, DryRun, previews) its titles into req.Library, one title at a time: add
// the copy unmonitored, for a series refresh its episode list when needed and bring Sonarr's monitoring over (TVDB
// numbering only), link the files, then give an added copy the instance's monitored flag. The report is returned
// also when the run stops early, with Error set.
func Run(ctx context.Context, d Deps, req Request, progress func(Progress)) (*Report, error) {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if progress == nil {
		progress = func(Progress) {}
	}
	writeBlock, err := Validate(d, req)
	if err != nil {
		return nil, err
	}
	rep := newReport(req)
	rep.Writable, rep.WriteBlock = writeBlock == "", writeBlock
	rep.TVDBMissing = req.Flavour == "sonarr" && !d.TVDB
	fail := func(err error) (*Report, error) {
		rep.Error = err.Error()
		return rep, err
	}

	g, err := d.Arr(req.URL, req.APIKey)
	if err != nil {
		return fail(&InputError{Msg: scrub(err.Error(), req.APIKey)})
	}
	key := req.APIKey
	req.APIKey = ""
	progress(Progress{Phase: "reading"})
	snap, err := importarr.ReadTitles(ctx, g, req.Flavour, func(done, total int) {
		progress(Progress{Phase: "reading", Done: done, Total: total})
	})
	g = nil
	if err != nil {
		return fail(errors.New(scrub(err.Error(), key)))
	}
	key = ""
	rep.AppVersion = snap.AppVersion
	rep.Titles = len(snap.Titles)
	folderMap := newFolderMap(snap.RootFolders, req.Paths)
	var folders []string
	for _, r := range snap.RootFolders {
		folders = append(folders, folderMap.apply(r))
	}
	if err := checkFolders(d, req.Library, folders); err != nil {
		return fail(err)
	}

	r := &runner{d: d, req: req, snap: snap, rep: rep, profiles: map[int]bool{}, source: sourceLabel(req),
		folders: folderMap}
	for _, id := range req.Profiles {
		r.profiles[id] = true
	}
	if r.removed, err = removedTitles(ctx, d, req.Library); err != nil {
		return fail(err)
	}
	var added []int64
	for i := range snap.Titles {
		progress(Progress{Phase: "importing", Done: i, Total: len(snap.Titles)})
		id, err := r.title(ctx, &snap.Titles[i])
		if id != 0 {
			added = append(added, id)
		}
		if err != nil {
			return fail(err)
		}
	}
	progress(Progress{Phase: "importing", Done: len(snap.Titles), Total: len(snap.Titles)})
	if !req.DryRun {
		if rep.MissingMonitored, err = missingMonitored(ctx, d, req.Library, req.Flavour == "sonarr", added); err != nil {
			return fail(err)
		}
	}
	d.Log.Info("arr import done", "flavour", req.Flavour, "library", req.Library, "dry_run", req.DryRun,
		"titles", rep.Titles, "added", rep.AddedTitle+rep.AddedLibrary, "linked", rep.Linked, "failed", rep.Failed)
	return rep, nil
}

// sourceLabel is the instance part of a link's provenance: flavour@host:port.
func sourceLabel(req Request) string {
	host := req.URL
	if u, err := url.Parse(req.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	return req.Flavour + "@" + host
}

func scrub(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, "***")
}

type runner struct {
	d        Deps
	req      Request
	snap     *importarr.Snapshot
	rep      *Report
	profiles map[int]bool
	source   string
	folders  folderMap
	// removed are the titles whose copy in the library was removed in Theatarr.
	removed map[int64]bool
}

// title imports one title. It returns the title id when this run added the library's copy, and an error only when
// the run must stop (a cancelled context, the mount or a library root down, a gate refusal); a title's own failure
// is counted and the run goes on.
func (r *runner) title(ctx context.Context, t *importarr.Title) (int64, error) {
	rep := r.rep
	if !r.profiles[t.QualityProfileID] {
		rep.Skipped.Profile++
		return 0, nil
	}
	series := r.req.Flavour == "sonarr"
	if !series && t.TmdbID <= 0 || series && t.TvdbID <= 0 {
		rep.Skipped.NoID++
		return 0, nil
	}
	at := importTitle(t, kindOf(r.req.Flavour))
	change, titleID, err := r.d.Titles.ImportArrTitle(ctx, r.req.Library, at, r.req.DryRun)
	if err != nil {
		return 0, r.failed(ctx, t, err)
	}
	switch change {
	case "excluded":
		rep.Skipped.Excluded++
		return 0, nil
	case "skipped":
		rep.Skipped.DoesNotFit++
		return 0, nil
	case "present":
		if r.removed[titleID] {
			rep.Removed++
			return 0, nil
		}
		rep.Present++
	case "add_title":
		rep.AddedTitle++
	case "add_library":
		rep.AddedLibrary++
	default:
		return 0, fmt.Errorf("arr import: unknown change %q", change)
	}
	files := t.Files
	if !series && t.File != nil {
		files = []*importarr.File{t.File}
	}
	rep.Files += len(files)
	if r.req.DryRun {
		return 0, r.preview(ctx, t, files)
	}
	added := change == "add_title" || change == "add_library"
	ok := true
	if series {
		ok, err = r.episodes(ctx, t, titleID, at, added, len(files))
	}
	if err == nil && ok {
		err = r.link(ctx, t, titleID, files)
	}
	if !added {
		return 0, err
	}
	// The copy was added unmonitored; it takes the instance's flag now that its monitoring and files are in, also
	// when linking failed, so it is never left unmonitored.
	if rerr := r.d.Titles.SetImportedMonitored(context.WithoutCancel(ctx), titleID, r.req.Library, at.Monitored); rerr != nil && err == nil {
		err = rerr
	}
	return titleID, err
}

// failed counts a title's own error, or returns err when the run must stop.
func (r *runner) failed(ctx context.Context, t *importarr.Title, err error) error {
	if ctx.Err() != nil || library.Unavailable(err) || errors.Is(err, library.ErrRefused) {
		return err
	}
	r.rep.Failed++
	r.rep.problem(t.Name, "", ReasonFailed, err.Error())
	r.d.Log.Warn("arr import: title failed", "title", t.Name, "err", err)
	return nil
}

// removedTitles are the titles whose copy in library key was removed in Theatarr.
func removedTitles(ctx context.Context, d Deps, key string) (map[int64]bool, error) {
	removed := map[int64]bool{}
	rows, err := d.Store.Reader().QueryContext(ctx, `SELECT DISTINCT v.title_id FROM arrview_removed_version rv JOIN
		version v ON v.id = rv.version_id WHERE v.policy_key = ?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		removed[id] = true
	}
	return removed, rows.Err()
}

// importTitle is the catalogue's view of an instance title.
func importTitle(t *importarr.Title, kind policy.Kind) catalog.ArrImportTitle {
	at := catalog.ArrImportTitle{Kind: kind, TmdbID: int64(t.TmdbID), TvdbID: int64(t.TvdbID), ImdbID: t.ImdbID,
		Name: t.Name, Year: t.Year, SeriesType: t.SeriesType, Monitored: t.Monitored,
		MinimumAvailability: t.MinimumAvailability}
	if t.MonitorNewItems == catalog.MonitorAll || t.MonitorNewItems == catalog.MonitorNone {
		at.MonitorNew = t.MonitorNewItems
	}
	for n, m := range t.SeasonsMonitored {
		at.Seasons = append(at.Seasons, catalog.Season{Number: n, Monitored: m})
	}
	sort.Slice(at.Seasons, func(i, j int) bool { return at.Seasons[i].Number < at.Seasons[j].Number })
	return at
}

// preview counts what a real run could link, without writing.
func (r *runner) preview(ctx context.Context, t *importarr.Title, files []*importarr.File) error {
	rep := r.rep
	if rep.TVDBMissing {
		rep.notLinked(ReasonNumbering, len(files))
		return nil
	}
	if r.d.Library == nil {
		return nil // no library service: the Preview already says the library cannot write links
	}
	for _, f := range files {
		p := r.folders.apply(f.Path)
		reason, detail, err := r.d.Library.CheckImportFile(ctx, p)
		if err != nil {
			return err
		}
		if reason == "" {
			rep.Linkable++
			continue
		}
		rep.notLinked(reason, 1)
		rep.problem(t.Name, f.Path, reason, detail)
	}
	return nil
}

// link links the title's files; a series' episode list and monitoring are in place (episodes).
func (r *runner) link(ctx context.Context, t *importarr.Title, titleID int64, files []*importarr.File) error {
	if len(files) == 0 {
		return nil
	}
	rep := r.rep
	var in []library.ImportFile
	if r.req.Flavour == "sonarr" {
		eps, err := r.seriesEpisodes(ctx, t, titleID)
		if err != nil {
			return err
		}
		for _, f := range files {
			ids := eps.ids(f)
			if ids == nil {
				rep.notLinked(library.AdoptNoEpisodes, 1)
				rep.problem(t.Name, f.Path, library.AdoptNoEpisodes, "")
				continue
			}
			in = append(in, r.importFile(titleID, f, ids))
		}
	} else {
		for _, f := range files {
			in = append(in, r.importFile(titleID, f, nil))
		}
	}
	if len(in) == 0 {
		return nil
	}
	ar, err := r.d.Library.AdoptImported(ctx, r.req.Library, in)
	if ar != nil {
		rep.Linked += ar.Linked
		rep.Existing += ar.Existing
		rep.FilesHasFile += ar.HasFile
		for reason, n := range ar.Skipped {
			rep.notLinked(reason, n)
		}
		for _, p := range ar.Problems {
			rep.problem(t.Name, p.Path, p.Reason, p.Detail)
		}
	}
	if err != nil {
		return r.failed(ctx, t, err)
	}
	return nil
}

// episodes makes sure the series has an episode list in Sonarr's (TVDB) numbering and brings Sonarr's monitoring
// over to a copy added in this run, or to a present copy whose list this run switched to TVDB. ok is false when the
// files cannot be matched (counted with the reason).
func (r *runner) episodes(ctx context.Context, t *importarr.Title, titleID int64, at catalog.ArrImportTitle,
	added bool, files int) (ok bool, err error) {
	rep := r.rep
	var before string
	var count int
	if err := r.d.Store.Reader().QueryRowContext(ctx, `SELECT COALESCE((SELECT source FROM series_meta WHERE
		title_id = ?), ''), (SELECT COUNT(*) FROM episode WHERE title_id = ? AND removed_at IS NULL)`, titleID,
		titleID).Scan(&before, &count); err != nil {
		return false, err
	}
	after := before
	needs := count == 0 || r.d.TVDB && before != "tvdb"
	if needs {
		if r.d.Series == nil {
			rep.notLinked(ReasonNoEpisodeList, files)
			return false, nil
		}
		res, err := r.d.Series.RefreshTitle(ctx, titleID, false)
		switch {
		case errors.Is(err, tvrefresh.ErrOff):
			rep.notLinked(ReasonNoEpisodeList, files)
			return false, nil
		case err != nil:
			return false, r.failed(ctx, t, err)
		case res != nil && res.NotFound:
			rep.notLinked(ReasonNoEpisodeList, files)
			rep.problem(t.Name, "", ReasonNoEpisodeList, res.Error)
			return false, nil
		case res != nil:
			after = res.Source
		}
	}
	if after != "tvdb" {
		rep.notLinked(ReasonNumbering, files)
		return false, nil
	}
	if added || before != "tvdb" {
		eps := make([]catalog.EpisodeMonitor, 0, len(t.Episodes))
		for _, e := range t.Episodes {
			eps = append(eps, catalog.EpisodeMonitor{Season: e.Season, Number: e.Number, Monitored: e.Monitored})
		}
		applied, err := r.d.Titles.ImportEpisodeMonitoring(ctx, titleID, r.req.Library, at, eps)
		if err != nil {
			return false, err
		}
		if !applied {
			rep.notLinked(ReasonNumbering, files)
			return false, nil
		}
	}
	return true, nil
}

// episodeIndex maps a Sonarr series' files to Theatarr's episodes by season and number.
type episodeIndex struct {
	byArr map[int]importarr.Episode
	bySE  map[[2]int]int64 // season, number -> episode id
}

// seriesEpisodes loads the series' episodes once for all its files.
func (r *runner) seriesEpisodes(ctx context.Context, t *importarr.Title, titleID int64) (*episodeIndex, error) {
	x := &episodeIndex{byArr: make(map[int]importarr.Episode, len(t.Episodes)), bySE: map[[2]int]int64{}}
	for _, e := range t.Episodes {
		x.byArr[e.ArrID] = e
	}
	rows, err := r.d.Store.Reader().QueryContext(ctx, `SELECT id, season, number FROM episode WHERE title_id = ? AND
		removed_at IS NULL ORDER BY id`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var season, number int
		if err := rows.Scan(&id, &season, &number); err != nil {
			return nil, err
		}
		if _, ok := x.bySE[[2]int{season, number}]; !ok {
			x.bySE[[2]int{season, number}] = id
		}
	}
	return x, rows.Err()
}

// ids are the Theatarr episodes of a Sonarr file; nil when any has no match.
func (x *episodeIndex) ids(f *importarr.File) []int64 {
	var ids []int64
	for _, arrID := range f.EpisodeArrIDs {
		e, ok := x.byArr[arrID]
		if !ok {
			return nil
		}
		id, ok := x.bySE[[2]int{e.Season, e.Number}]
		if !ok {
			return nil
		}
		ids = append(ids, id)
	}
	return ids
}

// importFile is the library's record of one instance file.
func (r *runner) importFile(titleID int64, f *importarr.File, episodes []int64) library.ImportFile {
	in := library.ImportFile{TitleID: titleID, Source: r.source + " " + f.Path,
		Path: r.folders.apply(f.Path), RelativePath: f.RelativePath, Quality: f.Quality,
		ReleaseGroup: f.ReleaseGroup, SceneName: f.SceneName, Edition: f.Edition, OriginalFilePath: f.OriginalFilePath,
		ReleaseType: f.ReleaseType, Languages: f.Languages, Revision: f.Revision, IndexerFlags: f.IndexerFlags,
		Size: f.Size, CutoffNotMet: f.CutoffNotMet, Episodes: episodes}
	if len(f.MediaInfo) > 0 {
		in.MediaInfo = string(f.MediaInfo)
	}
	return in
}

// missingMonitored counts the copies added in this run that are monitored and still wanted: a movie in state
// wanted, a series with an aired, monitored episode still wanted (a series with only future episodes monitored is
// wanted too but not missing).
func missingMonitored(ctx context.Context, d Deps, key string, series bool, titles []int64) (int, error) {
	if len(titles) == 0 {
		return 0, nil
	}
	ids, err := json.Marshal(titles)
	if err != nil {
		return 0, err
	}
	var n int
	err = d.Store.Reader().QueryRowContext(ctx, `SELECT COUNT(DISTINCT v.title_id) FROM version v WHERE v.title_id IN
		(SELECT value FROM json_each(?)) AND v.policy_key = ? AND v.monitored = 1 AND v.state = 'wanted' AND (? = 0 OR
		EXISTS(SELECT 1 FROM episode_version_state es JOIN episode e ON e.id = es.episode_id WHERE es.version_id = v.id
		AND es.monitored = 1 AND es.state = 'wanted' AND e.removed_at IS NULL AND e.air_date_utc_at IS NOT NULL AND
		e.air_date_utc_at <= ?))`, string(ids), key, series, store.Millis(d.Now())).Scan(&n)
	return n, err
}
