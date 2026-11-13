package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The library API behind the SPA's Home, grids and title page (docs/design/ui.md): GET titles grows sort,
// grid filters, facts and per-version files; titles/facets counts for the filter chips; titles/{id}/episodes; and
// discover (the Home rows). Every handler runs the "see files" field filter (perms.go).

func (w *Web) mountLibraryAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/titles/facets", SignedIn, auth.PermBrowse, w.apiTitleFacets)
	w.handle(mux, "GET "+b+"/titles/{id}/episodes", SignedIn, auth.PermBrowse, w.apiTitleEpisodes)
	w.handle(mux, "GET "+b+"/discover", SignedIn, auth.PermBrowse, w.apiDiscover)
	w.handle(mux, "GET "+b+"/discover/{row}", SignedIn, auth.PermBrowse, w.apiDiscoverRow)
}

// TitleGridFilter is the grid's filters and sort on top of TitleFilter.
type TitleGridFilter struct {
	Anime     *bool    // anime titles only (true) or none (false)
	Monitored *bool    // the title is monitored
	Genres    []string // TMDb genre names; any of them
	Quality   string   // some current file of the title has this quality (WEBDL-2160p)
	Provider  string   // some current file is held by this debrid provider: a cache code (rd) or a provider key (see files only)
	YearFrom  int
	YearTo    int
	// Availability is the poster badge (availabilityOf): missing, partial, upgrade, available or none.
	Availability string
	// Sort is added (newest current file), created (when the title was added), name, year, release (TMDb release or
	// first air date), rating, runtime, certification, network or size (current files); Order is asc or desc
	// (default: asc for name, certification, network and next, desc for the rest). Titles with no
	// value for the sort key come last either way. The library views' sorts (copies.go): completion (the share of a
	// series' aired episodes a copy holds; a movie copy's file), quality (a copy's best current quality), activity
	// (the newest change of a download in hand) and next (the next episode's air date, or a coming movie release).
	// SortVersion names the copy (version key) completion, quality and activity read; without it they take the
	// title's best copy (activity: any copy).
	Sort, Order, SortVersion string
	// clock is what the completion and next sorts count aired and coming episodes (and coming releases, by its day)
	// against; listTitles sets it.
	clock airClock
	// providerKeys are the provider keys Provider stands for (providerKeys: a cache code's keys); listTitles sets
	// them. Empty: Provider itself.
	providerKeys []string
}

var (
	titleSorts = map[string]bool{"": true, "added": true, "name": true, "year": true, "release": true, "rating": true,
		"size": true, "completion": true, "quality": true, "activity": true, "next": true, "created": true, "runtime": true,
		"certification": true, "network": true}
	// versionKeyRE is what a sort_version may look like (a policy version key).
	versionKeyRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	availabilities = map[string]bool{"": true, "missing": true, "partial": true, "upgrade": true, "available": true, "none": true}
)

// curFile is true when version_file vf is a current file of its version: the owning instance's, or any file of a
// version that has no owner. vs is the version's version_source row.
const curFile = `(vs.instance IS NULL OR vs.instance = vf.instance)`

// linkSizeSQL is the size of link l's file: its content file's, else (an adopted or imported link, with no content
// file) the size its file metadata keeps. cf is the LEFT JOINed content_file.
const linkSizeSQL = `COALESCE(cf.size_bytes, json_extract(l.file_metadata, '$.Size'))`

// linkContentSQL is the registry content (content.id) of link l's file, or NULL: its content file's content, else
// the content of the first hash the link records: its download's, the file metadata's InfoHash, the adopted source's
// hash (an adopted link has no content file). An empty string counts as missing at every step. It reads l only.
const linkContentSQL = `COALESCE((SELECT cf_c.content_id FROM content_file cf_c WHERE cf_c.id = l.content_file_id),
	(SELECT c_l.id FROM content c_l WHERE c_l.info_hash = lower(COALESCE(
		(SELECT NULLIF(d_l.info_hash, '') FROM download d_l WHERE d_l.id = l.download_id),
		CASE WHEN json_valid(l.file_metadata) THEN NULLIF(json_extract(l.file_metadata, '$.InfoHash'), '') END,
		CASE WHEN json_valid(l.adopted_source) THEN NULLIF(json_extract(l.adopted_source, '$.hash'), '') END))))`

// providerHolds is the condition on content_provider cp that the provider holds the content now, complete: listed
// on the account (present) and downloaded there (progress 100; the provider sync stores every provider's progress
// as 0-100). A torrent still downloading or never finished is not counted.
const providerHolds = `cp.state = 'present' AND cp.progress >= 100`

// liveVersion is the condition that version v is in the library: not removed (catalog.Remove lists removed versions
// in arrview_removed_version and keeps their rows).
const liveVersion = `NOT EXISTS (SELECT 1 FROM arrview_removed_version rv WHERE rv.version_id = v.id)`

// liveLink is the condition that link l files into some version in the library (liveVersion): the links whose
// providers the list rows show.
const liveLink = `EXISTS (SELECT 1 FROM link_version lv_l JOIN version v ON v.id = lv_l.version_id
	WHERE lv_l.link_id = l.id AND ` + liveVersion + `)`

// Per-title expressions over t (the title row).
const (
	nameExpr = `CASE WHEN t.sort_name = '' THEN lower(t.name) ELSE t.sort_name END`
	// addedExpr and sizeExpr read the copies in the library (liveVersion, liveLink), as the rows show them.
	addedExpr = `NULLIF(MAX(
		COALESCE((SELECT MAX(vf.added_at) FROM version v JOIN version_file vf ON vf.version_id = v.id
			LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id = t.id AND ` + liveVersion + ` AND ` +
		curFile + `), 0),
		COALESCE((SELECT MAX(l.created_at) FROM link l WHERE l.title_id = t.id AND l.state = 'active' AND ` + liveLink +
		`), 0)), 0)`
	sizeExpr = `NULLIF(
		COALESCE((SELECT SUM(vf.size_bytes) FROM version v JOIN version_file vf ON vf.version_id = v.id
			LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id = t.id AND ` + liveVersion + ` AND ` +
		curFile + `), 0) +
		COALESCE((SELECT SUM(COALESCE(` + linkSizeSQL + `, 0)) FROM link l
			LEFT JOIN content_file cf ON cf.id = l.content_file_id WHERE l.title_id = t.id AND l.state = 'active' AND ` +
		liveLink + `), 0), 0)`
)

// availStateSQL is version v's state as availability reads it (versionState.state): a series copy that holds files
// and is wanted only for episodes still to air or not monitored is complete over what has aired, as its episode
// counts say: filled when some held episode is below cutoff, else cutoff_met. Anything else keeps v.state. The
// clock's values are written into the SQL (a number and a YYYY-MM-DD day).
func availStateSQL(c airClock) string {
	aired := fmt.Sprintf(`(CASE WHEN e.air_date_utc_at IS NOT NULL THEN e.air_date_utc_at <= %d
		ELSE e.air_date IS NOT NULL AND e.air_date <= '%s' END)`, c.nowMs, c.today)
	return `(CASE WHEN v.state = 'wanted' AND (SELECT tk.kind FROM title tk WHERE tk.id = v.title_id) <> 'movie' AND ` +
		hasFileSQL + ` AND NOT EXISTS (SELECT 1 FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
			WHERE s.version_id = v.id AND e.season > 0 AND s.state = 'wanted' AND s.monitored = 1 AND ` + aired + `)
		THEN CASE WHEN EXISTS (SELECT 1 FROM episode_version_state s WHERE s.version_id = v.id AND s.state = 'filled')
			THEN 'filled' ELSE 'cutoff_met' END
		ELSE v.state END)`
}

// availabilityConds is the condition for each availability, matching availabilityOf over the versions in the
// library (liveVersion) and their availStateSQL, as the list's rows show them.
func availabilityConds(c airClock) map[string]string {
	st := availStateSQL(c)
	live := `FROM version v WHERE v.title_id = t.id AND ` + liveVersion
	nVersions := `(SELECT COUNT(*) ` + live + `)`
	// nMissing: wanted versions with no file; nWantedFile: wanted versions that have some (a series part-way).
	nMissing := `(SELECT COUNT(*) ` + live + ` AND ` + st + ` = 'wanted' AND NOT ` + hasFileSQL + `)`
	nWantedFile := `(SELECT COUNT(*) ` + live + ` AND ` + st + ` = 'wanted' AND ` + hasFileSQL + `)`
	nFilled := `(SELECT COUNT(*) ` + live + ` AND ` + st + ` = 'filled')`
	// anyFile: some version has a file; completeVersion: some version is complete (filled or at cutoff).
	anyFile := `EXISTS (SELECT 1 ` + live + ` AND ` + hasFileSQL + `)`
	completeVersion := `EXISTS (SELECT 1 ` + live + ` AND ` + st + ` IN ('filled', 'cutoff_met'))`
	return map[string]string{
		"missing":   nVersions + ` > 0 AND ` + nMissing + ` = ` + nVersions,
		"partial":   `(` + nMissing + ` > 0 AND ` + nMissing + ` < ` + nVersions + `) OR ` + nWantedFile + ` > 0`,
		"upgrade":   nMissing + ` = 0 AND ` + nWantedFile + ` = 0 AND ` + nFilled + ` > 0`,
		"available": nVersions + ` > 0 AND ` + nMissing + ` = 0 AND ` + nWantedFile + ` = 0 AND ` + nFilled + ` = 0`,
		"none":      nVersions + ` = 0`,
		// The watchable availabilities, what available, partial and missing mean to a caller without "see files"
		// (plainFilter), matching plainAvailability.
		"watch_available": nVersions + ` > 0 AND ((t.kind = 'movie' AND ` + anyFile + `) OR (t.kind <> 'movie' AND ` +
			completeVersion + `))`,
		"watch_partial": `t.kind <> 'movie' AND NOT ` + completeVersion + ` AND ` + anyFile,
		"watch_missing": nVersions + ` > 0 AND NOT ` + anyFile + ` AND NOT (t.kind <> 'movie' AND ` + completeVersion + `)`,
	}
}

// availOr is a version's availability state (availStateSQL) when it was read, else its state.
func availOr(avail, state string) string {
	if avail != "" {
		return avail
	}
	return state
}

// versionState is what availabilityOf needs of a version.
type versionState struct {
	state   string
	hasFile bool
}

// availabilityOf is a title's poster badge from its versions: missing when no version has a file and all are
// wanted; partial when some are missing, or a wanted version has some files (a series part-way); upgrade when all
// have files and some are below cutoff; available when all are at cutoff.
func availabilityOf(vs []versionState) string {
	if len(vs) == 0 {
		return "none"
	}
	missing, wantedFile, filled := 0, 0, 0
	for _, v := range vs {
		switch {
		case v.state == "wanted" && !v.hasFile:
			missing++
		case v.state == "wanted":
			wantedFile++
		case v.state == "filled":
			filled++
		}
	}
	switch {
	case missing == len(vs):
		return "missing"
	case missing > 0 || wantedFile > 0:
		return "partial"
	case filled > 0:
		return "upgrade"
	}
	return "available"
}

// hasQuality is a condition on title t: some current file (arr file or active link) of a copy in the library has the
// quality given twice as arguments.
func hasQuality() string { return qualityCond("= ?") }

// hasQualityLike is hasQuality with a LIKE pattern.
func hasQualityLike() string { return qualityCond("LIKE ?") }

func qualityCond(match string) string {
	return `(EXISTS (SELECT 1 FROM version v JOIN version_file vf ON vf.version_id = v.id
		LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id = t.id AND ` + liveVersion + ` AND ` + curFile +
		` AND vf.quality ` + match + `)
		OR EXISTS (SELECT 1 FROM link l WHERE l.title_id = t.id AND l.state = 'active' AND ` + liveLink +
		` AND l.quality ` + match + `))`
}

// hasProvider is the same for a debrid provider holding the file's content now (providerHolds), with n provider
// keys given twice as arguments, over the versions in the library. An active link's content is linkContentSQL, as
// currentFiles and the provider facet read it.
func hasProvider(n int) string {
	in := store.Placeholders(n)
	// CROSS JOIN keeps the title's versions and links as the outer loop: without statistics SQLite would otherwise
	// scan the provider's rows for every title.
	return `(EXISTS (SELECT 1 FROM version v CROSS JOIN version_file vf ON vf.version_id = v.id
		LEFT JOIN version_source vs ON vs.version_id = v.id CROSS JOIN content_provider cp ON cp.content_id = vf.content_id
		WHERE v.title_id = t.id AND ` + liveVersion + ` AND ` + curFile + ` AND ` + providerHolds + `
		AND cp.provider IN (` + in + `))
		OR EXISTS (SELECT 1 FROM link l CROSS JOIN content_provider cp ON cp.content_id = ` + linkContentSQL + `
		WHERE l.title_id = t.id AND l.state = 'active' AND ` + liveLink + ` AND ` + providerHolds + `
		AND cp.provider IN (` + in + `)))`
}

func (g TitleGridFilter) where(conds []string, args []any) ([]string, []any) {
	if g.Anime != nil {
		conds = append(conds, "t.anime = ?")
		args = append(args, *g.Anime)
	}
	if g.Monitored != nil {
		conds = append(conds, "t.monitored = ?")
		args = append(args, *g.Monitored)
	}
	if len(g.Genres) > 0 {
		conds = append(conds, "EXISTS (SELECT 1 FROM json_each(m.genres) g WHERE g.value IN ("+store.Placeholders(len(g.Genres))+"))")
		for _, x := range g.Genres {
			args = append(args, x)
		}
	}
	switch {
	case isResolution(g.Quality):
		// A bare resolution (1080p) matches every quality with it: WEBDL-1080p, Bluray-1080p.
		like := "%" + g.Quality + "%"
		conds = append(conds, hasQualityLike())
		args = append(args, like, like)
	case g.Quality != "":
		conds = append(conds, hasQuality())
		args = append(args, g.Quality, g.Quality)
	}
	if g.Provider != "" {
		keys := g.providerKeys
		if len(keys) == 0 {
			keys = []string{g.Provider}
		}
		conds = append(conds, hasProvider(len(keys)))
		for range 2 {
			for _, k := range keys {
				args = append(args, k)
			}
		}
	}
	if g.YearFrom > 0 {
		conds = append(conds, "t.year >= ?")
		args = append(args, g.YearFrom)
	}
	if g.YearTo > 0 {
		conds = append(conds, "t.year <= ?")
		args = append(args, g.YearTo)
	}
	if c, ok := availabilityConds(g.clock)[g.Availability]; ok {
		conds = append(conds, "("+c+")")
	}
	return conds, args
}

func (g TitleGridFilter) orderBy() (string, []any) {
	expr := ""
	var args []any
	switch g.Sort {
	case "added":
		expr = addedExpr
	case "year":
		expr = "t.year"
	case "release":
		expr = "COALESCE(NULLIF(m.release_date, ''), CASE WHEN t.year IS NOT NULL THEN printf('%04d-99', t.year) END)"
	case "rating":
		expr = "CASE WHEN m.rating_votes > 0 THEN m.rating END"
	case "size":
		expr = sizeExpr
	case "created":
		expr = "t.created_at"
	case "runtime":
		expr = "NULLIF(m.runtime_minutes, 0)"
	case "certification":
		expr = "NULLIF(m.certification, '')"
	case "network":
		expr = "NULLIF(lower(m.network), '')"
	case "completion", "quality", "activity", "next":
		expr, args = copySortExpr(g.Sort, g.SortVersion, g.clock)
	}
	dir := "DESC"
	if g.Order == "asc" || (g.Order == "" && (expr == "" || g.Sort == "next" || g.Sort == "certification" ||
		g.Sort == "network")) {
		dir = "ASC"
	}
	if expr == "" {
		return " ORDER BY " + nameExpr + " " + dir + ", t.id", nil
	}
	// The expression appears twice (nulls last, then the value), so its arguments do too.
	return " ORDER BY (" + expr + ") IS NULL, (" + expr + ") " + dir + ", " + nameExpr + ", t.id", append(args, args...)
}

func boolParam(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, errors.New(name + " must be true or false")
	}
	return &b, nil
}

// gridFilter reads the grid parameters of GET titles.
func gridFilter(r *http.Request) (TitleGridFilter, error) {
	q := r.URL.Query()
	g := TitleGridFilter{Quality: q.Get("quality"), Provider: q.Get("provider"), Availability: q.Get("availability"),
		Sort: q.Get("sort"), Order: q.Get("order")}
	var err error
	if g.Anime, err = boolParam(r, "anime"); err != nil {
		return g, err
	}
	if g.Monitored, err = boolParam(r, "monitored"); err != nil {
		return g, err
	}
	for _, v := range q["genre"] {
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				g.Genres = append(g.Genres, x)
			}
		}
	}
	if g.YearFrom, err = intParam(r, "year_from", 0); err != nil {
		return g, err
	}
	if g.YearTo, err = intParam(r, "year_to", 0); err != nil {
		return g, err
	}
	if !titleSorts[g.Sort] {
		return g, errors.New("sort must be added, created, name, year, release, rating, runtime, certification, network, size, " +
			"completion, quality, activity or next")
	}
	g.SortVersion = q.Get("sort_version")
	if g.SortVersion != "" && !versionKeyRE.MatchString(g.SortVersion) {
		return g, errors.New("sort_version must be a library key")
	}
	if g.Order != "" && g.Order != "asc" && g.Order != "desc" {
		return g, errors.New("order must be asc or desc")
	}
	if !availabilities[g.Availability] {
		return g, errors.New("availability must be missing, partial, upgrade, available or none")
	}
	return g, nil
}

// TitleFacts is what a list row knows from title_meta and its current files.
type TitleFacts struct {
	Genres         []string `json:"genres"`
	Rating         *float64 `json:"rating,omitempty"`
	Certification  string   `json:"certification,omitempty"`
	RuntimeMinutes *int     `json:"runtime_minutes,omitempty"`
	// ReleaseDate is the TMDb release date (movies) or first air date (series), YYYY-MM-DD.
	ReleaseDate string `json:"release_date,omitempty"`
	// Availability is the poster badge: missing, partial, upgrade, available or none (see TitleGridFilter). upgrade
	// means below cutoff: every version has a file and some file is below its profile's cutoff; the UI says
	// "Below cutoff", and "Upgrade available" only with UpgradeSeen.
	Availability string `json:"availability"`
	// UpgradeSeen: some version below cutoff has a better release seen (upgrade.go).
	UpgradeSeen bool `json:"upgrade_seen,omitempty"`
	// UpgradeState, with availability upgrade only: the most pressing upgrade state among the title's versions
	// (upgrade_available, higher_source_exists, below_cutoff, best_source_seen, best_available), so the poster badge
	// says what the title page says. The source-ceiling states need manage (redactRowCeiling).
	UpgradeState string `json:"upgrade_state,omitempty"`
	// AddedAt is the newest current file's time.
	AddedAt *time.Time `json:"added_at,omitempty"`
	// SizeBytes is the current files' total (see files only).
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	// Network is the series' network (a movie's first production company).
	Network string `json:"network,omitempty"`
	// Status is the stored title_meta status: a movie's release status (availability.Status: announced, inCinemas,
	// released) or a series' TMDb status (Returning Series, Ended, Canceled, In Production, Planned, Pilot).
	Status string `json:"status,omitempty"`
	// OriginalLanguage is TMDb's original language (ISO 639-1).
	OriginalLanguage string `json:"original_language,omitempty"`
}

func factsFor(m *art.Meta) TitleFacts {
	f := TitleFacts{Genres: []string{}}
	if m == nil {
		return f
	}
	if m.Genres != nil {
		f.Genres = m.Genres
	}
	if m.RefreshedAt != nil && m.RatingVotes > 0 {
		v := m.Rating
		f.Rating = &v
	}
	if m.RuntimeMinutes > 0 {
		v := m.RuntimeMinutes
		f.RuntimeMinutes = &v
	}
	f.Certification, f.ReleaseDate, f.Network = m.Certification, m.ReleaseDate, m.Network
	f.Status, f.OriginalLanguage = m.Status, m.OriginalLanguage
	return f
}

// VersionFiles is a version's current files in brief.
type VersionFiles struct {
	// Label is the version's name for people: "4K WEB", "4K Remux".
	Label string `json:"label"`
	// Quality is the current file's quality; for a series, the quality most of its episode files have.
	Quality string `json:"quality,omitempty"`
	// Files is how many current files the version has (episodes for a series).
	// Files and AddedAt are for callers who see files.
	Files   int        `json:"files,omitempty"`
	AddedAt *time.Time `json:"added_at,omitempty"`
	// SizeBytes and Providers are for callers who see files. Providers are cache codes (rd, ad, tb; cacheCodes) of
	// the providers that hold a current file now.
	SizeBytes *int64   `json:"size_bytes,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// versionLabel names a version for people: its name when it has one, else a label from its key and profile:
// movie-web with profile web-2160p is "4K WEB", tv-2160p is "4K", anime-tv is "Anime".
func (w *Web) versionLabel(key string) string {
	profile := ""
	if w.pol != nil {
		if v := w.pol.Versions[key]; v != nil {
			if name := strings.TrimSpace(v.Name); name != "" {
				return name
			}
			profile = v.Profile
		}
	}
	return labelFor(key, profile)
}

func labelFor(key, profile string) string {
	words := map[string]string{"2160p": "4K", "4k": "4K", "uhd": "4K", "1080p": "1080p", "720p": "720p",
		"web": "WEB", "remux": "Remux", "bluray": "Blu-ray", "anime": "Anime", "hdr": "HDR"}
	var res, rest []string
	seen := map[string]bool{}
	add := func(tok string) {
		w, ok := words[tok]
		if !ok || seen[w] {
			return
		}
		seen[w] = true
		switch w {
		case "4K", "1080p", "720p":
			res = append(res, w)
		default:
			rest = append(rest, w)
		}
	}
	for _, t := range strings.Split(strings.ToLower(key), "-") {
		add(t)
	}
	if !seen["Anime"] {
		for _, t := range strings.Split(strings.ToLower(profile), "-") {
			add(t)
		}
	}
	if seen["Anime"] {
		return "Anime"
	}
	out := strings.Join(append(res, rest...), " ")
	if out == "" {
		return key
	}
	return out
}

// copyName is one copy named next to others: its version key and its label.
type copyName struct{ key, label string }

// distinctLabels gives each copy (one per key) a label unique among the copies named together, as the SPA's
// distinctLabels (web/src/lib/copies.ts) does: labels that collide ("4K" for tv-2160p and tv-2160p-theatarr) get the
// version key ("4K · tv-2160p-theatarr"); unique labels stay plain.
func distinctLabels(cs []copyName) map[string]string {
	var order []copyName
	seen := map[string]bool{}
	for _, c := range cs {
		if c.key == "" || seen[c.key] {
			continue
		}
		seen[c.key] = true
		if c.label == "" {
			c.label = c.key
		}
		order = append(order, c)
	}
	count := func(names map[string]string) map[string]int {
		n := map[string]int{}
		for _, l := range names {
			n[l]++
		}
		return n
	}
	names := map[string]string{}
	for _, c := range order {
		names[c.key] = c.label
	}
	first := count(names)
	for _, c := range order {
		if first[c.label] > 1 {
			names[c.key] = c.label + " · " + c.key
		}
	}
	return names
}

// keyLabels is distinctLabels for keys named together (request lists, cross-title notes): same-named copies are told
// apart by key.
func (w *Web) keyLabels(keys []string) map[string]string {
	cs := make([]copyName, len(keys))
	for i, k := range keys {
		cs[i] = copyName{key: k, label: w.versionLabel(k)}
	}
	return distinctLabels(cs)
}

type fileAgg struct {
	quality string
	n       int
	size    int64
	added   int64
	path    string // the first active link's path (links only)
}

// fillVersionFiles sets each version's label and current files, and each row's availability, added time and
// size.
func (w *Web) fillVersionFiles(ctx context.Context, db *sql.DB, items []TitleRow) error {
	var ids []any
	byID := map[int64]*VersionBrief{}
	for i := range items {
		for j := range items[i].Versions {
			v := &items[i].Versions[j]
			v.Label = w.versionLabel(v.Key)
			byID[v.ID] = v
			ids = append(ids, v.ID)
		}
	}
	if len(ids) > 0 {
		aggs, provs, err := w.currentFiles(ctx, db, ids)
		if err != nil {
			return err
		}
		var lib map[string]*policy.LibraryVersion
		if w.pol != nil {
			lib = w.pol.LibrarySettings().Versions
		}
		for id, v := range byID {
			v.VersionFiles = briefFiles(v.Label, aggs[id], w.cacheCodes(provs[id]))
			if w.pol != nil && w.pol.Versions[v.Key] != nil {
				v.Profile = w.pol.Versions[v.Key].Profile
			}
			if lv := lib[v.Key]; lv != nil {
				v.Folder = titleFolder(lv.Roots, firstPath(aggs[id]))
			}
		}
	}
	for i := range items {
		it := &items[i]
		states := make([]versionState, len(it.Versions))
		var size, added int64
		for j, v := range it.Versions {
			states[j] = versionState{availOr(v.availState, v.State), v.HasFile}
			if v.SizeBytes != nil {
				size += *v.SizeBytes
			}
			if v.AddedAt != nil {
				added = max(added, v.AddedAt.UnixMilli())
			}
		}
		it.Availability = availabilityOf(states)
		if size > 0 {
			it.SizeBytes = &size
		}
		if added > 0 {
			t := ts(added)
			it.AddedAt = &t
		}
	}
	return w.markUpgradeSeen(ctx, db, items)
}

// firstPath is the smallest link path of aggs, "" when none.
func firstPath(aggs []fileAgg) string {
	p := ""
	for _, a := range aggs {
		if a.path != "" && (p == "" || a.path < p) {
			p = a.path
		}
	}
	return p
}

// titleFolder is the folder of a library path p: the root of roots that holds it (the longest) plus the first path
// element below it. "" when p is under no root or sits directly in one.
func titleFolder(roots []string, p string) string {
	best := ""
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if r != "" && strings.HasPrefix(p, r+"/") && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return ""
	}
	first, _, ok := strings.Cut(p[len(best)+1:], "/")
	if !ok || first == "" {
		return ""
	}
	return best + "/" + first
}

func briefFiles(label string, aggs []fileAgg, provs []string) VersionFiles {
	vf := VersionFiles{Label: label, Providers: provs}
	best := -1
	var size, added int64
	for _, a := range aggs {
		vf.Files += a.n
		size += a.size
		added = max(added, a.added)
		if a.quality != "" && a.n > best {
			best, vf.Quality = a.n, a.quality
		}
	}
	if vf.Files > 0 {
		vf.SizeBytes = &size
	}
	if added > 0 {
		t := ts(added)
		vf.AddedAt = &t
	}
	return vf
}

// currentFiles groups the current files of versions ids by quality, and lists the providers (keys) that hold them
// now (providerHolds; a link's content is linkContentSQL).
func (w *Web) currentFiles(ctx context.Context, db *sql.DB, ids []any) (map[int64][]fileAgg, map[int64][]string, error) {
	in := store.Placeholders(len(ids))
	aggs := map[int64][]fileAgg{}
	scan := func(r *sql.Rows) error {
		var vid int64
		var a fileAgg
		if err := r.Scan(&vid, &a.quality, &a.n, &a.size, &a.added, &a.path); err != nil {
			return err
		}
		aggs[vid] = append(aggs[vid], a)
		return nil
	}
	if err := scanAll(ctx, db, `SELECT vf.version_id, vf.quality, COUNT(*), COALESCE(SUM(vf.size_bytes), 0),
		COALESCE(MAX(vf.added_at), 0), '' FROM version_file vf LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE vf.version_id IN (`+in+`) AND `+curFile+` GROUP BY vf.version_id, vf.quality ORDER BY vf.version_id, vf.quality`,
		ids, scan); err != nil {
		return nil, nil, err
	}
	if err := scanAll(ctx, db, `SELECT lv.version_id, COALESCE(l.quality, ''), COUNT(*),
		COALESCE(SUM(COALESCE(`+linkSizeSQL+`, 0)), 0),
		COALESCE(MAX(l.created_at), 0), COALESCE(MIN(l.path), '') FROM link_version lv JOIN link l ON l.id = lv.link_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id WHERE lv.version_id IN (`+in+`) AND l.state = 'active'
		GROUP BY lv.version_id, l.quality ORDER BY lv.version_id`, ids, scan); err != nil {
		return nil, nil, err
	}
	provs, err := scanProviders(ctx, db, `SELECT vf.version_id, cp.provider FROM version_file vf
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id JOIN content_provider cp ON cp.content_id = vf.content_id
		WHERE vf.version_id IN (`+in+`) AND `+curFile+` AND `+providerHolds+`
		UNION SELECT lv.version_id, cp.provider FROM link_version lv JOIN link l ON l.id = lv.link_id
		JOIN content_provider cp ON cp.content_id = `+linkContentSQL+`
		WHERE lv.version_id IN (`+in+`) AND l.state = 'active' AND `+providerHolds+` ORDER BY 1, 2`,
		append(append([]any{}, ids...), ids...))
	return aggs, provs, err
}

// scanProviders runs q, which yields (id, provider key) rows, and groups the keys by id.
func scanProviders(ctx context.Context, db *sql.DB, q string, args []any) (map[int64][]string, error) {
	out := map[int64][]string{}
	err := scanAll(ctx, db, q, args, func(r *sql.Rows) error {
		var id int64
		var p string
		if err := r.Scan(&id, &p); err != nil {
			return err
		}
		out[id] = append(out[id], p)
		return nil
	})
	return out, err
}

// cacheCodes turns provider keys into cache codes (rd, ad, tb) by their kind in the running settings; a key of no
// known kind stays as it is. No duplicates; rd, ad, tb first, then the rest in order. nil for none.
func (w *Web) cacheCodes(keys []string) []string {
	var codes, rest []string
	for _, k := range keys {
		c := ""
		if w.pol != nil {
			if p := w.pol.Providers[k]; p != nil {
				c = indexer.CacheCode(p.Kind)
			}
		}
		if c == "" {
			if !slices.Contains(rest, k) {
				rest = append(rest, k)
			}
		} else if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	order := map[string]int{"rd": 0, "ad": 1, "tb": 2}
	slices.SortFunc(codes, func(a, b string) int { return order[a] - order[b] })
	return append(codes, rest...)
}

// providerKeys is what a provider filter value stands for: the keys of every provider whose kind has that cache
// code, and the value itself as a key.
func (w *Web) providerKeys(v string) []string {
	out := []string{v}
	if w.pol != nil {
		for k, p := range w.pol.Providers {
			if p != nil && indexer.CacheCode(p.Kind) == v && k != v {
				out = append(out, k)
			}
		}
	}
	slices.Sort(out[1:])
	return out
}
