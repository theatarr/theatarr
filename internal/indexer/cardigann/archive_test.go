// Port of Mediarium internal/indexers/definitions_test.go (the archive cases) @ f7d1e26
// (github.com/rdborg/Mediarium, AGPL-3.0, Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed
// in the package doc.

package cardigann

import (
	"strings"
	"testing"
)

func TestExtractDefinitionsPicksSupportedVersion(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"README.md":                          []byte("readme"),
		"definitions/v10/a.yml":              []byte("id: a\nname: Old\n"),
		"definitions/v11/a.yml":              []byte("id: a\nname: Current\n"),
		"definitions/v11/b.yaml":             []byte("id: b\n"),
		"definitions/v11/schema.json":        []byte("{}"),
		"definitions/v11/../../escape.yml":   []byte("id: escape\n"),
		"definitions/v11/.hidden.yml":        []byte("id: hidden\n"),
		"definitions/v12/a.yml":              []byte("id: a\nname: Future\n"),
		"definitions/v12/only-in-future.yml": []byte("id: f\n"),
	}
	for name, archive := range map[string][]byte{"tar.gz": tarGz(t, files), "zip": zipArchive(t, files)} {
		got, v, err := ExtractDefinitions(archive, 11)
		if err != nil || v != 11 {
			t.Fatalf("%s: v%d %v", name, v, err)
		}
		if len(got) != 2 || string(got["a"]) != "id: a\nname: Current\n" || got["b"] == nil {
			t.Errorf("%s: files = %v", name, keys(got))
		}
	}
}

// Only newer schemas: Mediarium fell back to the oldest; Theatarr refuses, since a newer schema may use features the
// engine does not know.
func TestExtractDefinitionsRefusesOnlyNewerVersions(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, map[string][]byte{
		"definitions/v12/a.yml": []byte("id: a"),
		"definitions/v13/a.yml": []byte("id: a"),
	})
	if _, _, err := ExtractDefinitions(archive, 11); err == nil || !strings.Contains(err.Error(), "v11 or older") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if _, _, err := ExtractDefinitions([]byte("<html>not an archive</html>"), 11); err == nil {
		t.Fatal("a web page is not an archive")
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
