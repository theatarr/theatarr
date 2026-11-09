// Port of Mediarium internal/indexers/cardigann_corpus_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDefinitionCorpus validates a folder of Cardigann definitions and reports which ones the engine can't run. By
// default it checks the small hand-made set in testdata, which must all run: Prowlarr/Indexers carries no licence, so
// no snapshot of the real definitions is committed, and tests never fetch. Point THEATARR_CARDIGANN_DEFS at a local
// checkout's definitions/v11 to check the real list (it reads local files only and only reports):
//
//	THEATARR_CARDIGANN_DEFS=/path/to/definitions/v11 go test ./internal/indexer/cardigann -run Corpus -v
func TestDefinitionCorpus(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("THEATARR_CARDIGANN_DEFS")
	strict := dir == ""
	if strict {
		dir = "testdata"
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no definitions in %s: %v", dir, err)
	}
	reasons := map[string]int{}
	bad, noRSS := 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		def, err := ParseDefinition(data)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		p := def.Problems()
		if len(p) == 0 {
			p = renderProblems(def)
		}
		if len(p) == 0 {
			if r := RSSProblem(def); r != "" {
				noRSS++
				t.Logf("%s: no RSS: %s", def.ID, r)
			}
		}
		if len(p) > 0 {
			bad++
			t.Logf("%s: %s", def.ID, strings.Join(p, "; "))
			for _, r := range p {
				if i := strings.Index(r, ": "); i >= 0 {
					r = r[i+2:]
				}
				if len(r) > 60 {
					r = r[:60]
				}
				reasons[r]++
			}
		}
	}
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return reasons[keys[i]] > reasons[keys[j]] })
	for _, k := range keys {
		t.Logf("%4d  %s", reasons[k], k)
	}
	t.Logf("supported: %d of %d definitions; %d of them without RSS", len(files)-bad, len(files), noRSS)
	if strict && bad > 0 {
		t.Errorf("%d hand-made definitions do not run", bad)
	}
}

// renderProblems runs the definition's search templates with default
// settings and a sample query, catching errors that only show when a
// template is executed.
func renderProblems(def *Definition) []string {
	s, err := newSession(&Manager{}, def, Instance{Name: def.ID, BaseURL: firstLink(def)})
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, q := range []string{"", "the matrix 1999"} {
		vars := s.vars()
		vars.Query["Keywords"] = q
		kw, err := applyFilters(q, def.Search.KeywordsFilters, vars)
		if err != nil {
			out = append(out, "keywords: "+err.Error())
		}
		vars.Keywords = kw
		vars.Categories = def.siteCategories([]int{2000, 5000})
		for _, p := range def.Search.Paths {
			if _, err := applyTemplate(p.Path, vars, urlPathEscape); err != nil {
				out = append(out, "path: "+err.Error())
			}
			for _, k := range p.Inputs.Keys {
				if _, err := applyTemplate(p.Inputs.Values[k], vars, nil); err != nil {
					out = append(out, "input: "+err.Error())
				}
			}
		}
		for _, k := range def.Search.Inputs.Keys {
			if _, err := applyTemplate(def.Search.Inputs.Values[k], vars, nil); err != nil {
				out = append(out, "input: "+err.Error())
			}
		}
		sel, err := applyTemplate(def.Search.Rows.Selector, vars, nil)
		if err != nil {
			out = append(out, "rows: "+err.Error())
		} else if !def.jsonSearch() {
			if _, err := CompileSelector(sel); err != nil {
				out = append(out, "rows: "+err.Error())
			}
		}
	}
	return out
}

func firstLink(def *Definition) string {
	if len(def.Links) > 0 {
		return def.Links[0]
	}
	return "https://site.invalid/"
}
