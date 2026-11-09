package decide

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// A one-feature series record is not curator-wanted: a season search drops it, so curator-wanted must not count
// it. The same record as a two-feature season pack is (control).
func TestSingleFeatureSeriesNotCuratorWanted(t *testing.T) {
	t.Parallel()
	e, v, q := reviewCutoffEngine(t, "anime-tv")
	e.Policy.Indexers["sd-test"] = &policy.Indexer{Kind: "seadex", Privacy: policy.PrivacyPublic}
	v.Policy.Indexers = []string{"sd-test"}
	wanted := func(files string) bool {
		t.Helper()
		title := animeMapped("auto")
		f := &ExistingFile{Quality: q.Name, Revision: 1, seaDexEpisodes: [][2]int{{2, 1}}, SeaDexAlternatives: []indexer.SeaDexStanding{
			{Entry: 2, InfoHash: strings.Repeat("f", 40), Quality: q.Name, Preference: 3, RecordID: "abcdef012345678", Size: 20000000}}}
		if err := json.Unmarshal([]byte(`{"facts":{"id":"abcdef012345678","tracker":"Nyaa","isBest":true,"files":[`+files+`]}}`),
			&f.SeaDexAlternatives[0]); err != nil {
			t.Fatal(err)
		}
		got, err := e.QualityCutoffNotMet(v, f, &title)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	ep1 := `{"name":"[Group] Isekai Monogatari - 01 (BD 1080p).mkv","length":10000000}`
	ep2 := `{"name":"[Group] Isekai Monogatari - 02 (BD 1080p).mkv","length":10000000}`
	if !wanted(ep1 + "," + ep2) {
		t.Fatal("control: two-feature season pack is curator-wanted")
	}
	if wanted(ep1) {
		t.Fatal("one-feature series record curator-wanted, but a season search drops it")
	}
}
