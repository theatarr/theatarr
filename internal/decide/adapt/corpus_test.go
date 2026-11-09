package adapt

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// TestCorpusInput compares, for every corpus title, the format input built from our port's parse with the one built
// from the arr's own parse answer (format.ParseResponse.Input): same release title, group, edition, quality, year,
// release type, media, and the aggregated languages in the arr's order. Opt-in: set THEATARR_CORPUS.
func TestCorpusInput(t *testing.T) {
	dir := os.Getenv("THEATARR_CORPUS")
	if dir == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	insts, err := format.Corpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range insts {
		var n, same, aggregated, shown int
		f, err := os.Open(filepath.Join(dir, inst.Name, "parse.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var line struct {
				Title  string          `json:"title"`
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			}
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				t.Fatal(err)
			}
			if line.Status != 200 {
				continue
			}
			var r format.ParseResponse
			if err := json.Unmarshal(line.Body, &r); err != nil {
				t.Fatal(err)
			}
			want, ok, err := r.Input(inst.Flavour)
			if err != nil || !ok {
				continue
			}
			got, parsed, err := ours(inst.Flavour, line.Title, line.Body)
			if err != nil {
				t.Errorf("%s %q: %v", inst.Name, line.Title, err)
				continue
			}
			n++
			if !reflect.DeepEqual(parsed, got.Languages) {
				aggregated++
			}
			if reflect.DeepEqual(got, want) {
				same++
			} else if shown++; shown <= 5 {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				t.Errorf("%s %q:\n ours %s\n arr  %s", inst.Name, line.Title, g, w)
			}
		}
		f.Close()
		t.Logf("%s: %d/%d inputs identical; aggregation changed the parsed languages on %d", inst.Name, same, n, aggregated)
	}
}

// ours parses title with the port and builds the input with the arr answer's library part. It also returns the
// parsed languages, to count how often the aggregation changes them.
func ours(fl format.Flavour, title string, body json.RawMessage) (*format.Input, []int, error) {
	var lib struct {
		Movie *struct {
			Title            string               `json:"title"`
			Year             int                  `json:"year"`
			OriginalLanguage *format.LanguageJSON `json:"originalLanguage"`
		} `json:"movie"`
		Series *struct {
			Year             int                  `json:"year"`
			OriginalLanguage *format.LanguageJSON `json:"originalLanguage"`
		} `json:"series"`
		Episodes []Episode `json:"episodes"`
	}
	if err := json.Unmarshal(body, &lib); err != nil {
		return nil, nil, err
	}
	id := func(l *format.LanguageJSON) int {
		if l == nil {
			return 0
		}
		return l.ID
	}
	if fl == format.Radarr {
		p := radarr.ParseMovieTitle(title, false)
		var m *Movie
		if lib.Movie != nil {
			m = &Movie{Title: lib.Movie.Title, Year: lib.Movie.Year, OriginalLanguage: id(lib.Movie.OriginalLanguage)}
		}
		in, err := Radarr(p, m)
		if p == nil {
			return in, nil, err
		}
		return in, radarrIDs(p.Languages), err
	}
	p := sonarr.ParseTitle(title)
	var s *Series
	if lib.Series != nil {
		s = &Series{Year: lib.Series.Year, OriginalLanguage: id(lib.Series.OriginalLanguage)}
	}
	in, err := Sonarr(p, s, lib.Episodes)
	if p == nil {
		return in, nil, err
	}
	return in, sonarrIDs(p.Languages), err
}
