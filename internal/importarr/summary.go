package importarr

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// Summary is the result of importing one instance.
type Summary struct {
	Instance    string   `json:"instance"`
	Flavour     string   `json:"flavour"`
	Version     string   `json:"version"`
	AppVersion  string   `json:"app_version"`
	RootFolders []string `json:"root_folders"`

	Titles   TitleCounts    `json:"titles"`
	Files    FileCounts     `json:"files"`
	Episodes EpisodeCounts  `json:"episodes,omitzero"`
	History  HistoryCounts  `json:"history"`
	Registry RegistryCounts `json:"registry"`
	// Collections counts the arr's monitored collections carried over to movie intake (collections.go).
	Collections CollectionCounts `json:"collections,omitzero"`
	Tags        int              `json:"tags"`
	Profiles    int              `json:"profiles"`

	// Unmapped lists arr titles that were not imported, with the reason.
	Unmapped []Unmapped `json:"unmapped,omitempty"`
	// Warnings are things worth a look that did not stop the import.
	Warnings []string `json:"warnings,omitempty"`
	// Changes counts the rows this run changed, per table. A re-run with nothing new upstream has none.
	Changes Changes `json:"changes"`
}

// TitleCounts counts the instance's titles.
type TitleCounts struct {
	Total     int `json:"total"`
	Imported  int `json:"imported"`
	Unmapped  int `json:"unmapped"`
	Monitored int `json:"monitored"`
	Wanted    int `json:"wanted"`
	Filled    int `json:"filled"`
	CutoffMet int `json:"cutoff_met"`
	// Shared counts imported titles that another instance had already brought in (one Title, several versions).
	Shared int `json:"shared"`
	// Removed counts titles stored from an earlier run that the arr no longer has.
	Removed int `json:"removed"`
}

// FileCounts counts the instance's files.
type FileCounts struct {
	Total int `json:"total"`
	// WithImport have an import event in history (the file's download id).
	WithImport int `json:"with_import"`
	// WithHash are tied to an info hash, so to a registry content.
	WithHash int `json:"with_hash"`
	// Usenet came in through a usenet download (no info hash).
	Usenet int `json:"usenet"`
	// ManualImport have an import event without a download id (ManualImport by a script).
	ManualImport int `json:"manual_import"`
	// WithReleaseTitle have the grab's release title.
	WithReleaseTitle int `json:"with_release_title"`
	// ReleaseTitleFromImport have no grab; their release title is the import event's source title.
	ReleaseTitleFromImport int `json:"release_title_from_import"`
}

// EpisodeCounts counts a Sonarr instance's episodes.
type EpisodeCounts struct {
	Total     int `json:"total"`
	Monitored int `json:"monitored"`
	WithFile  int `json:"with_file"`
}

// HistoryCounts counts history records.
type HistoryCounts struct {
	// ArrTotal is what the arr reports.
	ArrTotal int `json:"arr_total"`
	// Fetched are the records read this run that were not stored before.
	New int `json:"new"`
	// Stored is the total stored for this instance after the run.
	Stored  int            `json:"stored"`
	ByEvent map[string]int `json:"by_event"`
	// Unmapped are stored records whose arr title is not imported, by reason.
	Unmapped map[string]int `json:"unmapped,omitempty"`
	// Torrent and Usenet count grabs by protocol.
	TorrentGrabs int `json:"torrent_grabs"`
	UsenetGrabs  int `json:"usenet_grabs"`
	// Resolved counts stored records tied to a title this run that had none before.
	Resolved int `json:"resolved,omitempty"`
}

// RegistryCounts counts the registry seeded from this instance's history.
type RegistryCounts struct {
	// Hashes is the number of distinct info hashes in the instance's history.
	Hashes int `json:"hashes"`
	// ContentVersions are (hash, version) pairs.
	ContentVersions int `json:"content_versions"`
	Imported        int `json:"imported"`
	GrabbedOnly     int `json:"grabbed_only"`
	Failed          int `json:"failed"`
}

// Unmapped is an arr title that was not imported.
type Unmapped struct {
	ArrID  int    `json:"arr_id"`
	Name   string `json:"name"`
	Year   int    `json:"year,omitempty"`
	ImdbID string `json:"imdb_id,omitempty"`
	Reason string `json:"reason"`
}

// Conflict is one title whose files differ between instances mapped to the same version.
type Conflict struct {
	Version string   `json:"version"`
	TitleID int64    `json:"title_id"`
	TmdbID  int      `json:"tmdb_id"`
	Name    string   `json:"name"`
	Detail  string   `json:"detail"`
	Sides   []string `json:"sides"`
}

// VersionCounts is the state of one version over every instance, read from the database after the import.
type VersionCounts struct {
	Version   string   `json:"version"`
	Instances []string `json:"instances"`
	Titles    int      `json:"titles"`
	Monitored int      `json:"monitored"`
	Wanted    int      `json:"wanted"`
	Filled    int      `json:"filled"`
	CutoffMet int      `json:"cutoff_met"`
	Files     int      `json:"files"`
	FilesHash int      `json:"files_with_hash"`
	Contents  int      `json:"contents"`
}

// Report is a whole `theatarr import-arr` run.
type Report struct {
	Instances []*Summary      `json:"instances"`
	Versions  []VersionCounts `json:"versions"`
	Conflicts []Conflict      `json:"conflicts"`
	Totals    struct {
		Titles   int `json:"titles"`
		Contents int `json:"contents"`
	} `json:"totals"`
	Enrich *EnrichSummary `json:"enrich,omitempty"`
}

// WriteText prints the report for people.
func (r *Report) WriteText(w io.Writer) {
	for _, s := range r.Instances {
		fmt.Fprintf(w, "== %s (%s %s) -> version %s\n", s.Instance, s.Flavour, s.AppVersion, s.Version)
		fmt.Fprintf(w, "  root folders: %s\n", strings.Join(s.RootFolders, ", "))
		t := s.Titles
		fmt.Fprintf(w, "  titles: %d in the arr, %d imported (%d shared with another instance), %d unmapped, %d removed upstream\n",
			t.Total, t.Imported, t.Shared, t.Unmapped, t.Removed)
		fmt.Fprintf(w, "  versions: %d monitored; %d wanted, %d filled, %d cutoff met\n", t.Monitored, t.Wanted, t.Filled, t.CutoffMet)
		if e := s.Episodes; e.Total > 0 {
			fmt.Fprintf(w, "  episodes: %d, %d monitored, %d with a file\n", e.Total, e.Monitored, e.WithFile)
		}
		f := s.Files
		fmt.Fprintf(w, "  files: %d; %d with an import event (%d manual, no download id), %d with an info hash, %d from usenet\n",
			f.Total, f.WithImport, f.ManualImport, f.WithHash, f.Usenet)
		fmt.Fprintf(w, "    release title: %d from the grab, %d from the import event, %d none\n",
			f.WithReleaseTitle, f.ReleaseTitleFromImport, f.Total-f.WithReleaseTitle-f.ReleaseTitleFromImport)
		h := s.History
		fmt.Fprintf(w, "  history: %d in the arr, %d new, %d stored; grabs %d torrent, %d usenet\n",
			h.ArrTotal, h.New, h.Stored, h.TorrentGrabs, h.UsenetGrabs)
		fmt.Fprintf(w, "    by event: %s\n", mapLine(h.ByEvent))
		if len(h.Unmapped) > 0 {
			fmt.Fprintf(w, "    not tied to a title: %s\n", mapLine(h.Unmapped))
		}
		g := s.Registry
		fmt.Fprintf(w, "  registry: %d hashes; %d hash-version pairs (%d imported, %d grabbed only, %d failed)\n",
			g.Hashes, g.ContentVersions, g.Imported, g.GrabbedOnly, g.Failed)
		fmt.Fprintf(w, "  tags %d, quality profiles %d\n", s.Tags, s.Profiles)
		for _, u := range s.Unmapped {
			fmt.Fprintf(w, "  unmapped: arr id %d %q (%d) imdb %s: %s\n", u.ArrID, u.Name, u.Year, u.ImdbID, u.Reason)
		}
		for _, x := range s.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", x)
		}
		fmt.Fprintf(w, "  changes: %s\n\n", s.Changes)
	}
	if len(r.Versions) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tINSTANCES\tTITLES\tMONITORED\tWANTED\tFILLED\tCUTOFF MET\tFILES\tWITH HASH\tCONTENTS")
		for _, v := range r.Versions {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", v.Version, strings.Join(v.Instances, ","),
				v.Titles, v.Monitored, v.Wanted, v.Filled, v.CutoffMet, v.Files, v.FilesHash, v.Contents)
		}
		tw.Flush()
	}
	fmt.Fprintf(w, "titles in the database: %d; contents: %d\n", r.Totals.Titles, r.Totals.Contents)
	fmt.Fprintf(w, "conflicts: %d\n", len(r.Conflicts))
	for _, c := range r.Conflicts {
		fmt.Fprintf(w, "  %s tmdb %d %q: %s\n", c.Version, c.TmdbID, c.Name, c.Detail)
		for _, side := range c.Sides {
			fmt.Fprintf(w, "    %s\n", side)
		}
	}
	if r.Enrich != nil {
		r.Enrich.WriteText(w)
	}
}

func mapLine(m map[string]int) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}
