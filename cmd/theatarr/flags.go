package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

// libraryFlag registers -library and its older spelling -version on one variable. Settings keys and API fields
// call a library a version; the CLI takes both names. Given both, the later one on the command line wins.
func libraryFlag(fs *flag.FlagSet, usage string) *string {
	v := new(string)
	fs.StringVar(v, "library", "", usage)
	fs.StringVar(v, "version", "", "same as -library")
	return v
}

// loadEnvFile sets KEY=VALUE lines from path into the environment, leaving variables that are already set. Values
// are never printed.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("%s:%d: not KEY=VALUE", path, line)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}
