package web

import (
	"net/http"
	"regexp"

	"github.com/theatarr/theatarr/internal/auth"
)

// Permissions (docs/design/ui.md, docs/design/users.md). Every route declares the permission it needs when it is
// registered (handle); the guard refuses a caller without it (403). Handlers that return library data also ask
// permsOf for the caller's permissions and strip what the caller may not see: users see availability only
// (whether a version has a file, and its resolution: "Available in 1080p"); quality sources, file counts and dates,
// cutoffs, upgrade explanations, history, paths, hashes, release names, sizes, scores, providers and arr records are
// for managers and admins.

// Perms is what one caller may do or see.
type Perms struct {
	// Bits is the full set (internal/auth Perm).
	Bits auth.Perm
	// SeeFiles: file paths, info hashes, release names, sizes, scores, providers, arr records and the registry; the
	// quality with its source, file counts and dates, cutoffs and history.
	SeeFiles bool
	// Admin: the admin panel and the title page's Details tab.
	Admin bool
}

// PermsOf builds Perms from bits.
func PermsOf(bits auth.Perm) Perms {
	return Perms{Bits: bits, SeeFiles: bits.Has(auth.PermSeeFiles), Admin: bits.Has(auth.PermAdmin)}
}

// Has reports whether the caller holds every bit of q.
func (p Perms) Has(q auth.Perm) bool { return p.Bits.Has(q) }

// List is the permission names, as GET auth/me reports them.
func (p Perms) List() []string { return p.Bits.Names() }

// PermsFunc gives an identity's permissions.
type PermsFunc func(id *auth.Identity) Perms

// identityPerms is the rule: an identity carries its account's permissions (a key, its owner's).
func identityPerms(id *auth.Identity) Perms {
	if id == nil {
		return Perms{}
	}
	return PermsOf(id.Perms)
}

func (w *Web) permsOf(r *http.Request) Perms {
	return w.perms(auth.FromContext(r.Context()))
}

// resolutionRE finds the resolution in a quality name: Bluray-1080p, WEBDL-2160p, HDTV-720p.
var resolutionRE = regexp.MustCompile(`(?i)\b(2160|1080|720|576|480)p\b`)

// Resolution is what a caller without SeeFiles sees of a quality: the resolution alone (Bluray-1080p is 1080p,
// Remux-2160p and WEBDL-2160p are 2160p), or "" when the name carries none (DVD, SDTV, Raw-HD, Unknown). The source
// (WEB, Blu-ray, Remux, HDTV) is file detail.
func Resolution(quality string) string {
	if m := resolutionRE.FindStringSubmatch(quality); m != nil {
		return m[1] + "p"
	}
	return ""
}

// isResolution reports whether q is a bare resolution as Resolution gives it.
func isResolution(q string) bool { return q != "" && Resolution(q) == q }

// plainState is a version or episode state for a caller without SeeFiles: whether it has a file, not how the file
// stands against the cutoff (cutoff_met reads as filled).
func plainState(s string) string {
	if s == "cutoff_met" {
		return "filled"
	}
	return s
}

// plainAvailability is a title's poster badge for a caller without SeeFiles: whether it can be watched, not whether
// every version the household keeps is there. A movie with a file in any version is
// available; a series is available when some version is complete (filled or at its cutoff), partial ("some
// episodes available") when versions have files but none is complete, and missing when no version has a file.
// Below cutoff is available. The SQL twins are watchConds.
func plainAvailability(kind string, vs []versionState) string {
	if len(vs) == 0 {
		return "none"
	}
	anyFile, complete := false, false
	for _, v := range vs {
		anyFile = anyFile || v.hasFile
		complete = complete || v.state == "filled" || v.state == "cutoff_met"
	}
	switch {
	case kind == "movie" && anyFile, kind != "movie" && complete:
		return "available"
	case kind != "movie" && anyFile:
		return "partial"
	}
	return "missing"
}

// redactVersionBrief drops what a caller without SeeFiles may not see from a list row's version: sizes, providers,
// the file count and date, the source in the quality, and the profile, availability rule and folder.
func redactVersionBrief(v *VersionBrief) {
	v.SizeBytes = nil
	v.Providers = nil
	v.State = plainState(v.State)
	v.Quality = Resolution(v.Quality)
	v.Files, v.AddedAt, v.UpgradeSeen = 0, nil, false
	v.Profile, v.MinimumAvailability, v.Folder = "", "", ""
	// Copies are a household detail: no ownership or downloads; coverage stays as watchability.
	v.Control, v.Acquisition = nil, nil
	if c := v.Coverage; c != nil {
		v.Coverage = &CopyCoverage{Have: c.Have, Total: c.Total, Aired: c.Aired, Upcoming: c.Upcoming}
	}
}

// redactRow keeps the title-level added time (the Home row "Recently added" and the added sort show it); the
// availability is plainAvailability.
func redactRow(r *TitleRow) {
	states := make([]versionState, len(r.Versions))
	for i, v := range r.Versions {
		states[i] = versionState{availOr(v.availState, v.State), v.HasFile, v.Monitored}
	}
	redactRowWith(r, states)
}

func redactRowWith(r *TitleRow, states []versionState) {
	r.SizeBytes = nil
	r.Availability = plainAvailability(r.Kind, states)
	r.UpgradeSeen, r.UpgradeState = false, ""
	for i := range r.Versions {
		redactVersionBrief(&r.Versions[i])
	}
}

// redactRowCeiling hides the source-ceiling states from a row for a caller without manage (as redactCeiling does on
// the title page): best available, best source seen and higher source exists read as below cutoff.
func redactRowCeiling(r *TitleRow) {
	switch r.UpgradeState {
	case "best_available", "best_source_seen", "higher_source_exists":
		r.UpgradeState = "below_cutoff"
	}
}

// redactRowFor redacts a list row for a caller: everything file-related without see_files, the ceiling states
// without manage.
func redactRowFor(p Perms, r *TitleRow) {
	if !p.SeeFiles {
		redactRow(r)
	} else if !canManage(p) {
		redactRowCeiling(r)
	}
}

// redactVersion keeps a version's state, its resolution, languages and edition, and its episode counts: no files,
// history, cutoff or upgrade explanation.
func redactVersion(v *Version) {
	v.State = plainState(v.State)
	v.Source = ""
	v.Files, v.Links = []File{}, []Link{}
	v.Providers, v.CachedOn = nil, nil
	if c := v.Current; c != nil {
		*c = CurrentFile{Quality: Resolution(c.Quality), Languages: c.Languages, Edition: c.Edition}
	}
	v.History = nil
	v.Upgrade = nil
	v.Acquisition = nil
	// Watchability: an aired episode no library holds is missing whatever the library's monitoring.
	if v.Episodes != nil {
		v.Episodes.Missing = v.Episodes.watchMissing
	}
	redactControl(v.Control)
}

func redactTitle(d *TitleDetail) {
	// The title page's versions are d.Versions (TitleRow.Versions stays empty there); removed ones count in nothing.
	redactRowWith(&d.TitleRow, liveStates(d.Versions))
	d.Registry = []RegistryEntry{}
	for i := range d.Versions {
		redactVersion(&d.Versions[i])
	}
}

// redactEpisodeCell keeps an episode's state, monitoring, whether it has a file and its resolution.
func redactEpisodeCell(c EpisodeCell) EpisodeCell {
	return EpisodeCell{State: plainState(c.State), Monitored: c.Monitored, HasFile: c.HasFile, Quality: Resolution(c.Quality)}
}

func redactEpisodeCells(e *Episode) {
	for k, c := range e.Versions {
		e.Versions[k] = redactEpisodeCell(c)
	}
}

// redactEpisodes strips GET titles/{id}/episodes for a caller without SeeFiles: no cutoff, upgrade explanations,
// below-cutoff counts, sizes or scores; qualities become resolutions.
func redactEpisodes(out *EpisodeList) {
	for i := range out.Versions {
		out.Versions[i].Cutoff = nil
		redactControl(out.Versions[i].Control)
	}
	for i := range out.Seasons {
		for k, c := range out.Seasons[i].Versions {
			// Watchability: missing whatever the version's monitoring (watchMissing).
			out.Seasons[i].Versions[k] = SeasonCounts{Have: c.Have, Missing: c.watchMissing, Monitored: c.Monitored}
		}
	}
	for i := range out.Episodes {
		redactEpisodeCells(&out.Episodes[i])
	}
}

// plainFilter turns GET titles' filters into what a caller without SeeFiles may ask: a quality only as a resolution,
// no cutoff states, and available covering below cutoff (as redactRow shows it). It returns why a filter is refused.
func (f *TitleFilter) plainFilter() string {
	if f.Quality != "" && !isResolution(f.Quality) {
		return "filtering by a source quality needs the see-files permission; use a resolution such as 1080p"
	}
	switch f.State {
	case "cutoff_met":
		return "filtering by cutoff needs the see-files permission"
	case "filled":
		f.plainState = true
	}
	if f.Upgrade != "" {
		return "filtering by upgrades needs the see-files permission"
	}
	switch f.Availability {
	case "upcoming", "unmonitored":
		return "filtering by release or monitoring needs the see-files permission"
	case "available", "partial", "missing":
		// Watchability, as redactRow shows it (plainAvailability).
		f.Availability = "watch_" + f.Availability
	}
	return ""
}
