package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

var (
	mdLink     = regexp.MustCompile(`\[[^\]\[]*\]\(([^)\s]+)\)`)
	mdCodeSpan = regexp.MustCompile("`[^`]*`")
	mdHeading  = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
	mdInline   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdHTMLID   = regexp.MustCompile(`<a (?:id|name)="([^"]+)">`)
)

// mdAnchors returns the heading anchors of a Markdown file as GitHub makes them: lower case, punctuation dropped,
// spaces to hyphens, and -1, -2, ... for a repeated heading. An explicit <a id="..."> counts too.
func mdAnchors(text string) map[string]bool {
	out, seen, fenced := map[string]bool{}, map[string]int{}, false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, id := range mdHTMLID.FindAllStringSubmatch(line, -1) {
			out[id[1]] = true
		}
		m := mdHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h := mdInline.ReplaceAllString(m[1], "$1")
		var b strings.Builder
		for _, r := range strings.ToLower(h) {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
				b.WriteRune(r)
			case r == ' ':
				b.WriteByte('-')
			}
		}
		slug := b.String()
		if n := seen[slug]; n > 0 {
			out[fmt.Sprintf("%s-%d", slug, n)] = true
		} else {
			out[slug] = true
		}
		seen[slug]++
	}
	return out
}

// TestDocsLinks: every relative Markdown link in README.md and docs/ names a file that exists, and its #anchor a
// heading of that file. Links to other sites are not fetched; code blocks and code spans are skipped.
func TestDocsLinks(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{filepath.Join(root, "README.md")}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("found %d Markdown files", len(files))
	}
	anchors := map[string]map[string]bool{}
	anchorsOf := func(p string) map[string]bool {
		if a, ok := anchors[p]; ok {
			return a
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		anchors[p] = mdAnchors(string(b))
		return anchors[p]
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fenced := false
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fenced = !fenced
				continue
			}
			if fenced {
				continue
			}
			for _, m := range mdLink.FindAllStringSubmatch(mdCodeSpan.ReplaceAllString(line, ""), -1) {
				target := m[1]
				if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				path, anchor, _ := strings.Cut(target, "#")
				dest := file
				if path != "" {
					dest = filepath.Join(filepath.Dir(file), filepath.FromSlash(path))
					if _, err := os.Stat(dest); err != nil {
						t.Errorf("%s:%d: link %q: no such file", file, i+1, target)
						continue
					}
				}
				if anchor != "" && strings.HasSuffix(dest, ".md") && !anchorsOf(dest)[anchor] {
					t.Errorf("%s:%d: link %q: no heading or id with that anchor", file, i+1, target)
				}
			}
		}
	}
}
