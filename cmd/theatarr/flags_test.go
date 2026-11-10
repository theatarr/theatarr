package main

import (
	"bytes"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// -library and -version set the same variable, the later one wins, and help lists both.
func TestLibraryFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-library", "movies"}, "movies"},
		{[]string{"-version", "movies"}, "movies"},
		{[]string{"-version=a", "-library=b"}, "b"},
		{[]string{"-library=a", "-version=b"}, "b"},
		{nil, ""},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		v := libraryFlag(fs, "library key from the policy")
		if err := fs.Parse(tc.args); err != nil || *v != tc.want {
			t.Errorf("%v: %q, %v; want %q", tc.args, *v, err, tc.want)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var help bytes.Buffer
	fs.SetOutput(&help)
	libraryFlag(fs, "library key from the policy")
	fs.PrintDefaults()
	for _, want := range []string{"-library string", "library key from the policy", "-version string", "same as -library"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("help lacks %q:\n%s", want, help.String())
		}
	}
}

// Each command that takes a library accepts it as -library and as -version, with the same result: the policy has
// no library "nope" and the command says so, or (journal, parallel compare, which only filter by it) the command
// stops on another argument after parsing. Neither spelling is an unknown flag or leaves the library unset.
func TestCommandsTakeLibraryAndVersion(t *testing.T) {
	pol := filepath.Join("..", "..", "examples", "policy")
	db := filepath.Join(t.TempDir(), "none.db")
	for name, tc := range map[string]struct {
		run  func(args []string) error
		args []string
		want string // part of the error, when the command names the library
	}{
		"decide": {func(a []string) error { return runDecide(a, io.Discard, nil) },
			[]string{"-policy", pol, "-db", db, "-title", "7", "-search"}, `no library "nope"`},
		"decide shadow": {func(a []string) error { return runDecideShadow(a, io.Discard) },
			[]string{"-policy", pol, "-db", db, "-instance", "radarr"}, `no library "nope"`},
		"golive check": {func(a []string) error { return runGolive(a, io.Discard) },
			[]string{"check", "-policy", pol, "-db", "none"}, `no library "nope"`},
		"journal": {func(a []string) error { return runJournal(a, io.Discard) },
			[]string{"-policy", pol, "-db", db, "-since", "not-a-time"}, ""},
		"library adopt": {func(a []string) error { return runLibrary(a, io.Discard) },
			[]string{"adopt", "-policy", pol, "-db", db, "-dry-run"}, `"nope"`},
		"parallel compare": {func(a []string) error { return runParallel(a, io.Discard) },
			[]string{"compare", "-policy", pol, "-db", db, "-since", "nonsense"}, ""},
		"router dry-run": {func(a []string) error { return runRouterDryRun(a, io.Discard, nil) },
			[]string{"-policy", pol, "-db", db, "-title", "7"}, `no library "nope"`},
	} {
		var got [2]string
		for i, flagName := range []string{"-library", "-version"} {
			args := commandSettingsArgs(t, append(append([]string(nil), tc.args...), flagName, "nope"))
			err := tc.run(args)
			if err == nil {
				t.Errorf("%s %s nope: no error", name, flagName)
				continue
			}
			got[i] = err.Error()
			if strings.Contains(got[i], "flag provided but not defined") || strings.Contains(got[i], "required") ||
				!strings.Contains(got[i], tc.want) {
				t.Errorf("%s %s nope: %v", name, flagName, err)
			}
		}
		if got[0] != got[1] {
			t.Errorf("%s: -library gave %q, -version gave %q", name, got[0], got[1])
		}
	}
}
