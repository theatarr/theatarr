package radarr

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestCorpus compares ParseMovieTitle with the parsedMovieInfo Radarr itself returned for real
// release titles: the radarr instances' parse.jsonl in a collected corpus. It is
// opt-in: it skips unless THEATARR_CORPUS points at the corpus directory. A mismatch is logged, not
// failed, since the corpus may hold titles the port is known to differ on; the agreement rate
// per instance and the fields that differ are in the log (go test -v -run TestCorpus).
// THEATARR_CORPUS_STRICT=1 turns mismatches into failures.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("THEATARR_CORPUS")
	if dir == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	var instances []struct {
		Name    string `json:"name"`
		Flavour string `json:"flavour"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &instances); err != nil {
		t.Fatal(err)
	}
	strict := os.Getenv("THEATARR_CORPUS_STRICT") == "1"
	var report []string
	for _, inst := range instances {
		if inst.Flavour != "radarr" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, inst.Name, "parse.jsonl"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		total, same := 0, 0
		fieldDiffs := map[string]int{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var line struct {
				Title  string `json:"title"`
				Status int    `json:"status"`
				Body   struct {
					ParsedMovieInfo json.RawMessage `json:"parsedMovieInfo"`
				} `json:"body"`
			}
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				t.Fatalf("%s: %v", inst.Name, err)
			}
			if line.Status != 200 {
				continue
			}
			var want any
			if len(line.Body.ParsedMovieInfo) > 0 {
				_ = json.Unmarshal(line.Body.ParsedMovieInfo, &want)
			}
			got, err := Call("parseMovieTitle", line.Title, nil)
			if err != nil {
				t.Fatal(err)
			}
			total++
			diffs := diffFields(want, got)
			if len(diffs) == 0 {
				same++
				continue
			}
			for _, d := range diffs {
				fieldDiffs[d]++
			}
			msg := fmt.Sprintf("%s: %q differs in %s", inst.Name, line.Title, strings.Join(diffs, ", "))
			for _, d := range diffs {
				w, _ := lookup(want, d)
				g, _ := lookup(got, d)
				msg += fmt.Sprintf("\n    %s: arr %s, port %s", d, show(w), show(g))
			}
			if strict {
				t.Error(msg)
			} else {
				t.Log(msg)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		fields := make([]string, 0, len(fieldDiffs))
		for k, v := range fieldDiffs {
			fields = append(fields, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(fields)
		report = append(report, fmt.Sprintf("%-16s %5d/%5d  %6.2f%%  %s", inst.Name, same, total, pct(same, total),
			strings.Join(fields, ", ")))
	}
	t.Log("agreement with GET /api/v3/parse:\n" + strings.Join(report, "\n"))
}

// diffFields lists the top-level parsedMovieInfo fields that differ (null and absent are equal).
func diffFields(want, got any) []string {
	if want == nil || got == nil {
		if want == nil && got == nil {
			return nil
		}
		return []string{""}
	}
	w, _ := want.(map[string]any)
	g, _ := got.(map[string]any)
	keys := map[string]bool{}
	for k := range w {
		keys[k] = true
	}
	for k := range g {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if !reflect.DeepEqual(w[k], g[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
