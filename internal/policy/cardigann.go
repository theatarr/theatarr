package policy

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
)

// Cardigann configures the built-in indexer catalog (Prowlarr's Cardigann definitions, run natively). Nothing is
// fetched unless definitions is set, and no indexer runs a definition unless it is configured with kind cardigann.
type Cardigann struct {
	// Definitions is the repository branch the catalog follows. Unset: the catalog is not fetched.
	Definitions *CardigannDefinitions `yaml:"definitions"`
	// FlareSolverrURL is a FlareSolverr-compatible service (trawl) that passes Cloudflare checks, e.g.
	// http://trawl:8191. Its traffic must leave through the VPN; requests to it go through the gateway as the
	// indexer's own. Empty: sites behind a Cloudflare check fail with a message saying so.
	FlareSolverrURL string `yaml:"flaresolverr_url"`
	// Prowlarr is a Prowlarr instance on the LAN whose indexers the migration reader proposes as native ones. It is
	// only read (GET /api/v1/indexer); nothing is written to it or to the settings.
	Prowlarr *CardigannProwlarr `yaml:"prowlarr"`
}

// CardigannProwlarr is the Prowlarr the migration reader reads.
type CardigannProwlarr struct {
	// URL is the Prowlarr server endpoint.
	URL string `yaml:"url"`
	// APIKeyEnv names the environment variable holding the Prowlarr API key.
	APIKeyEnv string `yaml:"api_key_env"`
}

// CardigannDefinitions is the tracked definitions repository.
type CardigannDefinitions struct {
	// Repo is owner/repository on GitHub. Default Prowlarr/Indexers.
	Repo string `yaml:"repo"`
	// Branch selects the repository branch. Default master.
	Branch string `yaml:"branch"`
	// Refresh is how often the branch is checked. Default 24h, at least 1h.
	Refresh Duration `yaml:"refresh"`
}

// Defaults of the definitions source.
const (
	DefaultCardigannRepo    = "Prowlarr/Indexers"
	DefaultCardigannBranch  = "master"
	DefaultCardigannRefresh = 24 * time.Hour
)

var (
	cardigannRepoRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	cardigannBranchRE = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)
	cardigannIDRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	settingNameRE     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// RepoOrDefault, BranchOrDefault and RefreshOrDefault apply the defaults.
func (d *CardigannDefinitions) RepoOrDefault() string {
	if d == nil || d.Repo == "" {
		return DefaultCardigannRepo
	}
	return d.Repo
}

func (d *CardigannDefinitions) BranchOrDefault() string {
	if d == nil || d.Branch == "" {
		return DefaultCardigannBranch
	}
	return d.Branch
}

func (d *CardigannDefinitions) RefreshOrDefault() time.Duration {
	if d == nil || d.Refresh <= 0 {
		return DefaultCardigannRefresh
	}
	return time.Duration(d.Refresh)
}

// CardigannDefinitionIDs lists the definitions the policy's cardigann indexers use, sorted.
func (p *Policy) CardigannDefinitionIDs() []string {
	seen := map[string]bool{}
	for _, ix := range p.Indexers {
		if ix != nil && ix.Kind == "cardigann" && ix.Definition != "" {
			seen[ix.Definition] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// validateCardigannIndexer checks the cardigann-only fields of an indexer, and that other kinds leave them empty.
func (p *Policy) validateCardigannIndexer(e *Errors, path string, ix *Indexer) {
	if ix.Kind != "cardigann" {
		if ix.Definition != "" {
			e.add(path+".definition", "only valid for a cardigann indexer (kind is %s)", ix.Kind)
		}
		if len(ix.Settings) > 0 {
			e.add(path+".settings", "only valid for a cardigann indexer (kind is %s)", ix.Kind)
		}
		if len(ix.Hosts) > 0 {
			e.add(path+".hosts", "only valid for a cardigann indexer (kind is %s)", ix.Kind)
		}
		return
	}
	if !cardigannIDRE.MatchString(ix.Definition) {
		e.add(path+".definition", "required: the catalog id of the definition, got %q", ix.Definition)
	}
	if p.Cardigann == nil || p.Cardigann.Definitions == nil {
		e.add(path+".definition", "a cardigann indexer needs the definition catalog: set cardigann.definitions")
	}
	for _, k := range sortedKeys(ix.Settings) {
		if !settingNameRE.MatchString(k) {
			e.add(path+".settings", "bad setting name %q", k)
			continue
		}
		if CardigannSecretSetting(k) {
			e.add(fmt.Sprintf("%s.settings.%s", path, k), "holds a credential: put it in the indexer's secret (a JSON object of the secret settings), not in settings")
		}
	}
	for i, h := range ix.Hosts {
		if !validHostName(h) {
			e.add(fmt.Sprintf("%s.hosts[%d]", path, i), "must be a host name or host:port, got %q", h)
		}
	}
	// Every site gets its own budget, so one site's limits and back-offs never hold another back.
	if ix.Budget == "" {
		e.add(path+".budget", "required for a cardigann indexer: each site has its own budget")
	} else {
		for _, k := range sortedKeys(p.Indexers) {
			if other := p.Indexers[k]; other != ix && other != nil && other.Budget == ix.Budget {
				e.add(path+".budget", "budget %q is also used by indexers[%s]; a cardigann indexer needs a budget of its own", ix.Budget, k)
				break
			}
		}
	}
}

// CardigannSecretSetting reports whether a definition setting name holds a credential (the same rule as the
// engine's, by name only).
func CardigannSecretSetting(name string) bool {
	n := strings.ToLower(name)
	for _, part := range []string{"password", "cookie", "apikey", "api_key", "passkey", "pass_key", "rsskey", "authkey", "token", "secret", "2fa"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return n == "pin" || n == "pid" || n == "key"
}

func validHostName(h string) bool {
	if h == "" || strings.ContainsAny(h, "/?#@ ") {
		return false
	}
	u, err := url.Parse("http://" + h)
	if err != nil || u.Host != h || u.Hostname() == "" {
		return false
	}
	if _, port, err := net.SplitHostPort(h); err == nil && port == "" {
		return false
	}
	return true
}

func (p *Policy) validateCardigann(e *Errors) {
	c := p.Cardigann
	if c == nil {
		return
	}
	if c.FlareSolverrURL != "" {
		checkURL(e, "cardigann.flaresolverr_url", c.FlareSolverrURL)
	}
	if pr := c.Prowlarr; pr != nil {
		checkURL(e, "cardigann.prowlarr.url", pr.URL)
		if u, err := url.Parse(pr.URL); err == nil && u.Hostname() != "" {
			if err := arr.CheckLiteral(u.Hostname()); err != nil {
				e.add("cardigann.prowlarr.url", "%v", err)
			}
		}
		checkEnv(e, "cardigann.prowlarr.api_key_env", pr.APIKeyEnv, true)
	}
	if d := c.Definitions; d != nil {
		if d.Repo != "" && (!cardigannRepoRE.MatchString(d.Repo) || strings.Contains(d.Repo, "..")) {
			e.add("cardigann.definitions.repo", "must be owner/repository, got %q", d.Repo)
		}
		if d.Branch != "" && (!cardigannBranchRE.MatchString(d.Branch) || strings.Contains(d.Branch, "..") || strings.HasSuffix(d.Branch, "/") || len(d.Branch) > 200) {
			e.add("cardigann.definitions.branch", "must be a branch name, got %q", d.Branch)
		}
		if d.Refresh != 0 && time.Duration(d.Refresh) < time.Hour {
			e.add("cardigann.definitions.refresh", "must be at least 1h (default 24h)")
		}
	}
}
