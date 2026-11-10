package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/recyclarr"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/uipages"
	"go.yaml.in/yaml/v3"
)

// PlanOptions are the choices a plan is built with.
type PlanOptions struct {
	Name string `json:"name,omitempty"`
	// Native names Prowlarr indexers (or "all") to move to the native Cardigann catalog when it has a runnable
	// definition. The rest stay Prowlarr Torznab feeds, the supported default.
	Native []string `json:"native,omitempty"`
	// Skip names Prowlarr indexers to leave out.
	Skip []string `json:"skip,omitempty"`
	// Providers are debrid accounts to add, disabled. Accounts of a kind Theatarr already has are reused.
	Providers []ProviderChoice `json:"providers,omitempty"`
	// PathMap rewrites the arrs' paths to the paths Theatarr sees them at (longest From first).
	PathMap []PathMapping `json:"path_map,omitempty"`
	// ProwlarrURL is Prowlarr's address as Theatarr reaches it, when it differs from the inspected URL.
	ProwlarrURL string `json:"prowlarr_url,omitempty"`
}

// ProviderChoice is one debrid account: its kind (realdebrid, alldebrid, torbox), an optional key for the record and
// the name of its API key, resolved at apply.
type ProviderChoice struct {
	Kind   string `json:"kind"`
	Key    string `json:"key,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// PathMapping is one path rewrite.
type PathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Plan maps an inventory to Theatarr settings. It is saved in the database and resumable: apply records each step's
// outcome in it.
type Plan struct {
	SchemaVersion int         `json:"schema_version"`
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Options       PlanOptions `json:"options"`
	// Libraries group the arr instances: instances of one kind (movies, series, anime) are versions of one library.
	Libraries []Library     `json:"libraries"`
	Indexers  []IndexerPlan `json:"indexers"`
	Steps     []Step        `json:"steps"`
	// Gaps is what cannot be migrated automatically.
	Gaps        []Gap        `json:"gaps"`
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// Library is the versions of one kind of title.
type Library struct {
	Name     string        `json:"name"`
	Kind     policy.Kind   `json:"kind"`
	Anime    *bool         `json:"anime"`
	Versions []VersionPlan `json:"versions"`
}

// VersionPlan maps one arr instance to one version.
type VersionPlan struct {
	Key      string `json:"key"`
	Instance string `json:"instance"`
	// Existing is set when the policy already has the version (import.arr feeds it from this instance).
	Existing bool `json:"existing,omitempty"`
	// Library is the version's root as Theatarr sees it; ArrRoot the arr's root folder it maps.
	Library    string   `json:"library"`
	ArrRoot    string   `json:"arr_root"`
	Profile    string   `json:"profile"`
	ArrProfile string   `json:"arr_profile"`
	Indexers   []string `json:"indexers"`
	Providers  []string `json:"providers"`
	Client     string   `json:"client"`
	View       string   `json:"view,omitempty"`
	// Seerr lists the Seerr server entries that point at the instance.
	Seerr []string `json:"seerr,omitempty"`
	// Recyclarr is the TRaSH profile Recyclarr builds the arr profile from, if it does.
	Recyclarr *settings.ProfileOrigin `json:"recyclarr,omitempty"`
}

// IndexerPlan is the choice for one Prowlarr indexer.
type IndexerPlan struct {
	Key        string `json:"key"`
	ProwlarrID int    `json:"prowlarr_id"`
	Name       string `json:"name"`
	Definition string `json:"definition,omitempty"`
	// Choice is prowlarr (a Torznab feed through Prowlarr), native (the Cardigann catalog), existing (Theatarr has it)
	// or skip.
	Choice  string   `json:"choice"`
	Privacy string   `json:"privacy"`
	Private bool     `json:"private"`
	Notes   []string `json:"notes,omitempty"`
}

// Indexer choices.
const (
	ChoiceProwlarr = "prowlarr"
	ChoiceNative   = "native"
	ChoiceExisting = "existing"
	ChoiceSkip     = "skip"
)

// Step kinds. Every kind but manual is applied through the settings coordinator.
const (
	KindBudget   = "budget"
	KindIndexer  = "indexer"
	KindProvider = "provider"
	KindQuality  = "quality"
	KindVersion  = "version"
	KindView     = "view"
	KindManual   = "manual"
)

// Step statuses.
const (
	StatusPending      = "pending"
	StatusDone         = "done"
	StatusFailed       = "failed"
	StatusBlocked      = "blocked"
	StatusConflict     = "conflict"
	StatusConfirmation = "needs_confirmation"
	StatusManual       = "manual"
)

// Step is one change. Automatic steps run in apply; manual ones are for the human.
type Step struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	What       string   `json:"what"`
	Why        string   `json:"why"`
	Reversible bool     `json:"reversible"`
	Undo       string   `json:"undo"`
	Automatic  bool     `json:"automatic"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Payload    *Payload `json:"payload,omitempty"`
	// Blocked is why the step cannot run with this plan; fix it and plan again.
	Blocked string `json:"blocked,omitempty"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	// Confirmation holds the effects and token of a refused write; apply sends the token only when told to confirm
	// this step.
	Confirmation *settings.Confirmation `json:"confirmation,omitempty"`
	Generation   int64                  `json:"generation,omitempty"`
	AppliedAt    *time.Time             `json:"applied_at,omitempty"`
	Notes        []string               `json:"notes,omitempty"`
}

// Payload is what an automatic step writes. Config uses the policy files' field names.
type Payload struct {
	Key            string                     `json:"key"`
	Config         json.RawMessage            `json:"config,omitempty"`
	Priority       int                        `json:"priority,omitempty"`
	Naming         json.RawMessage            `json:"naming,omitempty"`
	FormatSet      string                     `json:"format_set,omitempty"`
	FormatConfig   json.RawMessage            `json:"format_config,omitempty"`
	Specifications map[string]json.RawMessage `json:"specifications,omitempty"`
	Origin         *settings.ProfileOrigin    `json:"origin,omitempty"`
	// Secret names the key the record's credential comes from at apply; never a value.
	Secret string `json:"secret,omitempty"`
	// Versions and Profiles are what a view step adds to the view's bindings.
	Versions []string            `json:"versions,omitempty"`
	Profiles map[string][]string `json:"profiles,omitempty"`
}

// Gap is something the migration cannot do automatically.
type Gap struct {
	Topic   string `json:"topic"`
	Subject string `json:"subject"`
	What    string `json:"what"`
	Why     string `json:"why"`
}

// Gap topics.
const (
	GapSecret      = "secret"
	GapUsenet      = "usenet"
	GapLocal       = "local_library"
	GapProfile     = "profile"
	GapPaths       = "paths"
	GapPrereq      = "prerequisite"
	GapUnsupported = "unsupported"
	GapUnread      = "unread"
)

// Checkpoint is a rollback point apply took before changing anything.
type Checkpoint struct {
	At time.Time `json:"at"`
	// Backup is the backup set's path without a suffix (theatarr backup restore -from takes it).
	Backup string `json:"backup"`
	// Settings is the settings export (YAML, credentials stripped) and Generation the settings generation then.
	Settings   string `json:"settings,omitempty"`
	Generation int64  `json:"generation"`
}

// BuildInput is what Build reads.
type BuildInput struct {
	Inventory *Inventory
	Options   PlanOptions
	// Policy is Theatarr's current policy: existing keys are reused or avoided, clients and views found.
	Policy *policy.Policy
	// Lookup finds a Cardigann catalog definition; nil keeps every indexer a Prowlarr feed.
	Lookup func(id string) (cardigann.Summary, bool)
	Now    time.Time
}

type builder struct {
	in       BuildInput
	pol      *policy.Policy
	plan     *Plan
	used     map[string]map[string]bool // section -> keys taken
	prowlarr map[int]string             // Prowlarr id -> planned indexer key
	steps    map[string]bool
}

// Build maps an inventory to a plan. It reads nothing but its input.
func Build(in BuildInput) (*Plan, error) {
	if in.Inventory == nil {
		return nil, errors.New("no inventory")
	}
	if in.Inventory.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("inventory schema_version %d, this Theatarr reads %d", in.Inventory.SchemaVersion, SchemaVersion)
	}
	for _, p := range in.Options.Providers {
		if providerDefaults[p.Kind] == nil {
			return nil, fmt.Errorf("provider kind %q: want realdebrid, alldebrid or torbox", p.Kind)
		}
		if p.Secret != "" && !keyNameRE.MatchString(p.Secret) {
			return nil, fmt.Errorf("provider %s: secret must name the key, not hold it", p.Kind)
		}
	}
	pol := in.Policy
	if pol == nil {
		pol = &policy.Policy{}
	}
	name := in.Options.Name
	if name == "" {
		name = "migration " + in.Now.UTC().Format("2006-01-02 15:04")
	}
	b := &builder{in: in, pol: pol, prowlarr: map[int]string{}, steps: map[string]bool{},
		plan: &Plan{SchemaVersion: SchemaVersion, Name: name, CreatedAt: in.Now.UTC(), UpdatedAt: in.Now.UTC(),
			Options: in.Options, Libraries: []Library{}, Indexers: []IndexerPlan{}, Steps: []Step{}, Gaps: []Gap{},
			Checkpoints: []Checkpoint{}}}
	b.used = map[string]map[string]bool{"indexers": keys(pol.Indexers), "providers": keys(pol.Providers),
		"budgets": keys(pol.Budgets), "versions": keys(pol.Versions), "profiles": keys(pol.Profiles),
		"format_sets": keys(pol.FormatSets)}
	b.unread()
	b.indexers()
	providers := b.providers()
	b.versions(providers)
	return b.plan, nil
}

func keys[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func (b *builder) gap(topic, subject, what, why string) {
	b.plan.Gaps = append(b.plan.Gaps, Gap{Topic: topic, Subject: subject, What: what, Why: why})
}

func (b *builder) add(s Step) {
	if b.steps[s.ID] {
		return
	}
	b.steps[s.ID] = true
	if s.Status == "" {
		s.Status = StatusPending
		if !s.Automatic {
			s.Status = StatusManual
		} else if s.Blocked != "" {
			s.Status = StatusBlocked
		}
	}
	b.plan.Steps = append(b.plan.Steps, s)
}

// unique returns base, or base-2, base-3... when base is taken in section, and takes it.
func (b *builder) unique(section, base string) string {
	key := base
	for n := 2; b.used[section][key]; n++ {
		key = fmt.Sprintf("%s-%d", base, n)
	}
	b.used[section][key] = true
	return key
}

func (b *builder) unread() {
	inv := b.in.Inventory
	for _, a := range inv.Arr {
		if a.Error != "" {
			b.gap(GapUnread, a.Name, "the instance could not be read: "+a.Error, "nothing is planned for it; fix the "+
				"address or key and inspect again")
		}
	}
	if inv.Prowlarr != nil && inv.Prowlarr.Error != "" {
		b.gap(GapUnread, "prowlarr", "Prowlarr could not be read: "+inv.Prowlarr.Error, "no indexers are planned")
	}
	if inv.Seerr != nil && inv.Seerr.Error != "" {
		b.gap(GapUnread, "seerr", "Seerr could not be read: "+inv.Seerr.Error, "no Seerr changes are planned")
	}
	if inv.Recyclarr != nil && inv.Recyclarr.Error != "" {
		b.gap(GapUnread, "recyclarr", "recyclarr.yml could not be read: "+inv.Recyclarr.Error, "profile origins are not known")
	}
	if inv.Seerr != nil {
		for _, srv := range append(append([]SeerrServer(nil), inv.Seerr.Radarr...), inv.Seerr.Sonarr...) {
			if srv.Instance == "" {
				b.gap(GapUnsupported, "seerr:"+srv.Name, fmt.Sprintf("Seerr sends requests to %s (%s:%d), which was not "+
					"inspected", srv.Name, srv.Hostname, srv.Port), "only the inspected instances get a version; the entry "+
					"keeps pointing at its arr")
			}
		}
	}
	if inv.Prowlarr != nil {
		for _, app := range inv.Prowlarr.Applications {
			if app.Instance == "" {
				b.gap(GapUnsupported, "prowlarr:"+app.Name, fmt.Sprintf("Prowlarr syncs to %s (%s), which was not "+
					"inspected", app.Name, app.Implementation), "only the Sonarr and Radarr instances given to inspect are "+
					"migrated; Prowlarr keeps serving the rest")
			}
		}
	}
}

// configJSON is v in the policy files' field names, as JSON.
func configJSON(v any) (json.RawMessage, error) {
	y, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := yaml.Unmarshal(y, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

// decodeConfig reads a payload config back into a policy struct, refusing unknown fields. JSON is YAML.
func decodeConfig(raw json.RawMessage, v any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	return dec.Decode(v)
}

func mustConfig(v any) json.RawMessage {
	b, err := configJSON(v)
	if err != nil {
		panic(err)
	}
	return b
}

var prowlarrBudget = policy.Budget{Requests: 60, Per: policy.Duration(time.Minute)}

func (b *builder) budget(key string, cfg policy.Budget, why string) string {
	if _, ok := b.pol.Budgets[key]; ok {
		return key
	}
	id := "budget/" + key
	if !b.steps[id] {
		b.used["budgets"][key] = true
		b.add(Step{ID: id, Kind: KindBudget, What: fmt.Sprintf("add budget %s (%d requests per %s)", key, cfg.Requests,
			time.Duration(cfg.Per)), Why: why, Reversible: true, Undo: "delete the budget in Settings › Budgets once no " +
			"source uses it", Automatic: true, Payload: &Payload{Key: key, Config: mustConfig(cfg)}})
	}
	return key
}

func (b *builder) wants(list []string, name string) bool {
	for _, n := range list {
		if strings.EqualFold(n, "all") || strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func (b *builder) indexers() {
	pr := b.in.Inventory.Prowlarr
	if pr == nil || pr.Error != "" {
		return
	}
	base := strings.TrimRight(b.in.Options.ProwlarrURL, "/")
	if base == "" {
		base = pr.URL
	}
	lookup := b.in.Lookup
	if lookup == nil {
		lookup = func(string) (cardigann.Summary, bool) { return cardigann.Summary{}, false }
	}
	proposals := map[int]cardigann.Proposal{}
	for _, p := range cardigann.ProposeFromProwlarr(pr.Indexers, lookup) {
		proposals[p.Prowlarr.ID] = p
	}
	byURL := map[string]string{}
	for k, ix := range b.pol.Indexers {
		byURL[strings.TrimRight(ix.URL, "/")] = k
	}
	list := append([]cardigann.ProwlarrIndexer(nil), pr.Indexers...)
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, pi := range list {
		privacy := proposals[pi.ID].Prowlarr.Privacy
		if privacy == "" {
			privacy = "private"
		}
		ip := IndexerPlan{ProwlarrID: pi.ID, Name: pi.Name, Definition: pi.DefinitionName, Privacy: privacy,
			Private: policy.Privacy(privacy).IsPrivate()}
		if strings.EqualFold(pi.Protocol, "usenet") {
			ip.Choice = ChoiceSkip
			ip.Notes = append(ip.Notes, "Usenet indexer")
			b.gap(GapUsenet, "indexer:"+pi.Name, "Usenet indexer "+pi.Name+" is not migrated",
				"Theatarr searches torrent indexers only; a Usenet streaming bridge (such as AltMount) keeps its own")
			b.plan.Indexers = append(b.plan.Indexers, ip)
			continue
		}
		if b.wants(b.in.Options.Skip, pi.Name) {
			ip.Choice = ChoiceSkip
			b.plan.Indexers = append(b.plan.Indexers, ip)
			continue
		}
		feed := fmt.Sprintf("%s/%d/api", base, pi.ID)
		if k, ok := byURL[feed]; ok {
			ip.Key, ip.Choice = k, ChoiceExisting
			ip.Private = b.pol.Indexers[k].Privacy.IsPrivate()
			b.prowlarr[pi.ID] = k
			b.plan.Indexers = append(b.plan.Indexers, ip)
			continue
		}
		prop := proposals[pi.ID]
		var cfg policy.Indexer
		step := Step{Kind: KindIndexer, Reversible: true, Automatic: true,
			Undo: "delete the indexer in " + uipages.Indexers + " (remove it from versions first)"}
		if b.wants(b.in.Options.Native, pi.Name) && prop.Status == cardigann.ProposalProposed && prop.Indexer != nil {
			ip.Choice, ip.Definition = ChoiceNative, prop.Indexer.Definition
			ip.Key = b.unique("indexers", prop.Key)
			ip.Privacy = prop.Indexer.Privacy
			ip.Private = policy.Privacy(ip.Privacy).IsPrivate()
			budget := b.budget(ip.Key, policy.Budget{Requests: 10, Per: policy.Duration(time.Minute)},
				"its own request budget, as every native site gets")
			cfg = policy.Indexer{Kind: "cardigann", Definition: prop.Indexer.Definition, URL: prop.Indexer.URL,
				Privacy: policy.Privacy(ip.Privacy), Budget: budget, Settings: prop.Indexer.Settings, Hosts: prop.Indexer.Hosts}
			step.DependsOn = []string{"budget/" + budget}
			step.What = fmt.Sprintf("add native indexer %s (definition %s, %s), disabled", ip.Key, ip.Definition, ip.Privacy)
			step.Why = fmt.Sprintf("Prowlarr's %s has a runnable catalog definition and was chosen to run natively", pi.Name)
			ip.Notes = append(ip.Notes, prop.Notes...)
			if len(prop.Secrets) > 0 {
				b.gap(GapSecret, "indexer:"+ip.Key, fmt.Sprintf("enter %s for native indexer %s ("+uipages.Indexers+", "+
					"credential as a JSON object)", strings.Join(prop.Secrets, ", "), ip.Key),
					"Prowlarr masks credentials in its API; they cannot be copied")
			}
		} else {
			if b.wants(b.in.Options.Native, pi.Name) {
				ip.Notes = append(ip.Notes, "kept as a Prowlarr feed: "+strings.Join(append([]string{prop.Status}, prop.Notes...), "; "))
			}
			ip.Choice = ChoiceProwlarr
			ip.Key = b.unique("indexers", "prowlarr-"+recyclarr.Slug(pi.Name))
			budget := b.budget("prowlarr", prowlarrBudget, "the Prowlarr feeds share one request budget")
			cfg = policy.Indexer{Kind: "torznab", URL: feed, Privacy: policy.Privacy(ip.Privacy), Budget: budget}
			step.DependsOn = []string{"budget/" + budget}
			step.What = fmt.Sprintf("add indexer %s: Prowlarr's %s as a Torznab feed (%s), disabled", ip.Key, pi.Name, ip.Privacy)
			step.Why = "Prowlarr feeds are the supported default; Prowlarr keeps the site's definition and login"
			step.Payload = &Payload{Secret: pr.Key}
		}
		if _, ok := b.pol.Budgets[cfg.Budget]; ok {
			step.DependsOn = nil
		}
		if !pi.Enable {
			ip.Notes = append(ip.Notes, "disabled in Prowlarr")
		}
		step.ID = "indexer/" + ip.Key
		if step.Payload == nil {
			step.Payload = &Payload{}
		}
		step.Payload.Key, step.Payload.Config = ip.Key, mustConfig(cfg)
		b.add(step)
		b.prowlarr[pi.ID] = ip.Key
		b.plan.Indexers = append(b.plan.Indexers, ip)
	}
}

type providerDefault struct {
	key    string
	url    string
	budget string
	limit  policy.Budget
	pacing policy.Pacing
}

// providerDefaults are the providers' API bases and the limits examples/policy uses (sourced there).
var providerDefaults = map[string]*providerDefault{
	"realdebrid": {key: "rd", url: "https://api.real-debrid.com/rest/1.0", budget: "rd-api",
		limit:  policy.Budget{Requests: 100, Per: policy.Duration(time.Minute)},
		pacing: policy.Pacing{Cooldown: policy.Duration(10 * time.Minute), BackoffMax: policy.Duration(2 * time.Hour)}},
	"alldebrid": {key: "ad", url: "https://api.alldebrid.com/v4", budget: "ad-api",
		limit:  policy.Budget{Requests: 300, Per: policy.Duration(time.Minute)},
		pacing: policy.Pacing{Cooldown: policy.Duration(5 * time.Minute), BackoffMax: policy.Duration(2 * time.Hour)}},
	"torbox": {key: "tb", url: "https://api.torbox.app/v1", budget: "torbox-api",
		limit:  policy.Budget{Requests: 250, Per: policy.Duration(time.Minute)},
		pacing: policy.Pacing{UncachedPerHr: 60, Cooldown: policy.Duration(5 * time.Minute), BackoffMax: policy.Duration(2 * time.Hour)}},
}

// providers plans the debrid accounts and returns every provider key the versions use, in fallback order.
func (b *builder) providers() []string {
	type entry struct {
		key   string
		order int
	}
	var all []entry
	maxOrder := 0
	byKind := map[string]string{}
	for k, p := range b.pol.Providers {
		all = append(all, entry{k, p.Order})
		maxOrder = max(maxOrder, p.Order)
		if byKind[p.Kind] == "" || k < byKind[p.Kind] {
			byKind[p.Kind] = k
		}
	}
	for _, c := range b.in.Options.Providers {
		if k := byKind[c.Kind]; k != "" && c.Key == "" {
			continue
		}
		d := providerDefaults[c.Kind]
		key := c.Key
		if key == "" {
			key = d.key
		}
		if b.used["providers"][key] {
			continue
		}
		b.used["providers"][key] = true
		byKind[c.Kind] = key
		maxOrder++
		budget := b.budget(d.budget, d.limit, "the provider's API budget, below its published limit")
		cfg := policy.Provider{Kind: c.Kind, Order: maxOrder, URL: d.url, Budget: budget, Pacing: d.pacing}
		s := Step{ID: "provider/" + key, Kind: KindProvider, What: fmt.Sprintf("add debrid provider %s (%s, fallback %d), "+
			"disabled", key, c.Kind, maxOrder), Why: "versions add through the debrid accounts the bridge used",
			Reversible: true, Undo: "delete the provider in Settings › Providers", Automatic: true,
			Payload: &Payload{Key: key, Config: mustConfig(cfg), Secret: c.Secret}}
		if _, ok := b.pol.Budgets[budget]; !ok {
			s.DependsOn = []string{"budget/" + budget}
		}
		b.add(s)
		if c.Secret == "" {
			b.gap(GapSecret, "provider:"+key, "enter the "+c.Kind+" API key for provider "+key+" (Settings › Providers)",
				"debrid keys live in the bridge's own config, which the migration does not read")
		}
		all = append(all, entry{key, maxOrder})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].order < all[j].order })
	out := make([]string, 0, len(all))
	for _, e := range all {
		out = append(out, e.key)
	}
	return out
}

func (b *builder) mapPath(p string) (string, bool) {
	maps := append([]PathMapping(nil), b.in.Options.PathMap...)
	sort.Slice(maps, func(i, j int) bool { return len(maps[i].From) > len(maps[j].From) })
	for _, m := range maps {
		from := strings.TrimRight(m.From, "/")
		if p == from || strings.HasPrefix(p, from+"/") {
			return strings.TrimRight(m.To, "/") + strings.TrimPrefix(p, from), true
		}
	}
	return p, false
}

// libraryArea is where version libraries may live (library.dangling.roots), if the policy says.
func (b *builder) libraryArea() []string {
	if b.pol.Library == nil {
		return nil
	}
	return b.pol.Library.Dangling.Roots
}

func isAnime(a ArrInstance) bool {
	if strings.Contains(strings.ToLower(a.Name), "anime") {
		return true
	}
	if a.Flavour == "sonarr" && a.Titles.Total > 0 && a.Titles.Anime*2 > a.Titles.Total {
		return true
	}
	for _, r := range a.RootFolders {
		if strings.Contains(strings.ToLower(r.Path), "anime") {
			return true
		}
	}
	return false
}

// versionBase is the version key an instance suggests: kind, anime and what is left of its name (radarr_4k_debrid ->
// movie-4k-zurg).
func versionBase(a ArrInstance, anime bool) string {
	base := "movie"
	if a.Flavour == "sonarr" {
		base = "tv"
	}
	if anime {
		base = "anime-" + base
	}
	var rest []string
	for _, w := range strings.FieldsFunc(strings.ToLower(a.Name), func(r rune) bool { return r == '_' || r == '-' }) {
		if w != "radarr" && w != "sonarr" && w != "anime" && w != "" {
			rest = append(rest, recyclarr.Slug(w))
		}
	}
	if len(rest) > 0 {
		base += "-" + strings.Join(rest, "-")
	}
	return base
}

func (b *builder) versions(providers []string) {
	inv := b.in.Inventory
	animeFlavour := map[string]bool{}
	for _, a := range inv.Arr {
		if a.Error == "" && isAnime(a) {
			animeFlavour[a.Flavour] = true
		}
	}
	// A debrid version needs no download client (the router adds to the providers itself); one already set up is
	// named so the version keeps it, as before.
	client := ""
	for _, k := range sortedKeys(b.pol.Clients) {
		if b.pol.Clients[k].Kind == "decypharr" {
			client = k
			break
		}
	}
	if len(providers) == 0 && len(inv.Arr) > 0 {
		b.gap(GapPrereq, "providers", "no debrid provider: give the accounts the bridge uses to plan (-provider "+
			"realdebrid=KEYNAME) or add one in Settings › Providers, then plan again",
			"a debrid version lists the providers it adds through")
	}
	planned := []string{}
	for _, ip := range b.plan.Indexers {
		if ip.Key != "" && ip.Choice != ChoiceSkip {
			planned = append(planned, ip.Key)
		}
	}
	feeding := map[string]string{} // instance -> existing version key
	if b.pol.Import != nil {
		for inst, src := range b.pol.Import.Arr {
			if b.pol.Versions[src.Version] != nil {
				feeding[inst] = src.Version
			}
		}
	}
	libs := map[string]*Library{}
	for _, a := range inv.Arr {
		if a.Error != "" {
			continue
		}
		anime := isAnime(a)
		kind := policy.KindMovie
		if a.Flavour == "sonarr" {
			kind = policy.KindSeries
		}
		libName := map[policy.Kind]string{policy.KindMovie: "movies", policy.KindSeries: "series"}[kind]
		var animePtr *bool
		if anime {
			libName = "anime-" + libName
			animePtr = ptr(true)
		} else if animeFlavour[a.Flavour] {
			animePtr = ptr(false)
		}
		lib := libs[libName]
		if lib == nil {
			lib = &Library{Name: libName, Kind: kind, Anime: animePtr}
			libs[libName] = lib
		}
		vp := b.version(a, kind, animePtr, providers, client, planned)
		if k, ok := feeding[a.Name]; ok {
			vp.Key, vp.Existing = k, true
		}
		lib.Versions = append(lib.Versions, vp)
	}
	for _, name := range sortedKeys(libs) {
		b.plan.Libraries = append(b.plan.Libraries, *libs[name])
	}
}

func ptr[T any](v T) *T { return &v }

// version plans one instance's version, its quality step and its manual steps.
func (b *builder) version(a ArrInstance, kind policy.Kind, anime *bool, providers []string, client string, planned []string) VersionPlan {
	inv := b.in.Inventory
	if src := b.pol.Import; src != nil && src.Arr[a.Name] != nil && b.pol.Versions[src.Arr[a.Name].Version] != nil {
		key := src.Arr[a.Name].Version
		vp := VersionPlan{Key: key, Instance: a.Name, Existing: true, Library: b.pol.Versions[key].Library,
			Profile: b.pol.Versions[key].Profile}
		b.seerrSteps(a, &vp)
		return vp
	}
	key := b.unique("versions", versionBase(a, anime != nil && *anime))
	vp := VersionPlan{Key: key, Instance: a.Name, Providers: providers, Client: client}
	// The root: the one with the most titles.
	roots := append([]RootFolder(nil), a.RootFolders...)
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Titles > roots[j].Titles })
	if len(roots) > 0 {
		vp.ArrRoot = roots[0].Path
		var mapped bool
		vp.Library, mapped = b.mapPath(roots[0].Path)
		if !mapped && len(b.in.Options.PathMap) > 0 {
			b.gap(GapPaths, "version:"+key, fmt.Sprintf("no path mapping covers %s; the version uses it as is", roots[0].Path),
				"Theatarr must see the library at the path the version names")
		}
		for _, r := range roots[1:] {
			if r.Titles > 0 {
				b.gap(GapPaths, "version:"+key, fmt.Sprintf("%s also has %d titles under %s; the version takes %s", a.Name,
					r.Titles, r.Path, roots[0].Path), "a version has one library root: add a second version or move them")
			}
		}
		if area := b.libraryArea(); vp.Library != "" && len(area) > 0 && !slices.ContainsFunc(area, func(r string) bool {
			r = strings.TrimRight(r, "/")
			return vp.Library == r || strings.HasPrefix(vp.Library, r+"/")
		}) {
			b.gap(GapPaths, "version:"+key, fmt.Sprintf("%s is outside the library area (%s)", vp.Library,
				strings.Join(area, ", ")), "a version's library must be inside library.dangling.roots in the policy "+
				"files; map the path (-path-map) or add the area, then plan again")
		}
	} else {
		b.gap(GapPaths, "version:"+key, a.Name+" has no root folder", "a version needs a library root")
	}
	// The profile: the one Seerr asks for, else the one most titles use.
	profiles := append([]NamedCount(nil), a.QualityProfiles...)
	sort.SliceStable(profiles, func(i, j int) bool { return profiles[i].Titles > profiles[j].Titles })
	if inv.Seerr != nil {
		for _, s := range append(append([]SeerrServer(nil), inv.Seerr.Radarr...), inv.Seerr.Sonarr...) {
			if s.Instance == a.Name && s.ActiveProfileName != "" {
				for i, p := range profiles {
					if p.Name == s.ActiveProfileName {
						profiles[0], profiles[i] = profiles[i], profiles[0]
					}
				}
				break
			}
		}
	}
	for _, p := range profiles {
		if p.Name != "" {
			vp.ArrProfile = p.Name
			break
		}
	}
	vp.Recyclarr = b.recyclarrOrigin(a, vp.ArrProfile)
	quality, naming, qerr := b.quality(a, vp.ArrProfile, vp.Recyclarr)
	if qerr != "" {
		b.gap(GapProfile, "version:"+key, fmt.Sprintf("%s's profile %q could not be frozen: %s", a.Name, vp.ArrProfile, qerr),
			"pick or build a profile for the library ("+uipages.Profiles+", a TRaSH preset) and plan again")
	} else {
		vp.Profile = quality.Payload.Key
	}
	// Indexers: the planned ones Prowlarr syncs to this instance, else every one planned.
	var ids []int
	for _, ix := range a.Indexers {
		if ix.ProwlarrID > 0 {
			ids = append(ids, ix.ProwlarrID)
		} else if !strings.EqualFold(ix.Protocol, "usenet") {
			b.gap(GapSecret, "indexer:"+a.Name+"/"+ix.Name, fmt.Sprintf("%s's indexer %s is set in the arr itself, not "+
				"through Prowlarr: add it to Theatarr by hand", a.Name, ix.Name), "the arrs mask indexer keys")
		}
	}
	for _, k := range planned {
		for _, id := range ids {
			if b.prowlarr[id] == k && !slices.Contains(vp.Indexers, k) {
				vp.Indexers = append(vp.Indexers, k)
			}
		}
	}
	if len(vp.Indexers) == 0 {
		vp.Indexers = append([]string(nil), planned...)
	}
	b.clientGaps(a, key)
	if a.DelayProfiles > 0 || a.ReleaseProfiles > 0 {
		b.gap(GapUnsupported, a.Name, fmt.Sprintf("%s has %d delay and %d release profiles", a.Name, a.DelayProfiles,
			a.ReleaseProfiles), "Theatarr has neither; express must-not-contain terms as custom formats with negative scores")
	}

	v := policy.VersionPolicy{Mode: policy.ModeShadow, Kind: kind, Anime: anime, Enable: "default", Profile: vp.Profile,
		Library: vp.Library, Storage: policy.StorageDebrid, Indexers: vp.Indexers, Providers: vp.Providers,
		Client: vp.Client, Import: "symlink"}
	step := Step{ID: "version/" + key, Kind: KindVersion, What: fmt.Sprintf("add version %s for %s: shadow, grab off, "+
		"library %s", key, a.Name, vp.Library), Why: fmt.Sprintf("one version per arr instance; %s is %s in the %s library",
		a.Name, key, map[bool]string{true: "anime", false: string(kind)}[anime != nil && *anime]),
		Reversible: true, Undo: "delete the library in " + uipages.Libraries + " (it has no links while in shadow)",
		Automatic: true, Payload: &Payload{Key: key, Config: mustConfig(v)}}
	if naming != nil {
		step.Payload.Naming = mustConfig(*naming)
	} else {
		step.Payload.Naming = mustConfig(policy.Naming{})
		step.Notes = append(step.Notes, "naming: the arr default names (the arr's naming could not be frozen)")
	}
	for _, k := range vp.Indexers {
		if b.steps["indexer/"+k] {
			step.DependsOn = append(step.DependsOn, "indexer/"+k)
		}
	}
	for _, k := range vp.Providers {
		if b.steps["provider/"+k] {
			step.DependsOn = append(step.DependsOn, "provider/"+k)
		}
	}
	switch {
	case qerr != "":
		step.Blocked = "no profile: " + qerr
	case len(vp.Providers) == 0:
		step.Blocked = "no debrid provider"
	case len(vp.Indexers) == 0:
		step.Blocked = "no indexer"
	case vp.Library == "":
		step.Blocked = "no library root"
	}
	if quality != nil {
		b.add(*quality)
		step.DependsOn = append([]string{quality.ID}, step.DependsOn...)
	}
	b.add(step)
	b.view(a, &vp)
	b.manualSteps(a, &vp)
	b.seerrSteps(a, &vp)
	return vp
}

// recyclarrOrigin is the TRaSH profile Recyclarr builds the instance's profile from.
func (b *builder) recyclarrOrigin(a ArrInstance, profile string) *settings.ProfileOrigin {
	r := b.in.Inventory.Recyclarr
	if r == nil {
		return nil
	}
	for _, ri := range r.Instances {
		if ri.Instance != a.Name {
			continue
		}
		for _, p := range ri.Profiles {
			if p.Name == profile && p.TrashID != "" {
				return &settings.ProfileOrigin{Preset: p.TrashID, Name: p.Name, Flavour: a.Flavour, ScoreSet: p.ScoreSet}
			}
		}
	}
	return nil
}

// quality freezes the instance's profile (freezeArr) into a quality step, and its naming.
func (b *builder) quality(a ArrInstance, profile string, origin *settings.ProfileOrigin) (*Step, *policy.Naming, string) {
	if profile == "" {
		return nil, nil, "the instance has no quality profile"
	}
	e, err := freezeArr(a.Name, a.Flavour, a.Raw, profile)
	var naming *policy.Naming
	if err == nil {
		naming = e.Naming
	} else if a.Raw["config/naming"] != nil {
		raw := map[string]json.RawMessage{}
		for k, v := range a.Raw {
			if k != "config/naming" {
				raw[k] = v
			}
		}
		e, err = freezeArr(a.Name, a.Flavour, raw, profile)
	}
	if err != nil {
		return nil, nil, err.Error()
	}
	profiles, sets, err := e.Fragments()
	if err != nil || len(profiles) != 1 || len(sets) != 1 {
		return nil, nil, fmt.Sprintf("unexpected export (%d profiles, %d format sets, %v)", len(profiles), len(sets), err)
	}
	pkey := sortedKeys(profiles)[0]
	skey := sortedKeys(sets)[0]
	set := sets[skey]
	specs := map[string]json.RawMessage{}
	for name, ref := range set.Formats {
		if ref == nil {
			continue
		}
		specs[name] = json.RawMessage(e.Files[ref.File])
		ref.File = ""
	}
	s := &Step{ID: "quality/" + pkey, Kind: KindQuality, What: fmt.Sprintf("add profile %s and format set %s: %s's "+
		"profile %q frozen with its %d custom formats", pkey, skey, a.Name, profile, len(set.Formats)),
		Why: "the version decides with the arr's own profile and formats, so shadow agreement measures Theatarr " +
			"against the arr", Reversible: true,
		Undo: "delete the profile and format set in " + uipages.Profiles + " once no library uses them", Automatic: true,
		Payload: &Payload{Key: pkey, Config: mustConfig(profiles[pkey]), FormatSet: skey, FormatConfig: mustConfig(set),
			Specifications: specs, Origin: origin}}
	if origin != nil {
		s.Notes = append(s.Notes, fmt.Sprintf("Recyclarr builds it from TRaSH profile %s; bind it to TRaSH sync in "+
			uipages.QualitySync+" to replace Recyclarr", origin.Preset))
	}
	s.Notes = append(s.Notes, e.Unsupported...)
	return s, naming, ""
}

func (b *builder) clientGaps(a ArrInstance, key string) {
	bridge := false
	for _, c := range a.DownloadClients {
		if !c.Enabled {
			continue
		}
		switch {
		case strings.EqualFold(c.Protocol, "usenet"):
			b.gap(GapUsenet, a.Name+"/"+c.Name, fmt.Sprintf("%s downloads through Usenet client %s (%s)", a.Name, c.Name,
				c.Implementation), "Theatarr adds torrents to debrid only; Usenet grabs stay with the arr or the bridge")
		case c.Bridge != "":
			bridge = true
		default:
			b.gap(GapUnsupported, a.Name+"/"+c.Name, fmt.Sprintf("%s's client %s (%s) is not a debrid bridge", a.Name, c.Name,
				c.Implementation), "Theatarr's own downloads go to debrid; a local or seedbox client is not migrated")
		}
	}
	if !bridge {
		b.gap(GapLocal, "version:"+key, fmt.Sprintf("%s's files do not come from a debrid bridge (local library)", a.Name),
			"Theatarr links from a debrid mount: shadow comparison works, but adopting the files only works for files on "+
				"the mount, and hardlinked or copied libraries are not taken over")
	} else if a.Hardlinks {
		b.gap(GapLocal, "version:"+key, a.Name+" imports with hardlinks", "Theatarr's debrid versions symlink into the mount")
	}
}

// view adds the version to the arr view of its flavour, with the profile names Seerr and the arr use.
func (b *builder) view(a ArrInstance, vp *VersionPlan) {
	var candidates []string
	for _, k := range sortedKeys(b.pol.Views) {
		if b.pol.Views[k].Flavour == a.Flavour {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) == 0 {
		b.gap(GapPrereq, "views:"+a.Flavour, fmt.Sprintf("add a %s view in "+uipages.Apps+" (flavour %s, "+
			"path, API key) for Seerr and other clients, then plan again", a.Flavour, a.Flavour),
			"views and their keys are settings records")
		return
	}
	view := candidates[0]
	for _, k := range candidates {
		if b.pol.Views[k].Path == "/"+a.Flavour {
			view = k
		}
	}
	vp.View = view
	profiles := map[string][]string{}
	names := []string{}
	if s := b.in.Inventory.Seerr; s != nil {
		for _, srv := range append(append([]SeerrServer(nil), s.Radarr...), s.Sonarr...) {
			if srv.Instance == a.Name {
				for _, n := range []string{srv.ActiveProfileName, srv.ActiveAnimeProfileName} {
					if n != "" && !slices.Contains(names, n) {
						names = append(names, n)
					}
				}
			}
		}
	}
	if len(names) == 0 && vp.ArrProfile != "" {
		names = append(names, vp.ArrProfile)
	}
	for _, n := range names {
		profiles[n] = []string{vp.Key}
	}
	id := "view/" + view + "/" + vp.Key
	b.add(Step{ID: id, Kind: KindView, What: fmt.Sprintf("serve %s through view %s (profiles %s)", vp.Key, view,
		strings.Join(names, ", ")), Why: "Seerr and the other arr clients reach the version through the view at cutover",
		Reversible: true, Undo: "remove the library from the view in " + uipages.Libraries + " (view bindings)", Automatic: true,
		DependsOn: []string{"version/" + vp.Key}, Payload: &Payload{Key: view, Versions: []string{vp.Key}, Profiles: profiles}})
}

func (b *builder) manualSteps(a ArrInstance, vp *VersionPlan) {
	env := "THEATARR_ARR_" + strings.ToUpper(strings.ReplaceAll(a.Name, "-", "_")) + "_KEY"
	b.add(Step{ID: "import/" + a.Name, Kind: KindManual, What: fmt.Sprintf("in "+uipages.Migration+
		", Arr instances (import), add arr.%s (flavour: %s, url: %s, api_key_env: %s, version: %s) and save; open it again "+
		"and enter the credential arr.%s.api_key (stored write-only; without it Theatarr reads %s from its "+
		"environment). It applies without a restart; theatarr import-arr then runs on its schedule (or run it once)",
		a.Name, a.Flavour, a.URL, env, vp.Key, a.Name, env),
		Why: "the import copies titles, monitoring, files and history, and the shadow runner compares the arr's grabs " +
			"with the version's decisions", Reversible: true, Undo: "remove the arr." + a.Name + " entry from the " +
			"import section in " + uipages.Settings + "; imported titles stay", DependsOn: []string{"version/" + vp.Key}})
	bridge := false
	for _, c := range a.DownloadClients {
		bridge = bridge || (c.Enabled && c.Bridge != "")
	}
	if bridge {
		follower := vp.Key + "-theatarr"
		b.add(Step{ID: "follow/" + vp.Key, Kind: KindManual, What: fmt.Sprintf("optional, the parallel path "+
			"(docs/migration.md#3-a-follower-library-beside-the-arr): add version %s following %s on a new root of its own, then theatarr library adopt "+
			"-version %s -dry-run, and without -dry-run once the report is clean", follower, vp.Key, follower),
			Why: "a follower builds Theatarr's own library beside the arr's from the same files, without taking " +
				"anything over", Reversible: true, Undo: "delete the follower; its links stay until removed by hand",
			DependsOn: []string{"import/" + a.Name}})
	}
}

func (b *builder) seerrSteps(a ArrInstance, vp *VersionPlan) {
	s := b.in.Inventory.Seerr
	if s == nil {
		return
	}
	for _, list := range [][]SeerrServer{s.Radarr, s.Sonarr} {
		for _, srv := range list {
			if srv.Instance != a.Name {
				continue
			}
			vp.Seerr = append(vp.Seerr, srv.Name)
			path, keyEnv := "/"+a.Flavour, "the view's API key ("+uipages.Apps+")"
			if v := b.pol.Views[vp.View]; v != nil {
				path = v.Path
			}
			b.add(Step{ID: fmt.Sprintf("seerr/%s/%d", a.Flavour, srv.ID), Kind: KindManual, What: fmt.Sprintf("at "+
				"cutover: in Seerr, edit %s server %q: Theatarr's host and port, URL base %s, API key %s, profile %q",
				a.Flavour, srv.Name, path, keyEnv, srv.ActiveProfileName), Why: "Seerr stays; it sends requests to " +
				"Theatarr's arr-compatible view instead of the arr", Reversible: true,
				Undo: fmt.Sprintf("point the entry back at %s", a.URL), DependsOn: []string{"view/" + vp.View + "/" + vp.Key}})
		}
	}
}

// Step returns the plan's step by id.
func (p *Plan) Step(id string) *Step {
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return &p.Steps[i]
		}
	}
	return nil
}

// Versions lists the plan's version mappings.
func (p *Plan) Versions() []VersionPlan {
	var out []VersionPlan
	for _, l := range p.Libraries {
		out = append(out, l.Versions...)
	}
	return out
}
