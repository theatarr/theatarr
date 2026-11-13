package format

// The agreement check: our formats and scores against the arrs' own, on a corpus collected from arr instances
// (<dir>/<instance>/snapshot/<endpoint>.json per instance).
//
// Parse check. For each parse.jsonl line, the Input is rebuilt from the arr's own parse (see arrparse.go), the
// instance's snapshot/customformat.json is evaluated, and the matched names are compared with the response's
// customFormats. When the title mapped to a movie or series, the score is computed with its quality profile from
// snapshot/qualityprofile.json and compared with customFormatScore.
//
// History check. Each grabbed history record carries customFormats and customFormatScore, which the arr's
// HistoryController computes when serving it: ParseCustomFormat(history, movie/series) re-parses SourceTitle for the
// release group, edition and simplified title, but takes quality and languages from the record, size and
// indexer flags (and Sonarr's release type) from its data, and Radarr's year from the movie. We take the re-parse
// from parse.jsonl (same title) and the movie or series from that parse when it is the record's own; records
// without both are skipped.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Instance is one arr's formats and profiles from a corpus snapshot.
type Instance struct {
	Name     string
	Flavour  Flavour
	Formats  []*Format
	Profiles map[int]*Profile
}

// LoadInstance reads <dir>/snapshot/{customformat,qualityprofile}.json.
func LoadInstance(dir, name string, fl Flavour) (*Instance, error) {
	b, err := os.ReadFile(filepath.Join(dir, "snapshot", "customformat.json"))
	if err != nil {
		return nil, err
	}
	fs, err := ParseArrFormats(fl, b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "snapshot", "qualityprofile.json"))
	if err != nil {
		return nil, err
	}
	ps, err := ParseArrProfiles(fl, b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	inst := &Instance{Name: name, Flavour: fl, Formats: fs, Profiles: map[int]*Profile{}}
	for _, p := range ps {
		inst.Profiles[p.ID] = p
	}
	return inst, nil
}

// Diff is one title on which we disagree with the arr.
type Diff struct {
	Instance  string   `json:"instance"`
	Check     string   `json:"check"` // "parse" or "history"
	Title     string   `json:"title"`
	ArrOnly   []string `json:"arrOnly,omitempty"`
	OursOnly  []string `json:"oursOnly,omitempty"`
	ArrScore  *int     `json:"arrScore,omitempty"`
	OurScore  *int     `json:"ourScore,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	Detail    []string `json:"detail,omitempty"` // per-condition results of the formats that differ
	InputJSON *Input   `json:"input,omitempty"`
}

// Tally counts agreement for one check of one instance.
type Tally struct {
	Titles      int `json:"titles"`      // lines read
	Compared    int `json:"compared"`    // titles whose formats were compared
	NamesAgree  int `json:"namesAgree"`  // identical matched-name sets
	Scored      int `json:"scored"`      // titles with an arr score to compare
	ScoresAgree int `json:"scoresAgree"` // identical scores
	Skipped     int `json:"skipped"`
}

// FormatTally counts one format's agreement over compared titles.
type FormatTally struct {
	Both     int `json:"both"`
	ArrOnly  int `json:"arrOnly"`
	OursOnly int `json:"oursOnly"`
}

// Result is the check of one instance.
type Result struct {
	Instance string                  `json:"instance"`
	Flavour  Flavour                 `json:"flavour"`
	Parse    Tally                   `json:"parse"`
	History  Tally                   `json:"history"`
	Formats  map[string]*FormatTally `json:"formats"` // parse check only
	Diffs    []Diff                  `json:"diffs"`
}

type parseLine struct {
	Title  string          `json:"title"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// CheckInstance runs both checks on one corpus instance directory.
func CheckInstance(dir, name string, fl Flavour) (*Result, error) {
	inst, err := LoadInstance(dir, name, fl)
	if err != nil {
		return nil, err
	}
	return inst.Check(dir)
}

// Check runs both checks of the corpus instance directory dir against inst's formats and profiles (by arr profile
// id), which need not be the snapshot's: a converted policy is checked this way (internal/recyclarr).
func (inst *Instance) Check(dir string) (*Result, error) {
	name, fl := inst.Name, inst.Flavour
	res := &Result{Instance: name, Flavour: fl, Formats: map[string]*FormatTally{}}
	parses := map[string]*ParseResponse{}

	err := eachLine(filepath.Join(dir, "parse.jsonl"), func(b []byte) error {
		var pl parseLine
		if err := json.Unmarshal(b, &pl); err != nil {
			return err
		}
		res.Parse.Titles++
		if pl.Status != 200 || len(pl.Body) == 0 {
			res.Parse.Skipped++
			return nil
		}
		var r ParseResponse
		if err := json.Unmarshal(pl.Body, &r); err != nil {
			return fmt.Errorf("%s: %w", pl.Title, err)
		}
		parses[pl.Title] = &r
		in, ok, err := r.Input(fl)
		if err != nil {
			return fmt.Errorf("%s: %w", pl.Title, err)
		}
		if !ok {
			res.Parse.Skipped++
			return nil
		}
		var arrScore *int
		if r.Mapped() {
			arrScore = r.CustomFormatScore
		}
		inst.compare(res, &res.Parse, "parse", pl.Title, in, r.CustomFormats, arrScore, r.QualityProfileID(), true)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s parse.jsonl: %w", name, err)
	}

	err = eachLine(filepath.Join(dir, "history.jsonl"), func(b []byte) error {
		var h historyRecord
		if err := json.Unmarshal(b, &h); err != nil {
			return err
		}
		res.History.Titles++
		r := parses[h.SourceTitle]
		in, profile, ok := h.input(fl, r)
		if !ok {
			res.History.Skipped++
			return nil
		}
		inst.compare(res, &res.History, "history", h.SourceTitle, in, h.CustomFormats, h.CustomFormatScore, profile, false)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("%s history.jsonl: %w", name, err)
	}
	return res, nil
}

func (inst *Instance) compare(res *Result, t *Tally, check, title string, in *Input, arr []FormatRefJSON,
	arrScore *int, profileID int, perFormat bool) {
	got, err := Calculate(inst.Formats, in)
	t.Compared++
	ours := map[string]bool{}
	for _, f := range got {
		ours[f.Name] = true
	}
	theirs := map[string]bool{}
	for _, f := range arr {
		theirs[f.Name] = true
	}
	d := Diff{Instance: inst.Name, Check: check, Title: title}
	for n := range theirs {
		if !ours[n] {
			d.ArrOnly = append(d.ArrOnly, n)
		}
	}
	for n := range ours {
		if !theirs[n] {
			d.OursOnly = append(d.OursOnly, n)
		}
	}
	sort.Strings(d.ArrOnly)
	sort.Strings(d.OursOnly)
	if perFormat {
		for n := range union(ours, theirs) {
			ft := res.Formats[n]
			if ft == nil {
				ft = &FormatTally{}
				res.Formats[n] = ft
			}
			switch {
			case ours[n] && theirs[n]:
				ft.Both++
			case theirs[n]:
				ft.ArrOnly++
			default:
				ft.OursOnly++
			}
		}
	}
	namesOK := len(d.ArrOnly) == 0 && len(d.OursOnly) == 0
	if namesOK {
		t.NamesAgree++
	}
	scoreOK := true
	if arrScore != nil {
		if p := inst.Profiles[profileID]; p != nil {
			t.Scored++
			s := p.Score(got)
			if s == *arrScore {
				t.ScoresAgree++
			} else {
				scoreOK = false
				d.ArrScore, d.OurScore = arrScore, &s
			}
		}
	}
	if err != nil {
		d.Errors = strings.Split(err.Error(), "\n")
	}
	if namesOK && scoreOK && err == nil {
		return
	}
	for _, f := range inst.Formats {
		if theirs[f.Name] != ours[f.Name] {
			d.Detail = append(d.Detail, explain(f, in))
		}
	}
	d.InputJSON = in
	res.Diffs = append(res.Diffs, d)
}

// explain renders a format's evaluation: each group with its conditions' results.
func explain(f *Format, in *Input) string {
	ev := f.Evaluate(in)
	var b strings.Builder
	fmt.Fprintf(&b, "%s=%v:", f.Name, ev.Matched)
	for _, g := range ev.Groups {
		fmt.Fprintf(&b, " [%s %v", strings.TrimSuffix(g.Implementation, "Specification"), g.DidMatch())
		for i, s := range g.Specs {
			flags := ""
			if s.Negate {
				flags += "!"
			}
			if s.Required {
				flags += "*"
			}
			fmt.Fprintf(&b, " %s%s=%v", flags, s.Name, g.Results[i])
		}
		b.WriteString("]")
	}
	return b.String()
}

func union(a, b map[string]bool) map[string]bool {
	u := map[string]bool{}
	for k := range a {
		u[k] = true
	}
	for k := range b {
		u[k] = true
	}
	return u
}

type historyRecord struct {
	SourceTitle       string            `json:"sourceTitle"`
	EventType         string            `json:"eventType"`
	MovieID           int               `json:"movieId"`
	SeriesID          int               `json:"seriesId"`
	Languages         []LanguageJSON    `json:"languages"`
	Quality           *qualityJSON      `json:"quality"`
	Data              map[string]string `json:"data"`
	CustomFormats     []FormatRefJSON   `json:"customFormats"`
	CustomFormatScore *int              `json:"customFormatScore"`
}

type qualityJSON struct {
	Quality struct {
		Source     any `json:"source"`
		Resolution int `json:"resolution"`
		Modifier   any `json:"modifier"`
	} `json:"quality"`
}

// input rebuilds what CustomFormatCalculationService.ParseCustomFormat(history, movie/series) uses. It needs the
// arr's parse of the same title, mapped to the record's own movie or series.
func (h *historyRecord) input(fl Flavour, r *ParseResponse) (*Input, int, bool) {
	if r == nil || h.CustomFormats == nil {
		return nil, 0, false
	}
	m := r.media()
	if m == nil || m.ID == 0 || (fl == Radarr && m.ID != h.MovieID) || (fl == Sonarr && m.ID != h.SeriesID) {
		return nil, 0, false
	}
	pi := r.ParsedMovieInfo
	if fl == Sonarr {
		pi = r.ParsedEpisodeInfo
	}
	if pi == nil { // the arr would fall back to SimplifyReleaseTitle, which we do not port here
		return nil, 0, false
	}
	in := &Input{Flavour: fl, ReleaseGroup: pi.ReleaseGroup, Media: &Media{Year: m.Year}}
	if m.OriginalLanguage != nil {
		in.Media.OriginalLanguage = m.OriginalLanguage.ID
	}
	if fl == Radarr {
		in.ReleaseTitle = pi.SimpleReleaseTitle
		in.Edition = pi.Edition
		in.Year = m.Year // MovieInfo.Year = movie.Year
	} else {
		in.ReleaseTitle = pi.ReleaseTitle
		if rt, ok := h.Data["releaseType"]; ok {
			in.ReleaseType, _ = sonarrReleaseTypes.exact(rt)
		}
	}
	if h.Quality != nil {
		in.Resolution = h.Quality.Quality.Resolution
		if h.Quality.Quality.Source != nil {
			in.Source, _ = enumAny(h.Quality.Quality.Source, fl.SourceValue)
		}
		if fl == Radarr && h.Quality.Quality.Modifier != nil {
			in.Modifier, _ = enumAny(h.Quality.Quality.Modifier, ModifierValue)
		}
	}
	if h.Languages != nil {
		in.Languages = make([]int, len(h.Languages))
		for i, l := range h.Languages {
			in.Languages[i] = l.ID
		}
	}
	in.Size, _ = strconv.ParseInt(h.Data["size"], 10, 64) // long.TryParse
	in.IndexerFlags, _ = fl.IndexerFlagsValue(h.Data["indexerFlags"])
	return in, m.QualityProfileID, true
}

// exact is Enum.TryParse without ignoreCase: a number or an exact name.
func (t enumTable) exact(s string) (int, bool) {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v, true
	}
	for i, n := range t.names {
		if n == strings.TrimSpace(s) {
			return t.values[i], true
		}
	}
	return 0, false
}

func eachLine(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
	}
	return sc.Err()
}

// Corpus lists the instances of a corpus directory from its instances.json.
func Corpus(dir string) ([]struct {
	Name    string  `json:"name"`
	Flavour Flavour `json:"flavour"`
}, error) {
	var list []struct {
		Name    string  `json:"name"`
		Flavour Flavour `json:"flavour"`
	}
	err := readJSON(filepath.Join(dir, "instances.json"), &list)
	return list, err
}
