package cardigann

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/theatarr/theatarr/internal/arr"
)

// ReadProwlarr reads Prowlarr's indexer list (GET /api/v1/indexer) with the LAN-only client, which can call nothing
// else. Link Prowlarr and Move from Prowlarr both read it.
func ReadProwlarr(ctx context.Context, baseURL, key string) ([]ProwlarrIndexer, error) {
	cl, err := arr.New(baseURL, key, arr.Options{Prowlarr: true, Retries: 1})
	if err != nil {
		return nil, err
	}
	var list []ProwlarrIndexer
	if err := cl.GetJSON(ctx, "indexer", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ProwlarrFeed is one Prowlarr indexer as Link Prowlarr offers it: a Torznab feed at Prowlarr's per-indexer URL.
type ProwlarrFeed struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Privacy  string `json:"privacy"`
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	// Key is the indexer key a link would use (linkable feeds only); Linked the existing indexer with this URL.
	Key    string `json:"key,omitempty"`
	Linked string `json:"linked,omitempty"`
	// Status is linkable, linked (an indexer already has this URL), disabled (off in Prowlarr) or usenet.
	Status string `json:"status"`
}

// Link Prowlarr statuses.
const (
	FeedLinkable = "linkable"
	FeedLinked   = "linked"
	FeedDisabled = "disabled"
	FeedUsenet   = "usenet"
)

// ProwlarrFeeds lists Prowlarr's indexers as feeds, sorted by id. existing maps each indexer's normalised URL
// (NormalizeFeedURL) to its key; taken are the indexer keys in use, which new keys (prowlarr-<name>) avoid.
func ProwlarrFeeds(list []ProwlarrIndexer, baseURL string, existing map[string]string, taken []string) []ProwlarrFeed {
	sorted := append([]ProwlarrIndexer(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	used := map[string]bool{}
	for _, k := range taken {
		used[k] = true
	}
	base := strings.TrimRight(baseURL, "/")
	out := make([]ProwlarrFeed, 0, len(sorted))
	for _, pi := range sorted {
		f := ProwlarrFeed{ID: pi.ID, Name: pi.Name, Privacy: prowlarrPrivacy(pi.Privacy), Protocol: pi.Protocol,
			Enabled: pi.Enable, URL: base + "/" + strconv.Itoa(pi.ID) + "/api"}
		switch linked, ok := existing[NormalizeFeedURL(f.URL)]; {
		case ok:
			f.Status, f.Linked = FeedLinked, linked
		case strings.EqualFold(pi.Protocol, "usenet"):
			f.Status = FeedUsenet
		case !pi.Enable:
			f.Status = FeedDisabled
		default:
			f.Status = FeedLinkable
			f.Key = uniqueKey("prowlarr-"+pi.Name, used)
		}
		out = append(out, f)
	}
	return out
}

// NormalizeFeedURL is a feed URL as Link Prowlarr compares it: no trailing slash, scheme and host lower-cased.
func NormalizeFeedURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	return u.String()
}

// ProwlarrIndexer is the part of one entry of Prowlarr's GET /api/v1/indexer that the migration reader uses.
type ProwlarrIndexer struct {
	ID             int             `json:"id"`
	Name           string          `json:"name"`
	DefinitionName string          `json:"definitionName"`
	Implementation string          `json:"implementation"`
	Protocol       string          `json:"protocol"`
	Privacy        string          `json:"privacy"`
	Enable         bool            `json:"enable"`
	IndexerURLs    []string        `json:"indexerUrls"`
	Fields         []ProwlarrField `json:"fields"`
}

// ProwlarrField is one setting of a Prowlarr indexer. Prowlarr masks secret values; the reader never copies them.
type ProwlarrField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// Proposal is what the reader proposes for one Prowlarr indexer. It is a proposal only: nothing is saved.
type Proposal struct {
	Prowlarr ProwlarrRef `json:"prowlarr"`
	// Status is proposed, no_definition (no catalog definition by that name: a Prowlarr-native or custom indexer),
	// unsupported (the definition can't run here) or usenet.
	Status string `json:"status"`
	// Key is a suggested indexer key; Indexer the suggested indexer (policy field names), Budget its own budget.
	Key     string            `json:"key,omitempty"`
	Indexer *ProposedIndexer  `json:"indexer,omitempty"`
	Budget  map[string]string `json:"budget,omitempty"`
	// Secrets are the secret settings to put in the indexer's credential ({"name": "value", ...}); their values are
	// not read from Prowlarr.
	Secrets []string `json:"secrets,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// ProwlarrRef identifies the Prowlarr indexer a proposal is for.
type ProwlarrRef struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	DefinitionName string `json:"definition_name"`
	Privacy        string `json:"privacy"`
	Enabled        bool   `json:"enabled"`
}

// ProposedIndexer is a cardigann indexer as the policy writes it.
type ProposedIndexer struct {
	Kind       string            `json:"kind"`
	Definition string            `json:"definition"`
	URL        string            `json:"url"`
	Privacy    string            `json:"privacy"`
	Budget     string            `json:"budget"`
	Settings   map[string]string `json:"settings,omitempty"`
	Hosts      []string          `json:"hosts,omitempty"`
}

// Proposal statuses.
const (
	ProposalProposed     = "proposed"
	ProposalNoDefinition = "no_definition"
	ProposalUnsupported  = "unsupported"
	ProposalUsenet       = "usenet"
)

var keyUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// ProposeFromProwlarr maps Prowlarr's indexers to native cardigann indexers. lookup finds a catalog definition by id
// (or by an id it replaces). Privacy is the stricter of Prowlarr's and the definition's; settings come over when
// they are not secret and differ from the definition's default.
func ProposeFromProwlarr(list []ProwlarrIndexer, lookup func(id string) (Summary, bool)) []Proposal {
	sorted := append([]ProwlarrIndexer(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	used := map[string]bool{}
	out := make([]Proposal, 0, len(sorted))
	for _, pi := range sorted {
		p := Proposal{Prowlarr: ProwlarrRef{ID: pi.ID, Name: pi.Name, DefinitionName: pi.DefinitionName,
			Privacy: prowlarrPrivacy(pi.Privacy), Enabled: pi.Enable}}
		if strings.EqualFold(pi.Protocol, "usenet") {
			p.Status = ProposalUsenet
			p.Notes = append(p.Notes, "usenet indexers are not run natively")
			out = append(out, p)
			continue
		}
		sum, ok := lookup(strings.ToLower(strings.TrimSpace(pi.DefinitionName)))
		if !ok {
			p.Status = ProposalNoDefinition
			p.Notes = append(p.Notes, fmt.Sprintf("no catalog definition named %q (%s indexer); keep it behind Prowlarr", pi.DefinitionName, pi.Implementation))
			out = append(out, p)
			continue
		}
		if !sum.Supported {
			p.Status = ProposalUnsupported
			p.Notes = append(p.Notes, sum.Problems...)
			out = append(out, p)
			continue
		}
		p.Status = ProposalProposed
		p.Key = uniqueKey(sum.ID, used)
		privacy := stricterPrivacy(p.Prowlarr.Privacy, sum.Privacy)
		ix := &ProposedIndexer{Kind: "cardigann", Definition: sum.ID, Privacy: privacy, Budget: p.Key, Hosts: sum.Hosts}
		fields := map[string]any{}
		for _, f := range pi.Fields {
			fields[f.Name] = f.Value
		}
		ix.URL = fieldString(fields["baseUrl"])
		if ix.URL == "" && len(pi.IndexerURLs) > 0 {
			ix.URL = pi.IndexerURLs[0]
		}
		if ix.URL == "" && len(sum.Links) > 0 {
			ix.URL = sum.Links[0]
		}
		for _, s := range sum.Settings {
			if strings.HasPrefix(s.Type, "info") {
				continue
			}
			if s.Secret {
				p.Secrets = append(p.Secrets, s.Name)
				continue
			}
			v, ok := fields[s.Name]
			if !ok {
				continue
			}
			val := settingValue(s, v)
			if val == "" || val == fmt.Sprint(s.Default) {
				continue
			}
			if ix.Settings == nil {
				ix.Settings = map[string]string{}
			}
			ix.Settings[s.Name] = val
		}
		p.Indexer = ix
		p.Budget = map[string]string{"requests": "10", "per": "1m"}
		if privacy != p.Prowlarr.Privacy {
			p.Notes = append(p.Notes, fmt.Sprintf("Prowlarr says %s; the definition says %s: proposed %s", p.Prowlarr.Privacy, sum.Privacy, privacy))
		}
		if sum.Status == StatusNeedsFlareSolverr {
			p.Notes = append(p.Notes, "the site may show a Cloudflare check: it needs cardigann.flaresolverr_url (trawl)")
		}
		if len(p.Secrets) > 0 {
			p.Notes = append(p.Notes, "put the secret settings in the indexer's credential; Prowlarr does not hand them out")
		}
		out = append(out, p)
	}
	return out
}

// prowlarrPrivacy maps Prowlarr's privacy names; anything else is private.
func prowlarrPrivacy(p string) string {
	switch strings.ToLower(p) {
	case "public":
		return "public"
	case "semiprivate", "semi-private":
		return "semi-private"
	}
	return "private"
}

func privacyRank(p string) int {
	switch p {
	case "public":
		return 0
	case "semi-private":
		return 1
	}
	return 2
}

func stricterPrivacy(a, b string) string {
	if privacyRank(a) >= privacyRank(b) {
		return a
	}
	return b
}

func uniqueKey(id string, used map[string]bool) string {
	base := strings.Trim(keyUnsafe.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if base == "" {
		base = "site"
	}
	key := base
	for n := 2; used[key]; n++ {
		key = fmt.Sprintf("%s-%d", base, n)
	}
	used[key] = true
	return key
}

// settingValue turns a Prowlarr field value into the definition's setting value. Prowlarr keeps a select as the
// option's index after sorting the option keys with the default culture comparer, and a checkbox as a bool.
func settingValue(s Setting, v any) string {
	if s.Type == "select" {
		if f, ok := v.(float64); ok && f >= 0 && int(f) < len(s.Options) && float64(int(f)) == f {
			// Prowlarr CardigannBase.GetBaseTemplateVariables and IndexerResource.MapCardigannField use
			// Options.OrderBy(x => x.Key), hence Comparer<string>.Default (CurrentCulture), not ordinal order.
			// English collation matches its usual ICU ordering for option keys: punctuation is significant,
			// digits sort lexically, and case breaks ties after letters. Keep OrderBy's stable ties. Prowlarr's
			// API does not expose its process culture or invariant-globalization override.
			keys := make([]string, len(s.Options))
			for i, option := range s.Options {
				keys[i] = option.Value
			}
			cmp := collate.New(language.English)
			sort.SliceStable(keys, func(i, j int) bool { return cmp.CompareString(keys[i], keys[j]) < 0 })
			return keys[int(f)]
		}
	}
	return fieldString(v)
}

func fieldString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
