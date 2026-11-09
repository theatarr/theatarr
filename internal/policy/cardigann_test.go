package policy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCardigannIndexer(t *testing.T) {
	t.Parallel()
	load := func(t *testing.T, indexers, extra string) (*Policy, error) {
		t.Helper()
		dir := copyExample(t)
		add := func(file, text string) {
			p := filepath.Join(dir, file)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, append(b, []byte(text)...), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		add("indexers.yaml", indexers)
		add("budgets.yaml", "  site-a: {requests: 10, per: 1m}\n")
		if extra != "" {
			if err := os.WriteFile(filepath.Join(dir, "cardigann.yaml"), []byte(extra), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return Load(dir)
	}
	ok := `
  site-a:
    kind: cardigann
    definition: thepiratebay
    url: https://thepiratebay.org/
    privacy: public
    budget: site-a
    settings: {sort: seeders}
    hosts: [apibay.org]
`
	p, err := load(t, ok, "cardigann:\n  definitions: {}\n  flaresolverr_url: http://trawl:8191\n  prowlarr: {url: \"http://10.1.2.3:9696\", api_key_env: THEATARR_PROWLARR_KEY}\n")
	if err != nil {
		t.Fatal(err)
	}
	ix := p.Indexers["site-a"]
	if ix.Definition != "thepiratebay" || ix.Settings["sort"] != "seeders" || !slices.Equal(ix.Hosts, []string{"apibay.org"}) {
		t.Fatalf("indexer %+v", ix)
	}
	if d := p.Cardigann.Definitions; d.RepoOrDefault() != "Prowlarr/Indexers" || d.BranchOrDefault() != "master" || d.RefreshOrDefault().Hours() != 24 {
		t.Errorf("defaults %+v", d)
	}
	hosts := map[string][]string{}
	for _, h := range p.OutboundHosts() {
		hosts[h.Host] = h.Users
	}
	if hosts["apibay.org"] == nil || hosts["trawl:8191"] == nil || hosts["thepiratebay.org"] == nil {
		t.Errorf("outbound hosts %v", hosts)
	}
	if got := p.CardigannDefinitionIDs(); !slices.Equal(got, []string{"thepiratebay"}) {
		t.Errorf("in use %v", got)
	}

	for _, tc := range []struct{ name, indexers, extra, want string }{
		{"no definition", strings.Replace(ok, "    definition: thepiratebay\n", "", 1), "", "indexers[site-a].definition: required"},
		{"a secret in settings", strings.Replace(ok, "{sort: seeders}", "{sort: seeders, password: hunter2}", 1), "", "indexers[site-a].settings.password: holds a credential"},
		{"a shared budget", strings.Replace(ok, "budget: site-a", "budget: prowlarr", 1), "", "a cardigann indexer needs a budget of its own"},
		{"no budget", strings.Replace(ok, "    budget: site-a\n", "", 1), "", "indexers[site-a].budget: required for a cardigann indexer"},
		{"a bad host", strings.Replace(ok, "[apibay.org]", "[\"https://apibay.org/x\"]", 1), "", "indexers[site-a].hosts[0]"},
		{"definition on torznab", strings.Replace(ok, "kind: cardigann", "kind: torznab", 1), "", "indexers[site-a].definition: only valid for a cardigann indexer"},
		{"short refresh", ok, "cardigann:\n  definitions: {refresh: 10m}\n", "cardigann.definitions.refresh: must be at least 1h"},
		{"bad repo", ok, "cardigann:\n  definitions: {repo: ../x}\n", "cardigann.definitions.repo"},
		{"bad flaresolverr", ok, "cardigann:\n  definitions: {}\n  flaresolverr_url: trawl:8191\n", "cardigann.flaresolverr_url"},
		{"no catalog", ok, "", "indexers[site-a].definition: a cardigann indexer needs the definition catalog"},
		{"prowlarr off the LAN", ok, "cardigann:\n  definitions: {}\n  prowlarr: {url: \"http://8.8.8.8:9696\", api_key_env: THEATARR_PROWLARR_KEY}\n", "cardigann.prowlarr.url"},
		{"prowlarr without a key", ok, "cardigann:\n  definitions: {}\n  prowlarr: {url: \"http://10.1.2.3:9696\"}\n", "cardigann.prowlarr.api_key_env: required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(t, tc.indexers, tc.extra); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
