package policy

import (
	"fmt"
	"net/url"

	"github.com/theatarr/theatarr/internal/arr"
	"go.yaml.in/yaml/v3"
)

// Import says where `theatarr import-arr` reads from and which version each arr instance becomes (docs/migration.md).
type Import struct {
	// Arr maps an arr instance name (radarr_4k_debrid) to its source settings.
	Arr map[string]*ArrSource `yaml:"arr"`
}

// ArrSource is an arr instance whose titles and files Theatarr imports.
type ArrSource struct {
	// Flavour is radarr or sonarr.
	Flavour string `yaml:"flavour"`
	// URL is the instance base URL, without /api/v3. Its host must be a LAN address (loopback, RFC 1918 or ULA), since
	// every request carries the API key. Validation checks an IP literal (it makes no DNS lookups); `theatarr policy
	// validate` also resolves a name, and internal/arr checks the address before every connection.
	URL string `yaml:"url"`
	// APIKeyEnv names the env var holding the instance's API key.
	APIKeyEnv string `yaml:"api_key_env"`
	// Version is the version key the instance's titles and files map to. Several instances may map to one
	// version; the import then reports titles whose files differ between them.
	Version string `yaml:"version"`
}

func (p *Policy) validateImport(e *Errors) {
	if p.Import == nil {
		return
	}
	for _, k := range sortedKeys(p.Import.Arr) {
		a := p.Import.Arr[k]
		field := fmt.Sprintf("import.arr[%s]", k)
		checkKey(e, field, k)
		if a == nil {
			e.add(field, "source is required")
			continue
		}
		kind, ok := flavours[a.Flavour]
		if !ok {
			e.add(field+".flavour", "must be radarr or sonarr, got %q", a.Flavour)
		}
		checkURL(e, field+".url", a.URL)
		if u, err := url.Parse(a.URL); err == nil && u.Hostname() != "" {
			if err := arr.CheckLiteral(u.Hostname()); err != nil {
				e.add(field+".url", "%v", err)
			}
		}
		checkEnv(e, field+".api_key_env", a.APIKeyEnv, true)
		if checkRef(e, field+".version", "version", a.Version, p.Versions, true) && ok && p.Versions[a.Version].Kind != kind {
			e.add(field+".version", "version %q is a %s version; a %s instance holds %ss",
				a.Version, p.Versions[a.Version].Kind, a.Flavour, kind)
		}
	}
}

// ManagedBy is instance when it is an import source of p: the arr instance then owns the versions it maps to (their
// monitoring and files) until cutover, and Theatarr refuses to remove or edit them. "" otherwise, also for a nil p.
func (p *Policy) ManagedBy(instance string) string {
	if p == nil || instance == "" || p.Import == nil || p.Import.Arr[instance] == nil {
		return ""
	}
	return instance
}

// ManagedReason says why a version ManagedBy instance is refused.
func ManagedReason(instance string) string {
	return "Managed by " + instance + " until cutover: Theatarr imports its monitoring and files every 15 minutes"
}

// UnmarshalYAML reads an instance strictly, accepting the empty role and sink_path that settings stored before
// comparison imports (role: compare) were removed still carry.
func (a *ArrSource) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		kept := *n
		kept.Content = nil
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value == "role" || k.Value == "sink_path" {
				if v.Tag != "!!null" && v.Value != "" && !(k.Value == "role" && v.Value == "import") {
					return fmt.Errorf("line %d: %s %q: comparison imports were removed; an arr instance is always imported",
						k.Line, k.Value, v.Value)
				}
				continue
			}
			kept.Content = append(kept.Content, k, v)
		}
		n = &kept
	}
	type plain ArrSource
	return decodeStrict(n, (*plain)(a))
}
