// Port of Mediarium internal/indexers/definitions.go (summaries, setting kinds) and local_sites.go
// (IsLocalDefinition) @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0, Copyright (C) Ryan Borg and the Mediarium
// contributors). Privacy, categories, required settings, hosts and the status are additions.

package cardigann

import (
	"net/url"
	"sort"
	"strings"
)

// Summary is what the catalog shows for a definition. It never carries a configured value.
type Summary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Type is the definition's own privacy: public, semi-private or private. Privacy is what Theatarr treats it as:
	// public only when Type says so, private otherwise (semi-private and unknown count as private).
	Type        string    `json:"type"`
	Privacy     string    `json:"privacy"`
	Language    string    `json:"language"`
	Protocol    string    `json:"protocol"`
	Links       []string  `json:"links"`
	LegacyLinks []string  `json:"legacy_links,omitempty"`
	Replaces    []string  `json:"replaces,omitempty"`
	Settings    []Setting `json:"settings"`
	// Categories are the Newznab categories the site maps to, sorted.
	Categories []int `json:"categories"`
	// Login is the definition's sign-in method (post, form, cookie, get), or "" for a site without one.
	Login string `json:"login,omitempty"`
	// Hosts are hosts other than the site that the search paths reach with the default settings (an API host);
	// an indexer must list them in its hosts.
	Hosts []string `json:"hosts,omitempty"`
	// Supported is false when the definition uses something this engine can't run; Problems say what.
	Supported bool     `json:"supported"`
	Problems  []string `json:"problems,omitempty"`
	// Status sums it up for the catalog: runs, needs_account, needs_flaresolverr or unsupported.
	Status string `json:"status"`
	// Local marks a site that runs on the person's own network (every link is a local address).
	Local bool `json:"local,omitempty"`
	// Custom marks the operator's own definition (custom/ in the catalog folder), which wins over a synced one with
	// the same id.
	Custom bool `json:"custom,omitempty"`
	// RSS: the RSS sync can read the site's latest releases (its search without keywords); RSSProblem says why not.
	RSS        bool   `json:"rss"`
	RSSProblem string `json:"rss_problem,omitempty"`
}

// Setting is one setting as the definition declares it, for a form. Never carries a configured value.
type Setting struct {
	Name    string          `json:"name"`
	Label   string          `json:"label"`
	Type    string          `json:"type"`
	Default any             `json:"default,omitempty"`
	Options []SettingOption `json:"options,omitempty"`
	// Secret settings go in the indexer's credential, never in its settings.
	Secret bool `json:"secret,omitempty"`
	// Required: a text or password setting without a default that the definition uses.
	Required bool `json:"required,omitempty"`
}

// SettingOption is one choice of a select setting.
type SettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Catalog statuses.
const (
	StatusRuns              = "runs"
	StatusNeedsAccount      = "needs_account"
	StatusNeedsFlareSolverr = "needs_flaresolverr"
	StatusUnsupported       = "unsupported"
)

// predefinedInfo fills in the text of the predefined info_* setting types, which definitions declare without a
// label or text of their own.
var predefinedInfo = map[string][2]string{
	"info_cookie": {"How to get the cookie",
		"Sign in to the site in your web browser, open the browser's developer tools, reload a page of the site and copy the value of the Cookie request header from the network tab. Put it in the indexer's credential as cookie. When the site signs you out, the cookie stops working and has to be copied again."},
	"info_flaresolverr": {"About Cloudflare protection",
		"This site may show a Cloudflare check that only a real browser can pass. To use it, run trawl (FlareSolverr-compatible) with its traffic leaving through the VPN, and set cardigann.flaresolverr_url."},
	"info_useragent": {"About the User-Agent",
		"Some sites tie your session cookie to the browser it was created in. Copy your browser's User-Agent header together with the cookie."},
	"info_category_8000": {"About categories",
		"Results in categories this site doesn't map are listed as Other."},
}

// Privacy is the privacy Theatarr gives a definition type: public only for "public"; private, semi-private and
// anything else (or nothing) count as private.
func Privacy(defType string) string {
	switch strings.ToLower(strings.TrimSpace(defType)) {
	case "public":
		return "public"
	case "semi-private":
		return "semi-private"
	}
	return "private"
}

// Summarize describes a definition for the catalog.
func Summarize(def *Definition) Summary {
	sum := Summary{
		ID:          def.ID,
		Name:        def.Name,
		Description: def.Description,
		Type:        def.Type,
		Privacy:     Privacy(def.Type),
		Language:    def.Language,
		Protocol:    string(def.DefinitionProtocol()),
		Links:       def.Links,
		LegacyLinks: def.LegacyLinks,
		Replaces:    def.Replaces,
		Settings:    []Setting{},
		Supported:   true,
		Categories:  def.Categories(),
	}
	if sum.Name == "" {
		sum.Name = def.ID
	}
	if sum.Links == nil {
		sum.Links = []string{}
	}
	sum.Local = IsLocalDefinition(sum.Links)
	flare := false
	for _, st := range def.Settings {
		ss := Setting{Name: st.Name, Label: st.Label, Type: st.Type, Secret: SecretSetting(st.Type, st.Name)}
		if st.Type == "info_flaresolverr" {
			flare = true
		}
		if info, ok := predefinedInfo[st.Type]; ok {
			if ss.Label == "" {
				ss.Label = info[0]
			}
			if st.Default == "" {
				st.Default = info[1]
			}
		}
		switch st.Type {
		case "checkbox":
			ss.Default = parseBoolSetting(st.Default)
		default:
			if st.Default != "" {
				ss.Default = st.Default
			}
		}
		for _, k := range st.Options.Keys {
			ss.Options = append(ss.Options, SettingOption{Value: k, Label: st.Options.Values[k]})
		}
		if ss.Label == "" {
			ss.Label = st.Name
		}
		ss.Required = requiredSetting(st)
		sum.Settings = append(sum.Settings, ss)
	}
	if def.Login != nil {
		sum.Login = strings.ToLower(def.Login.Method)
		if sum.Login == "" {
			sum.Login = "post"
		}
	}
	sum.Hosts = ExtraHosts(def, nil)
	if problems := def.Problems(); len(problems) > 0 {
		sum.Supported = false
		sum.Problems = problems
	}
	sum.RSSProblem = RSSProblem(def)
	sum.RSS = sum.RSSProblem == ""
	switch {
	case !sum.Supported:
		sum.Status = StatusUnsupported
	case flare:
		sum.Status = StatusNeedsFlareSolverr
	case def.Login != nil || len(sum.RequiredSettings()) > 0:
		sum.Status = StatusNeedsAccount
	default:
		sum.Status = StatusRuns
	}
	return sum
}

// RequiredSettings names the settings a configuration must fill.
func (s Summary) RequiredSettings() []string {
	var out []string
	for _, st := range s.Settings {
		if st.Required {
			out = append(out, st.Name)
		}
	}
	return out
}

// requiredSetting: a text or password setting with no default. (Definitions have no "required" flag; Prowlarr
// asks for every field, and an empty login, cookie or key is what fails.)
func requiredSetting(st SettingField) bool {
	t := strings.ToLower(strings.TrimSpace(st.Type))
	if t != "text" && t != "password" {
		return false
	}
	if strings.TrimSpace(st.Default) != "" || plainSettings[strings.ToLower(st.Name)] {
		return false
	}
	return SecretSetting(st.Type, st.Name) || credentialSettingName(st.Name)
}

// Categories lists the Newznab categories the definition maps its site categories to, sorted.
func (d *Definition) Categories() []int {
	seen := map[int]bool{}
	for _, m := range d.mappings() {
		if m.newznab > 0 {
			seen[m.newznab] = true
		}
	}
	out := make([]int, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// ExtraHosts lists the hosts, other than the site's, that the definition's search, login and download paths
// reach with the given settings (defaults for the rest), sorted. Paths that cannot be rendered are skipped.
func ExtraHosts(def *Definition, settings map[string]string) []string {
	if len(def.Links) == 0 {
		return nil
	}
	s, err := newSession(&Manager{}, def, Instance{Key: def.ID, BaseURL: def.Links[0], Settings: settings})
	if err != nil {
		return nil
	}
	vars := s.vars()
	vars.Query["Keywords"] = "test"
	vars.Keywords = "test"
	vars.Categories = def.siteCategories([]int{2000, 5000})
	var paths []string
	for _, p := range def.Search.Paths {
		paths = append(paths, p.Path)
	}
	if def.Login != nil {
		paths = append(paths, def.Login.Path, def.Login.SubmitPath)
	}
	if def.Download != nil && def.Download.Before != nil {
		paths = append(paths, def.Download.Before.Path)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out, err := applyTemplate(p, vars, urlPathEscape)
		if err != nil {
			continue
		}
		u, err := s.base.Parse(strings.TrimSpace(out))
		if err != nil || u.Hostname() == "" {
			continue
		}
		if h := normHost(u.Hostname()); h != normHost(s.base.Hostname()) {
			seen[h] = true
		}
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// secretNameParts mark settings that hold credentials even when the definition declares them as plain text
// (cookies, API keys, passkeys).
var secretNameParts = []string{"password", "cookie", "apikey", "api_key", "passkey", "pass_key", "rsskey", "authkey", "token", "secret", "2fa"}

// SecretSetting reports whether a definition setting holds a credential: it belongs in the indexer's credential,
// never in its settings.
func SecretSetting(settingType, name string) bool {
	settingType = strings.ToLower(strings.TrimSpace(settingType))
	if settingType == "password" {
		return true
	}
	if strings.HasPrefix(settingType, "info") {
		return false
	}
	n := strings.ToLower(name)
	for _, p := range secretNameParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return n == "pin" || n == "pid" || n == "key"
}

// HasLink reports whether u is one of the site's addresses (trailing slashes ignored).
func (s Summary) HasLink(u string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), "/") }
	for _, l := range append(append([]string{}, s.Links...), s.LegacyLinks...) {
		if norm(l) == norm(u) {
			return true
		}
	}
	return false
}

func parseBoolSetting(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on", checkboxTrue:
		return true
	}
	return false
}

// IsLocalDefinition reports whether every link of a definition is on the person's own network (a self-hosted
// site).
func IsLocalDefinition(links []string) bool {
	if len(links) == 0 {
		return false
	}
	for _, l := range links {
		u, err := url.Parse(strings.TrimSpace(l))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return false
		}
		if !localHost(u.Hostname()) {
			return false
		}
	}
	return true
}
