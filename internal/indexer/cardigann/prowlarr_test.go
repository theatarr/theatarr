package cardigann

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Prowlarr's answer (shape of GET /api/v1/indexer, values invented; the masked password as Prowlarr sends it).
const prowlarrIndexers = `[
 {"id":3,"name":"Fixture Private","definitionName":"fixtureprivate","implementation":"Cardigann","protocol":"torrent","privacy":"public","enable":true,
  "fields":[{"name":"definitionFile","value":"fixtureprivate"},{"name":"baseUrl","value":"https://mirror.private.fixture.test/"},
   {"name":"username","value":"alice"},{"name":"password","value":"********"}]},
 {"id":1,"name":"Fixture Public","definitionName":"fixturepublic","implementation":"Cardigann","protocol":"torrent","privacy":"public","enable":true,
  "indexerUrls":["https://public.fixture.test/"],
  "fields":[{"name":"sort","value":1},{"name":"disablesort","value":false}]},
 {"id":2,"name":"Native Site","definitionName":"BeyondHD","implementation":"BeyondHD","protocol":"torrent","privacy":"private","enable":true},
 {"id":4,"name":"News","definitionName":"nzbsite","implementation":"Newznab","protocol":"usenet","privacy":"private","enable":true}
]`

func TestProposeFromProwlarr(t *testing.T) {
	t.Parallel()
	var list []ProwlarrIndexer
	if err := json.Unmarshal([]byte(prowlarrIndexers), &list); err != nil {
		t.Fatal(err)
	}
	sums := map[string]Summary{}
	for _, name := range []string{"fixturepublic", "fixtureprivate"} {
		def, err := ParseDefinition(fixtureDef(t, name))
		if err != nil {
			t.Fatal(err)
		}
		sums[name] = Summarize(def)
	}
	got := ProposeFromProwlarr(list, func(id string) (Summary, bool) { s, ok := sums[id]; return s, ok })
	if len(got) != 4 {
		t.Fatalf("%d proposals", len(got))
	}
	pub := got[0]
	if pub.Prowlarr.ID != 1 || pub.Status != ProposalProposed || pub.Key != "fixturepublic" || pub.Indexer.Privacy != "public" ||
		pub.Indexer.URL != "https://public.fixture.test/" || pub.Indexer.Budget != "fixturepublic" {
		t.Errorf("public %+v %+v", pub, pub.Indexer)
	}
	// Sorted select option 1 is "time", the default; both settings are left out.
	if len(pub.Indexer.Settings) != 0 {
		t.Errorf("public settings %v", pub.Indexer.Settings)
	}
	priv := got[2]
	if priv.Prowlarr.ID != 3 || priv.Status != ProposalProposed || priv.Indexer.Privacy != "private" ||
		priv.Indexer.URL != "https://mirror.private.fixture.test/" {
		t.Errorf("private %+v %+v", priv, priv.Indexer)
	}
	// Prowlarr called it public; the definition says private, and the stricter wins. No secret value comes over.
	if !slices.Contains(priv.Secrets, "password") || priv.Indexer.Settings["password"] != "" {
		t.Errorf("secrets %v settings %v", priv.Secrets, priv.Indexer.Settings)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "********") {
		t.Error("a masked secret came over")
	}
	if got[1].Status != ProposalNoDefinition || got[1].Indexer != nil || got[3].Status != ProposalUsenet {
		t.Errorf("native %+v usenet %+v", got[1], got[3])
	}
}

func TestProwlarrSelectUsesSortedOptionKeys(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(string(fixtureDef(t, "fixturepublic")), "      time: created\n      seeders: seeders", "      zebra: Z\n      alpha: A\n      middle: M", 1)
	def, err := ParseDefinition([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := Summarize(def)
	for index, want := range []string{"alpha", "middle", "zebra"} {
		got := ProposeFromProwlarr([]ProwlarrIndexer{{ID: 1, DefinitionName: sum.ID, Protocol: "torrent", Privacy: "public", Fields: []ProwlarrField{{Name: "sort", Value: float64(index)}}}}, func(string) (Summary, bool) { return sum, true })
		if got[0].Indexer.Settings["sort"] != want {
			t.Errorf("index %d: %v, want %s", index, got[0].Indexer.Settings, want)
		}
	}
	// Importing must not reorder the definition's native option list.
	if sum.Settings[0].Options[0].Value != "zebra" {
		t.Fatal("definition mutated")
	}
}
