// Package parsertest runs the arr ParserTests cases extracted to JSON (testdata/parsertests in the radarr and
// sonarr parser packages) against any parser that can answer the named calls.
//
// A case file is {"source", "fixture", "cases": [...]}. A case has:
//   - call: the operation, answered by a Caller;
//   - input, or inputs: every input must meet the expectations;
//   - args: extra call arguments;
//   - expect: {path: value}, the value at path equals value (null also matches a missing field);
//   - contains: {path: [values]}, each value occurs in the list at path;
//   - not: {path: value}, the value at path differs from value;
//   - compare: a function applied to both sides of every expect before comparing (cleanSeriesTitle);
//   - known_failure: the reason a case is expected to fail.
//
// Paths are dot separated: "" is the whole value, a number indexes an array, "#" is an array's length and "#.key"
// projects key over an array's elements.
package parsertest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// File is one fixture's case file.
type File struct {
	Source  string `json:"source"`
	Fixture string `json:"fixture"`
	Cases   []Case `json:"cases"`
}

// Case is one extracted test case.
type Case struct {
	ID           string           `json:"id"`
	Line         int              `json:"line"`
	Call         string           `json:"call"`
	Input        *string          `json:"input"`
	Inputs       []string         `json:"inputs"`
	Args         map[string]any   `json:"args"`
	Expect       map[string]any   `json:"expect"`
	Contains     map[string][]any `json:"contains"`
	Not          map[string]any   `json:"not"`
	Compare      string           `json:"compare"`
	KnownFailure string           `json:"known_failure"`
}

// Caller answers the calls a case names.
type Caller interface {
	// Call runs one named operation and returns its result as decoded JSON (map[string]any, []any, string,
	// float64, bool or nil for a .NET null). An operation the parser cannot answer returns ErrNotApplicable.
	Call(name, input string, args map[string]any) (any, error)
	// Compare applies the named compare function to one side of an expectation.
	Compare(name string, v any) (any, error)
}

// ErrNotApplicable is returned (possibly wrapped) by a Caller for a call or compare function it does not answer.
var ErrNotApplicable = errors.New("not applicable")

// Load reads every *.json case file in dir, sorted by file name.
func Load(dir string) ([]File, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no case files in %s", dir)
	}
	sort.Strings(paths)
	out := make([]File, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var f File
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// MustLoad is Load for tests.
func MustLoad(t testing.TB, dir string) []File {
	t.Helper()
	files, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// RunCase runs one case: nil when it passes, an error saying what differs when it fails. An error wrapping
// ErrNotApplicable means the Caller does not answer the case's call or compare function.
func RunCase(c Caller, tc Case) error {
	inputs := tc.Inputs
	if tc.Input != nil {
		inputs = []string{*tc.Input}
	}
	if len(inputs) == 0 {
		return fmt.Errorf("case has no input")
	}
	for _, in := range inputs {
		got, err := c.Call(tc.Call, in, tc.Args)
		if err != nil {
			return err
		}
		if err := Check(c, tc, got); err != nil {
			if len(inputs) > 1 {
				return fmt.Errorf("input %q: %w", in, err)
			}
			return err
		}
	}
	return nil
}

// Check applies the case's expect, contains and not to one result.
func Check(c Caller, tc Case, got any) error {
	for _, k := range sortedKeys(tc.Expect) {
		want := tc.Expect[k]
		have, _ := Lookup(got, k)
		if tc.Compare != "" {
			var err error
			if have, err = c.Compare(tc.Compare, have); err != nil {
				return err
			}
			if want, err = c.Compare(tc.Compare, want); err != nil {
				return err
			}
		}
		if !Equal(have, want) {
			return fmt.Errorf("%q: got %s, want %s", k, Show(have), Show(want))
		}
	}
	containsKeys := make([]string, 0, len(tc.Contains))
	for k := range tc.Contains {
		containsKeys = append(containsKeys, k)
	}
	sort.Strings(containsKeys)
	for _, k := range containsKeys {
		have, _ := Lookup(got, k)
		list, _ := have.([]any)
		for _, w := range tc.Contains[k] {
			found := false
			for _, h := range list {
				if Equal(h, w) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%q: %s does not contain %s", k, Show(have), Show(w))
			}
		}
	}
	for _, k := range sortedKeys(tc.Not) {
		have, _ := Lookup(got, k)
		if Equal(have, tc.Not[k]) {
			return fmt.Errorf("%q: got %s, want anything else", k, Show(have))
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Lookup resolves a dot path in a decoded JSON value. "" is the value itself, a number indexes an array, "#" is
// an array's length and "#.key" projects key over an array's elements.
func Lookup(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	parts := strings.Split(path, ".")
	for i, p := range parts {
		switch cur := v.(type) {
		case map[string]any:
			x, ok := cur[p]
			if !ok {
				return nil, false
			}
			v = x
		case []any:
			if p == "#" {
				if i == len(parts)-1 {
					return float64(len(cur)), true
				}
				rest := strings.Join(parts[i+1:], ".")
				out := make([]any, 0, len(cur))
				for _, e := range cur {
					if x, ok := Lookup(e, rest); ok {
						out = append(out, x)
					}
				}
				return out, true
			}
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n >= len(cur) {
				return nil, false
			}
			v = cur[n]
		default:
			return nil, false
		}
	}
	return v, true
}

// Equal compares two values as JSON: both are marshalled and decoded before a deep comparison, so typed Go values
// and decoded JSON compare alike.
func Equal(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// Show renders a value as compact JSON for messages.
func Show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ToJSON round-trips a typed result through encoding/json, giving the decoded form a Caller returns.
func ToJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}
