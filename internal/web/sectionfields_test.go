package web

import (
	"bytes"
	"github.com/theatarr/theatarr/internal/settings/fielddocs"
	"testing"
)

func TestSectionFieldDocsCurrent(t *testing.T) {
	got, err := fielddocs.Generate("../policy")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sectionFieldDocs) {
		t.Fatal("policy help changed: run go generate ./internal/web")
	}
}
