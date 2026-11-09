package naming

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// goldenCase is one file the arr named: everything the arr's naming read, and the names it produced. Cases come
// from `theatarr import-arr` databases (version_file, title, episode, arr_title) and the instance's corpus snapshots
// (config-naming, qualitydefinition, customformat); see dbgolden_test.go.
type goldenCase struct {
	Instance string `json:"instance"`
	FileID   int64  `json:"file_id"`

	Movie   *Movie       `json:"movie,omitempty"`
	MFile   *MovieFile   `json:"movie_file,omitempty"`
	Series  *Series      `json:"series,omitempty"`
	Eps     []Episode    `json:"episodes,omitempty"`
	EFile   *EpisodeFile `json:"episode_file,omitempty"`
	RootDir string       `json:"root"`

	// CFSource says where the custom format names came from: "name" (read back from the arr's own file name,
	// since the import does not store them per file) or "none".
	CFSource string `json:"cf_source"`

	ExpectFolder string `json:"expect_folder"` // movie or series folder
	ExpectSeason string `json:"expect_season,omitempty"`
	ExpectFile   string `json:"expect_file"` // movie file without extension; episode file with extension
	Ext          string `json:"ext"`
}

type goldenFile struct {
	Instance string          `json:"instance"`
	Naming   json.RawMessage `json:"naming"`
	Cases    []goldenCase    `json:"cases"`
}

type goldenResult struct {
	folder, season, file string
	err                  error
}

func runGolden(c *Config, gc *goldenCase) goldenResult {
	if gc.Movie != nil {
		f, err := MovieFileName(c, gc.Movie, gc.MFile)
		folder, _ := MovieFolder(c, gc.Movie)
		return goldenResult{folder: folder, file: f, err: err}
	}
	folder, _ := SeriesFolder(c, gc.Series)
	season, _ := SeasonFolder(c, gc.Series, gc.Eps[0].Season)
	sp := path.Join(gc.RootDir, folder, season)
	f, err := SeriesFileName(c, gc.Series, gc.Eps, gc.EFile, gc.Ext, sp)
	return goldenResult{folder: folder, season: season, file: f, err: err}
}

// renamedRE says whether a file name has the shape of the instance's naming format at all: a file the arr imported
// without renaming (or under an older format) keeps another shape, and is not the naming's output.
var (
	movieRenamed  = regexp.MustCompile(`\{imdb-tt\d+\}|\) - \[`)
	seriesRenamed = regexp.MustCompile(` - S\d{2,}E\d{2,}.* \[`)
)

type goldenStats struct {
	total, renamed, fileEqual, folderEqual, equal int
	diffs                                         map[string][]string
}

func (s *goldenStats) add(gc *goldenCase, r goldenResult) bool {
	s.total++
	if s.diffs == nil {
		s.diffs = map[string][]string{}
	}
	folderOK := r.folder == gc.ExpectFolder && (gc.Series == nil || r.season == gc.ExpectSeason)
	if folderOK {
		s.folderEqual++
	} else {
		s.diffs["folder"] = append(s.diffs["folder"], gc.Instance+": got  "+r.folder+"/"+r.season+"\n    want "+
			gc.ExpectFolder+"/"+gc.ExpectSeason)
	}
	re := movieRenamed
	if gc.Series != nil {
		re = seriesRenamed
	}
	if !re.MatchString(gc.ExpectFile) {
		return false
	}
	s.renamed++
	if r.err == nil && r.file == gc.ExpectFile {
		s.fileEqual++
		if folderOK {
			s.equal++
		}
		return folderOK
	}
	got := r.file
	if r.err != nil {
		got = r.err.Error()
	}
	s.diffs["file"] = append(s.diffs["file"], gc.Instance+": got  "+got+"\n    want "+gc.ExpectFile)
	return false
}

// TestGolden runs the committed sample of real arr names (testdata/golden). Every case must match: the sample holds
// only files the arr renamed and whose inputs are complete.
func TestGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if len(files) == 0 {
		t.Fatal("no golden fixtures")
	}
	for _, fn := range files {
		b, err := os.ReadFile(fn)
		if err != nil {
			t.Fatal(err)
		}
		var g goldenFile
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		c, err := ParseArrConfig(g.Naming)
		if err != nil {
			t.Fatal(err)
		}
		var st goldenStats
		for i := range g.Cases {
			gc := &g.Cases[i]
			st.add(gc, runGolden(&c, gc))
		}
		if st.renamed != st.total {
			t.Errorf("%s: %d cases are not in the naming format's shape", filepath.Base(fn), st.total-st.renamed)
		}
		for k, ds := range st.diffs {
			sort.Strings(ds)
			for _, d := range ds {
				t.Errorf("%s %s: %s", filepath.Base(fn), k, d)
			}
		}
		t.Logf("%s: %d cases, %d files equal, %d folders equal", filepath.Base(fn), st.total, st.fileEqual, st.folderEqual)
	}
}
