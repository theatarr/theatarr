package adder

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyRouterImportsAdder keeps the debrid write capability behind the router and the torrent manager
// (docs/dev.md): no Go file, tests included, imports this package unless it is in internal/router (with routertest),
// internal/torrents (the user-chosen cleanup and restore) or this package itself.
// cmd/theatarr builds its adders with router.BuildAdders.
func TestOnlyRouterImportsAdder(t *testing.T) {
	const self = "github.com/theatarr/theatarr/internal/provider/adder"
	root := filepath.Join("..", "..", "..")
	allowed := []string{"internal/router/", "internal/torrents/", "internal/provider/adder/"}
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." || d.Name() == "testdata" || d.Name() == "vendor" ||
				d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		seen++
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if p != self {
				continue
			}
			ok := false
			for _, a := range allowed {
				ok = ok || strings.HasPrefix(rel, a)
			}
			if !ok {
				t.Errorf("%s imports internal/provider/adder; only internal/router may (use router.BuildAdders)", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 100 {
		t.Fatalf("walked only %d Go files; wrong root?", seen)
	}
}

// TestOnlyTorrentsDeletesChosen keeps the user-chosen delete and the restore magnet in the torrent manager: outside
// this package, only Go files in internal/torrents may name ChooseForDelete, UserChoice, UserDeleter,
// DeleteUserChosen or Restore of this package. The router imports the package for its adds and its two
// own deletes and must not reach these.
func TestOnlyTorrentsDeletesChosen(t *testing.T) {
	const self = "github.com/theatarr/theatarr/internal/provider/adder"
	guarded := map[string]bool{"ChooseForDelete": true, "UserChoice": true, "UserDeleter": true,
		"DeleteUserChosen": true, "Restore": true}
	root := filepath.Join("..", "..", "..")
	users := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." || d.Name() == "testdata" || d.Name() == "vendor" ||
				d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasPrefix(rel, "internal/provider/adder/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		names := map[string]bool{} // local names of the adder import
		for _, im := range f.Imports {
			if p, _ := strconv.Unquote(im.Path.Value); p == self {
				n := "adder"
				if im.Name != nil {
					n = im.Name.Name
				}
				names[n] = true
			}
		}
		// A method call through an interface value is a selector on anything: DeleteUserChosen is guarded by name
		// in every file; the other identifiers only as adder.<Name>.
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !guarded[sel.Sel.Name] {
				return true
			}
			x, _ := sel.X.(*ast.Ident)
			if sel.Sel.Name != "DeleteUserChosen" && (x == nil || !names[x.Name]) {
				return true
			}
			if strings.HasPrefix(rel, "internal/torrents/") {
				users++
				return true
			}
			t.Errorf("%s names %s; only internal/torrents may", rel, sel.Sel.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if users == 0 {
		t.Error("found no use in internal/torrents: the walk is not seeing the code")
	}
}
