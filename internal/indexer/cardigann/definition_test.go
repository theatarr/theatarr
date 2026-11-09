// Port of Mediarium internal/indexers/cardigann_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann_test

import (
	"testing"

	"github.com/theatarr/theatarr/internal/indexer/cardigann"
)

const fixtureDefinition = `
id: fixtureindexer
name: Fixture Indexer
type: private
links:
  - https://fixture.test/
caps:
  categories:
    2000: Movies
    2010: Movies/SD
`

func TestParseDefinition(t *testing.T) {
	t.Parallel()
	def, err := cardigann.ParseDefinition([]byte(fixtureDefinition))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	if def.ID != "fixtureindexer" {
		t.Errorf("unexpected id: %s", def.ID)
	}
	if def.Name != "Fixture Indexer" {
		t.Errorf("unexpected name: %s", def.Name)
	}
	if def.Type != "private" {
		t.Errorf("unexpected type: %s", def.Type)
	}
	if len(def.Links) != 1 || def.Links[0] != "https://fixture.test/" {
		t.Errorf("unexpected links: %v", def.Links)
	}
	if def.Caps.Categories["2000"] != "Movies" {
		t.Errorf("unexpected categories: %v", def.Caps.Categories)
	}
}

func TestParseDefinitionMissingID(t *testing.T) {
	t.Parallel()
	_, err := cardigann.ParseDefinition([]byte(`name: No ID Here`))
	if err == nil {
		t.Fatal("expected error for definition missing id")
	}
}
