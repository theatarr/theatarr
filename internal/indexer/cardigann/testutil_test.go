// Port of Mediarium internal/indexers/cardigann_testutil_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// fixtureDef reads a test definition from testdata.
func fixtureDef(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// tarGz builds a .tar.gz shaped like a GitHub archive of the definitions repository: files maps paths below
// "Indexers-master/" to contents.
func tarGz(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "Indexers-master/" + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipArchive(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create("Indexers-master/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// memDefs is an in-memory definition source for tests.
type memDefs map[string][]byte

func (d memDefs) Load(_ context.Context, id string) (*Definition, string, error) {
	data, ok := d[id]
	if !ok {
		return nil, "", fmt.Errorf("definition %q not found", id)
	}
	def, err := ParseDefinition(data)
	if err != nil {
		return nil, "", err
	}
	if err := def.Validate(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return def, hex.EncodeToString(sum[:]), nil
}

// fixtureManager returns a manager holding the named fixture definitions from testdata, with no polite delay.
func fixtureManager(t testing.TB, names ...string) *Manager {
	t.Helper()
	defs := memDefs{}
	for _, n := range names {
		defs[n] = fixtureDef(t, n)
	}
	m := NewManager(defs, nil)
	m.DefaultDelay = 0
	return m
}

// direct is the test transport: the real network stack against httptest servers (production uses the gateway).
var direct http.RoundTripper = http.DefaultTransport
