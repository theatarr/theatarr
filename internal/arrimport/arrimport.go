// Package arrimport is the one-time import from a Radarr or Sonarr instance (docs/migration.md "Import from
// Sonarr/Radarr"): it reads the instance once with GETs only, adds its titles to one Theatarr library with the
// instance's monitoring, and links the instance's files that are symlinks into the debrid mount. The run is held in
// memory by its caller; nothing here stores the API key.
package arrimport

import (
	"context"
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
	ImportNeedsEpisodes(ctx context.Context, titleID int64, tvdbConfigured bool) (bool, error)
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
// gate (a real run) and its folders against the folders the request maps. It returns the gate's plain refusal (""
// when the library can write links) for a Preview to report.
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
		return writeBlock, &Refusal{Msg: "Import needs the library to write links: " + writeBlock + ". Set it to Live " +
			"and turn Write library links on; Allow new downloads can stay off."}
	}
	var folders []string
	for from, to := range req.Paths {
		folders = append(folders, from, to)
	}
	if err := checkFolders(d, req.Library, folders); err != nil {
		return writeBlock, err
	}
	return writeBlock, nil
}

// gateBlock is the gate's refusal of library key in plain words, "" when it may write.
func gateBlock(d Deps, key string) string {
	if d.Library == nil || d.Library.Gate() == nil {
		return "Write library links is off"
	}
	err := d.Library.Gate().CheckVersion(key)
	var r *library.Refused
	switch {
	case err == nil:
		return ""
	case errors.As(err, &r) && r.Reason == "disabled":
		return "Write library links is off"
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
					"library's folder must be separate from the instance's folders.", r, f)}
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

// mapPath rewrites p by the longest root folder that holds it and has a mapping in paths.
func mapPath(p string, roots []string, paths map[string]string) string {
	best := ""
	for _, r := range roots {
		if under(p, r) && len(path.Clean(r)) > len(best) {
			best = path.Clean(r)
		}
	}
	if best == "" {
		return p
	}
	to, ok := lookupPath(paths, best)
	if !ok || to == "" {
		return p
	}
	return path.Join(to, strings.TrimPrefix(path.Clean(p), best))
}

func lookupPath(paths map[string]string, root string) (string, bool) {
	for from, to := range paths {
		if path.Clean(from) == root {
			return to, true
		}
	}
	return "", false
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
	var folders []string
	for _, r := range snap.RootFolders {
		folders = append(folders, mapPath(r, snap.RootFolders, req.Paths))
	}
	if err := checkFolders(d, req.Library, folders); err != nil {
		return fail(err)
	}

	r := &runner{d: d, req: req, snap: snap, rep: rep, profiles: map[int]bool{}, source: sourceLabel(req)}
	for _, id := range req.Profiles {
		r.profiles[id] = true
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
		if rep.MissingMonitored, err = missingMonitored(ctx, d, req.Library, added); err != nil {
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
		removed, err := r.removed(ctx, titleID)
		if err != nil {
			return 0, err
		}
		if removed {
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
	if !added {
		return 0, r.link(ctx, t, titleID, at, false, files)
	}
	err = r.link(ctx, t, titleID, at, true, files)
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

func (r *runner) removed(ctx context.Context, titleID int64) (bool, error) {
	var removed bool
	err := r.d.Store.Reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM arrview_removed_version rv JOIN
		version v ON v.id = rv.version_id WHERE v.title_id = ? AND v.policy_key = ?)`, titleID, r.req.Library).Scan(&removed)
	return removed, err
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
		p := mapPath(f.Path, r.snap.RootFolders, r.req.Paths)
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

// link brings a series' episode list and monitoring over, then links the title's files.
func (r *runner) link(ctx context.Context, t *importarr.Title, titleID int64, at catalog.ArrImportTitle, added bool,
	files []*importarr.File) error {
	rep := r.rep
	var in []library.ImportFile
	if r.req.Flavour == "sonarr" {
		ok, err := r.episodes(ctx, t, titleID, at, added, len(files))
		if err != nil || !ok {
			return err
		}
		for _, f := range files {
			ids, err := r.episodeIDs(ctx, t, titleID, f)
			if err != nil {
				return err
			}
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
		rep.HasFile += ar.HasFile
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
	before, err := r.seriesSource(ctx, titleID)
	if err != nil {
		return false, err
	}
	needs, err := r.d.Titles.ImportNeedsEpisodes(ctx, titleID, r.d.TVDB)
	if err != nil {
		return false, err
	}
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
		}
	}
	after, err := r.seriesSource(ctx, titleID)
	if err != nil {
		return false, err
	}
	applied := after == "tvdb"
	if added || needs && before != "tvdb" && after == "tvdb" {
		eps := make([]catalog.EpisodeMonitor, 0, len(t.Episodes))
		for _, e := range t.Episodes {
			eps = append(eps, catalog.EpisodeMonitor{Season: e.Season, Number: e.Number, Monitored: e.Monitored})
		}
		if applied, err = r.d.Titles.ImportEpisodeMonitoring(ctx, titleID, r.req.Library, at, eps); err != nil {
			return false, err
		}
	}
	if !applied {
		rep.notLinked(ReasonNumbering, files)
		return false, nil
	}
	return true, nil
}

func (r *runner) seriesSource(ctx context.Context, titleID int64) (string, error) {
	var source string
	err := r.d.Store.Reader().QueryRowContext(ctx, `SELECT COALESCE((SELECT source FROM series_meta WHERE title_id = ?),
		'')`, titleID).Scan(&source)
	return source, err
}

// episodeIDs maps a Sonarr file's episodes to Theatarr's by season and number; nil when any has no match.
func (r *runner) episodeIDs(ctx context.Context, t *importarr.Title, titleID int64, f *importarr.File) ([]int64, error) {
	if len(f.EpisodeArrIDs) == 0 {
		return nil, nil
	}
	byArr := make(map[int]importarr.Episode, len(t.Episodes))
	for _, e := range t.Episodes {
		byArr[e.ArrID] = e
	}
	var ids []int64
	for _, arrID := range f.EpisodeArrIDs {
		e, ok := byArr[arrID]
		if !ok {
			return nil, nil
		}
		var id int64
		err := r.d.Store.Reader().QueryRowContext(ctx, `SELECT COALESCE((SELECT id FROM episode WHERE title_id = ? AND
			season = ? AND number = ? AND removed_at IS NULL ORDER BY id LIMIT 1), 0)`, titleID, e.Season, e.Number).Scan(&id)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			return nil, nil
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// importFile is the library's record of one instance file.
func (r *runner) importFile(titleID int64, f *importarr.File, episodes []int64) library.ImportFile {
	in := library.ImportFile{TitleID: titleID, Source: r.source + " " + f.Path,
		Path: mapPath(f.Path, r.snap.RootFolders, r.req.Paths), RelativePath: f.RelativePath, Quality: f.Quality,
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
func missingMonitored(ctx context.Context, d Deps, key string, titles []int64) (int, error) {
	n := 0
	now := store.Millis(d.Now())
	for _, id := range titles {
		var missing bool
		err := d.Store.Reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM version v WHERE v.title_id = ? AND
			v.policy_key = ? AND v.monitored = 1 AND v.state = 'wanted' AND ((SELECT kind FROM title WHERE id = v.title_id) <> 'series' OR EXISTS(SELECT 1
			FROM episode_version_state es JOIN episode e ON e.id = es.episode_id WHERE es.version_id = v.id AND
			es.monitored = 1 AND es.state = 'wanted' AND e.removed_at IS NULL AND e.air_date_utc_at IS NOT NULL
			AND e.air_date_utc_at <= ?)))`, id, key, now).Scan(&missing)
		if err != nil {
			return 0, err
		}
		if missing {
			n++
		}
	}
	return n, nil
}
