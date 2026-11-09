package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theatarr anime build runs offline from the golden fixture's offline cache, saves, and theatarr anime review lists the queue.
func TestAnimeBuildOffline(t *testing.T) {
	dir := t.TempDir()
	stdout := os.Stdout
	f, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = f
	defer func() { os.Stdout = stdout }()
	cache := filepath.Join("..", "..", "internal", "metadata", "anime", "testdata", "offline", "cache")
	db := filepath.Join(dir, "theatarr.db")
	if err := runAnime([]string{"build", "-franchise", "mushoku-tensei", "-cache", cache, "-today", "2026-10-25",
		"-out", dir, "-db", db, "-save"}); err != nil {
		t.Fatal(err)
	}
	if err := runAnime([]string{"review", "-db", db}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, _ := os.ReadFile(filepath.Join(dir, "stdout"))
	md, _ := os.ReadFile(filepath.Join(dir, "mushoku-tensei.md"))
	for _, want := range []string{"## Automatic watch order", "## Story order (candidate)", "## Mapping decisions",
		"| conflict | `al:146065` |", "mushoku-tensei       open"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if !strings.HasPrefix(string(md), "# Mushoku Tensei: watch order and arr mapping") {
		t.Errorf("md file: %.80s", md)
	}
	if err := runAnime([]string{"build", "-franchise", "nope", "-cache", cache}); err == nil {
		t.Error("unknown franchise accepted")
	}
	if err := runAnime([]string{"build", "-franchise", "mushoku-tensei"}); err == nil {
		t.Error("a live build without a policy was accepted")
	}
}
