package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestEnvironmentNames: every built-in environment name the service reads is a THEATARR_ name. In non-test Go, an
// upper-case name literal passed to an env helper (os.Getenv, os.LookupEnv, envOr, envInt, ...), held by a constant or
// variable named for an env name or prefix, given as the default of an -...-env flag, or quoted as "env NAME" in a
// flag's help, starts with THEATARR_; the system variables in systemEnv are the only exceptions. The image's runtime
// ENV defaults use THEATARR_ names too. Names an operator configures (api_key_env, ...) are not literals and not
// checked here.
func TestEnvironmentNames(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	systemEnv := map[string]bool{"USER": true}
	nameShape := regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*_?$`)
	envHelper := regexp.MustCompile(`(?i)env`)
	envHolder := regexp.MustCompile(`(?i)(env|vars?|prefix|sentinel)$`)
	helpEnv := regexp.MustCompile(`(?:^|[\s(])env ([A-Z][A-Z0-9_]*)`)
	n, names := 0, 0
	check := func(path, s string) {
		if !nameShape.MatchString(s) {
			return
		}
		names++
		if !strings.HasPrefix(s, "THEATARR_") && !systemEnv[s] {
			t.Errorf("%s: environment name %q is not a THEATARR_ name", path, s)
		}
	}
	lits := func(path string, nodes ...ast.Node) {
		for _, node := range nodes {
			ast.Inspect(node, func(x ast.Node) bool {
				if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						check(path, s)
					}
				}
				return true
			})
		}
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		n++
		ast.Inspect(f, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.CallExpr:
				callee := ""
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					callee = fn.Name
				case *ast.SelectorExpr:
					callee = fn.Sel.Name
				}
				if envHelper.MatchString(callee) {
					for _, a := range x.Args {
						if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							s, _ := strconv.Unquote(lit.Value)
							check(path, s)
						}
					}
				}
				// fs.String("api-key-env", "NAME", ...): the default of a flag naming an env variable.
				if len(x.Args) >= 2 {
					if flag, ok := x.Args[0].(*ast.BasicLit); ok && flag.Kind == token.STRING && strings.HasSuffix(flag.Value, `-env"`) {
						lits(path, x.Args[1])
					}
				}
			case *ast.ValueSpec:
				for i, id := range x.Names {
					if envHolder.MatchString(id.Name) && i < len(x.Values) {
						lits(path, x.Values[i])
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && envHolder.MatchString(id.Name) && i < len(x.Rhs) {
						lits(path, x.Rhs[i])
					}
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if s, err := strconv.Unquote(x.Value); err == nil {
						for _, m := range helpEnv.FindAllStringSubmatch(s, -1) {
							check(path, m[1])
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("only %d Go files read", n)
	}
	if names < 40 {
		t.Fatalf("only %d environment names found", names)
	}

	b, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	final := string(b)[strings.LastIndex(string(b), "\nFROM "):] // the runtime stage
	m := regexp.MustCompile(`(?m)^(?:ENV\s+|\s+)([A-Z][A-Z0-9_]*)=`).FindAllStringSubmatch(final, -1)
	if len(m) == 0 {
		t.Fatal("no ENV defaults in the Dockerfile's runtime stage")
	}
	for _, v := range m {
		if !strings.HasPrefix(v[1], "THEATARR_") {
			t.Errorf("Dockerfile ENV %s is not a THEATARR_ name", v[1])
		}
	}
}
