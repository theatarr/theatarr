package sonarr

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

// TestCorpusAgreement compares ParseTitle with Sonarr's own parsedEpisodeInfo on the collected corpus
// It is opt-in: it skips unless THEATARR_CORPUS points at the corpus directory. It only
// reports; the agreement table belongs to the corpus check. THEATARR_CORPUS_STRICT=1 fails on any difference.
func TestCorpusAgreement(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("THEATARR_CORPUS")
	if dir == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	b, err := os.ReadFile(filepath.Join(dir, "instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	var instances []struct {
		Name    string `json:"name"`
		Flavour string `json:"flavour"`
	}
	if err := json.Unmarshal(b, &instances); err != nil {
		t.Fatal(err)
	}

	var total, agree int
	fieldDiffs := map[string]int{}
	var examples []string
	for _, inst := range instances {
		if inst.Flavour != "sonarr" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, inst.Name, "parse.jsonl"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		var n, ok int
		for sc.Scan() {
			var line struct {
				Title  string `json:"title"`
				Status int    `json:"status"`
				Body   struct {
					ParsedEpisodeInfo json.RawMessage `json:"parsedEpisodeInfo"`
				} `json:"body"`
			}
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				t.Fatalf("%s: %v", inst.Name, err)
			}
			if line.Status != 200 {
				continue
			}
			var want any
			if len(line.Body.ParsedEpisodeInfo) > 0 {
				_ = json.Unmarshal(line.Body.ParsedEpisodeInfo, &want)
			}
			gotJSON, _ := json.Marshal(ParseTitle(line.Title))
			var got any
			_ = json.Unmarshal(gotJSON, &got)
			n++
			diffs := diffJSON("", got, want)
			if len(diffs) == 0 {
				ok++
				continue
			}
			for _, d := range diffs {
				field, _, _ := strings.Cut(d, " ")
				fieldDiffs[field]++
			}
			if len(examples) < 40 {
				examples = append(examples, fmt.Sprintf("%s %q: %s", inst.Name, line.Title, strings.Join(diffs, "; ")))
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		t.Logf("%-20s %5d/%5d agree (%.2f%%)", inst.Name, ok, n, pct(ok, n))
		total += n
		agree += ok
	}
	fields := make([]string, 0, len(fieldDiffs))
	for k := range fieldDiffs {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	for _, k := range fields {
		t.Logf("differs: %-40s %d", k, fieldDiffs[k])
	}
	for _, e := range examples {
		t.Log(e)
	}
	t.Logf("TOTAL %d/%d agree (%.2f%%)", agree, total, pct(agree, total))
	if os.Getenv("THEATARR_CORPUS_STRICT") != "" && agree != total {
		t.Fail()
	}
}

// diffJSON lists the paths where got and want differ.
func diffJSON(path string, got, want any) []string {
	gm, gok := got.(map[string]any)
	wm, wok := want.(map[string]any)
	if gok && wok {
		keys := map[string]bool{}
		for k := range gm {
			keys[k] = true
		}
		for k := range wm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		var out []string
		for _, k := range sorted {
			p := k
			if path != "" {
				p = path + "." + k
			}
			out = append(out, diffJSON(p, gm[k], wm[k])...)
		}
		return out
	}
	if !reflect.DeepEqual(got, want) {
		if path == "" {
			path = "(whole)"
		}
		return []string{fmt.Sprintf("%s got %s want %s", path, show(got), show(want))}
	}
	return nil
}
