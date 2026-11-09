package anime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/offlinecache"
)

// ourKeys are fields the golden outputs don't have; the comparison ignores them.
var ourKeys = map[string]bool{"stages": true, "mapping_decision": true}

// normalize decodes JSON into generic values, dropping our own keys. Absent keys and null values are the same
// thing for the comparison (the golden outputs write some keys as null and leave others out).
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if ourKeys[k] || val == nil {
				continue
			}
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalize(x[i])
		}
		return out
	}
	return v
}

// diffJSON lists the differences between two normalized values, with JSON paths.
func diffJSON(path string, a, b any, out *[]string, limit int) {
	if len(*out) >= limit {
		return
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: got %s, golden %s", path, short(a), short(b)))
			return
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			diffJSON(path+"."+k, x[k], y[k], out, limit)
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: got %s, golden %s", path, short(a), short(b)))
			return
		}
		if len(x) != len(y) {
			*out = append(*out, fmt.Sprintf("%s: got %d items, golden %d: got %s, golden %s", path, len(x), len(y), short(a), short(b)))
			return
		}
		for i := range x {
			diffJSON(fmt.Sprintf("%s[%d]", path, i), x[i], y[i], out, limit)
		}
	default:
		if fmt.Sprint(a) != fmt.Sprint(b) {
			*out = append(*out, fmt.Sprintf("%s: got %s, golden %s", path, short(a), short(b)))
		}
	}
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}

func loadJSON(t *testing.T, p string, v any) bool {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return true
}

// buildAndCompare builds one franchise from a fixture directory (cache/, overrides/, community/, out/) and
// returns the differences from out/<slug>.json and out/<slug>.md.
func buildAndCompare(t *testing.T, src *anime.Sources, root string, f anime.Franchise) ([]string, *anime.Result) {
	t.Helper()
	var want map[string]any
	if !loadJSON(t, filepath.Join(root, "out", f.Slug+".json"), &want) {
		t.Fatalf("no expected output for %s", f.Slug)
	}
	ov, err := anime.LoadOverrides(filepath.Join(root, "overrides"), f.Slug)
	if err != nil {
		t.Fatal(err)
	}
	var com *anime.Community
	var c anime.Community
	if loadJSON(t, filepath.Join(root, "community", f.Slug+".json"), &c) {
		com = &c
	}
	res, err := anime.Build(context.Background(), src, f, anime.Options{Today: want["generated"].(string), Overrides: ov, Community: com})
	if err != nil {
		t.Fatalf("%s: %v", f.Slug, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("THEATARR_ANIME_DUMP"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, f.Slug+".json"), b, 0o644)
		_ = os.WriteFile(filepath.Join(dir, f.Slug+".md"), []byte(anime.Markdown(res, "theatarr anime", "overrides/"+f.Slug+".json")), 0o644)
	}
	var got any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var diffs []string
	diffJSON(f.Slug, normalize(got), normalize(want), &diffs, 40)
	if md, err := os.ReadFile(filepath.Join(root, "out", f.Slug+".md")); err == nil {
		mine := anime.Markdown(res, "theatarr anime", "overrides/"+f.Slug+".json")
		if mine != string(md) {
			diffs = append(diffs, f.Slug+".md: "+firstLineDiff(mine, string(md)))
		}
	}
	return diffs, res
}

func firstLineDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return fmt.Sprintf("line %d differs:\n  got:    %s\n  golden: %s", i+1, x, y)
		}
	}
	return "differs"
}

// TestGoldenFixture builds the franchises in testdata/offline from its offline cache and compares them with the
// golden outputs in testdata/offline/out.
func TestGoldenFixture(t *testing.T) {
	root := filepath.Join("testdata", "offline")
	src, _, err := offlinecache.Sources(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	var fs []anime.Franchise
	if !loadJSON(t, filepath.Join(root, "franchises.json"), &fs) || len(fs) == 0 {
		t.Fatal("no franchises in testdata/offline")
	}
	for _, f := range fs {
		t.Run(f.Slug, func(t *testing.T) {
			diffs, res := buildAndCompare(t, src, root, f)
			for _, d := range diffs {
				t.Error(d)
			}
			// a stored build reads back to the same JSON
			b, _ := json.Marshal(res)
			var back anime.Result
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			b2, _ := json.Marshal(&back)
			if string(b) != string(b2) {
				var x, y any
				_ = json.Unmarshal(b, &x)
				_ = json.Unmarshal(b2, &y)
				var d []string
				diffJSON(f.Slug, x, y, &d, 5)
				t.Errorf("round trip differs: %v", d)
			}
		})
	}
}

// TestExampleOverrides checks the policy examples: every overrides file parses and the franchise list loads.
func TestExampleOverrides(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "policy", "overrides")
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no overrides in %s: %v", dir, err)
	}
	for _, p := range files {
		slug := strings.TrimSuffix(filepath.Base(p), ".yaml")
		if slug == "README" || slug == "franchises" {
			continue
		}
		ov, err := anime.LoadOverrides(dir, slug)
		if err != nil || ov == nil || len(ov.Moves)+len(ov.Routes)+len(ov.AltCuts)+len(ov.Mappings) == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "franchises.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var fs []anime.Franchise
	if err := yaml.Unmarshal(b, &fs); err != nil || len(fs) != 12 {
		t.Errorf("franchises.yaml: %d franchises, %v", len(fs), err)
	}
}
