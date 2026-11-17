package web

import (
	"bytes"
	"encoding/json"
	"github.com/theatarr/theatarr/internal/settings/fielddocs"
	"strings"
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

// "Only add cached releases" keeps its name; its help says what happens per provider: TorBox refuses the add, Real-Debrid
// and AllDebrid add to check and delete what Theatarr just added, and AllDebrid's switch is the exception.
func TestCachedOnlyHelpNamesEachProvider(t *testing.T) {
	var docs map[string]map[string]string
	if err := json.Unmarshal(sectionFieldDocs, &docs); err != nil {
		t.Fatal(err)
	}
	help := docs["Router"]["cached_only"]
	for _, want := range []string{"TorBox refuses an uncached add", "Real-Debrid and AllDebrid add to check", "Theatarr just made", `"Delete uncached AllDebrid adds"`} {
		if !strings.Contains(help, want) {
			t.Errorf("cached_only help %q lacks %q", help, want)
		}
	}
	if sectionLabels["router.cached_only"] != "Only add cached releases" {
		t.Errorf("label changed: %q", sectionLabels["router.cached_only"])
	}
}
