package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeaDexScoring(t *testing.T) {
	t.Parallel()
	edit := func(t *testing.T, old, new string) string {
		t.Helper()
		dir := copyExample(t)
		p := filepath.Join(dir, "indexers.yaml")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), old) {
			t.Fatalf("indexers.yaml has no %q", old)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir := edit(t, "    budget: seadex\n", "    budget: seadex\n    seadex:\n      dual_audio: 50\n      size_weight: -30\n"+
		"      exclude_tags: [Deband Required]\n      tags: {HDR: 20, Broken: -30}\n")
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sc := p.Indexers["seadex"].SeaDex
	if sc == nil || sc.BestWeight() != DefaultSeaDexBest || sc.DualAudio != 50 || sc.SizeWeight != -30 ||
		sc.Tags["Broken"] != -30 || sc.ExcludeTags[0] != "Deband Required" {
		t.Errorf("scoring %+v", sc)
	}
	var none *SeaDexScoring
	if none.BestWeight() != 100 {
		t.Error("nil scoring: best weight not the default")
	}
	dir = edit(t, "    budget: dmm-key\n", "    budget: dmm-key\n    seadex: {best: 5}\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "indexers[dmm].seadex: only valid for a seadex indexer") {
		t.Errorf("seadex scoring on dmm: %v", err)
	}
}
