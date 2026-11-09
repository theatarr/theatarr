package format

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCorpus runs the agreement check on the local corpus. Opt-in: set THEATARR_CORPUS to the corpus directory.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("THEATARR_CORPUS")
	if dir == "" {
		t.Skip("THEATARR_CORPUS not set")
	}
	insts, err := Corpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	var compared, agree int
	for _, i := range insts {
		d := filepath.Join(dir, i.Name)
		if _, err := os.Stat(filepath.Join(d, "parse.jsonl")); err != nil {
			t.Logf("%s: no parse.jsonl", i.Name)
			continue
		}
		r, err := CheckInstance(d, i.Name, i.Flavour)
		if err != nil {
			t.Fatal(err)
		}
		compared += r.Parse.Compared
		agree += r.Parse.NamesAgree
		t.Logf("%s: names %d/%d, scores %d/%d", i.Name, r.Parse.NamesAgree, r.Parse.Compared, r.Parse.ScoresAgree, r.Parse.Scored)
	}
	if compared > 0 && float64(agree) < 0.98*float64(compared) {
		t.Errorf("name agreement %d/%d is below 98%%", agree, compared)
	}
}
