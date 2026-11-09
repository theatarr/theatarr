// Package fielddocs generates form help from policy source without building policy.
package fielddocs

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var Sections = strings.Fields("metadata library notify clients egress torrents cardigann shadow import playlists ui provider_sync router")

// Generate returns deterministic JSON and refuses undocumented reachable fields.
func Generate(dir string) ([]byte, error) {

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	// Parse source only, including all production files irrespective of build tags.
	// Preserve the AST: go/doc otherwise removes internal declarations.
	if _, err := doc.NewFromFiles(fset, files, "", doc.AllDecls|doc.PreserveAST); err != nil {
		return nil, err
	}
	types := map[string]*ast.StructType{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, s := range g.Specs {
				t, ok := s.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := t.Type.(*ast.StructType); ok {
					types[t.Name.Name] = st
				}
			}
		}
	}
	// Replace identifiers as whole words, including references to other fields.
	identifiers := map[string]string{}
	for _, st := range types {
		for _, f := range st.Fields.List {
			if name := yamlName(f); name != "" && len(f.Names) > 0 {
				identifiers[f.Names[0].Name] = name
			}
		}
	}
	names := make([]string, 0, len(identifiers))
	for name := range identifiers {
		names = append(names, regexp.QuoteMeta(name))
	}
	sort.Strings(names)
	identifier := regexp.MustCompile(`\b(?:` + strings.Join(names, "|") + `)\b`)
	out := map[string]map[string]string{}
	var missing []string
	var visit func(ast.Expr) error
	visit = func(e ast.Expr) error {
		switch t := e.(type) {
		case *ast.StarExpr:
			return visit(t.X)
		case *ast.ArrayType:
			return visit(t.Elt)
		case *ast.MapType:
			return visit(t.Value)
		case *ast.Ident:
			st := types[t.Name]
			if st == nil || out[t.Name] != nil {
				return nil
			}
			out[t.Name] = map[string]string{}
			for _, f := range st.Fields.List {
				name := yamlName(f)
				if name == "" {
					continue
				}
				help := strings.Join(strings.Fields(f.Doc.Text()), " ")
				if help == "" {
					help = strings.Join(strings.Fields(f.Comment.Text()), " ")
				}
				if help == "" {
					missing = append(missing, fmt.Sprintf("%s.%s (%s)", t.Name, f.Names[0].Name, name))
				}
				if strings.HasPrefix(help, f.Names[0].Name+" ") {
					help = "This" + strings.TrimPrefix(help, f.Names[0].Name)
				}
				help = identifier.ReplaceAllStringFunc(help, func(name string) string { return identifiers[name] })
				help = strings.ReplaceAll(help, "env vars", "environment variables")
				help = strings.ReplaceAll(help, "env var", "environment variable")
				if len(help) > 240 {
					cut := strings.LastIndex(help[:237], ". ")
					if cut > 40 {
						help = help[:cut+1]
					} else {
						cut = strings.LastIndex(help[:237], " ")
						help = help[:cut] + "…"
					}
				}
				out[t.Name][name] = help
				if err := visit(f.Type); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, f := range types["Policy"].Fields.List {
		for _, section := range Sections {
			if yamlName(f) == section {
				if err := visit(f.Type); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing docs: %s", strings.Join(missing, ", "))
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return append(b, '\n'), err
}
func yamlName(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}
	tag, _ := strconv.Unquote(f.Tag.Value)
	name, _, _ := strings.Cut(reflect.StructTag(tag).Get("yaml"), ",")
	if name == "-" {
		return ""
	}
	return name
}
