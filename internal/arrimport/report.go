package arrimport

// Report is what a Preview found or an Import did. Counts are titles unless named files.
type Report struct {
	DryRun     bool   `json:"dry_run"`
	App        string `json:"app"` // Radarr or Sonarr
	AppVersion string `json:"app_version"`
	Library    string `json:"library"`
	// Writable is false when the library's gate refuses links; WriteBlock says why in plain words (a Preview only:
	// an Import refuses such a library).
	Writable   bool   `json:"writable"`
	WriteBlock string `json:"write_block,omitempty"`
	// TVDBMissing: a Sonarr import without a TVDB key; series come in, but their monitoring and files do not.
	TVDBMissing bool `json:"tvdb_missing"`
	// Titles are the instance's titles.
	Titles int `json:"titles"`
	// AddedTitle are titles new to Theatarr, AddedLibrary titles in Theatarr but not in this library (a Preview:
	// would be added).
	AddedTitle   int `json:"added_title"`
	AddedLibrary int `json:"added_library"`
	// Present are already in this library and stay as they are; Removed were removed from it in Theatarr.
	Present int     `json:"present"`
	Removed int     `json:"removed"`
	Skipped Skipped `json:"skipped"`
	Failed  int     `json:"failed"`
	// Files are the instance's files of the titles brought in or present.
	Files int `json:"files"`
	// Linkable (a Preview) is at most how many files can be linked.
	Linkable int `json:"linkable"`
	// Linked are new links; Existing were linked already; HasFile are files whose episodes or movie the library has
	// its own file for.
	Linked   int `json:"linked"`
	Existing int `json:"existing"`
	HasFile  int `json:"has_file"`
	// NotLinked counts the files not linked, by reason: adopt's (arr_file_missing, not_a_symlink, target_off_mount,
	// dangling, path_taken, no_episodes, naming, has_file) and numbering, no_episode_list.
	NotLinked map[string]int `json:"not_linked"`
	// MissingMonitored (an Import) are titles added now that are monitored and still have something aired to find.
	MissingMonitored int `json:"missing_monitored"`
	// Problems are the files not linked and the titles that failed, at most 200.
	Problems []Problem `json:"problems"`
	// Error is why the run stopped early, "" when it finished.
	Error string `json:"error,omitempty"`
}

// Skipped counts the titles not brought in, by reason.
type Skipped struct {
	Profile    int `json:"profile"`      // a quality profile not ticked
	NoID       int `json:"no_id"`        // no TMDb (movie) or TVDB (series) id
	Excluded   int `json:"excluded"`     // excluded in Theatarr
	DoesNotFit int `json:"does_not_fit"` // anime or not, against the library
}

// Problem is one file not linked, or one title that failed (Path empty).
type Problem struct {
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

const problemsMax = 200

func newReport(req Request) *Report {
	return &Report{DryRun: req.DryRun, App: appName(req.Flavour), Library: req.Library, NotLinked: map[string]int{},
		Problems: []Problem{}}
}

func (r *Report) notLinked(reason string, n int) {
	if n > 0 {
		r.NotLinked[reason] += n
	}
}

func (r *Report) problem(title, path, reason, detail string) {
	if len(r.Problems) < problemsMax {
		r.Problems = append(r.Problems, Problem{Title: title, Path: path, Reason: reason, Detail: detail})
	}
}
