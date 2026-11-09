package web

// SchemaField describes one field of a settings record for the UI's forms. Config fields use the policy's YAML
// names; record fields (enabled, priority, secret) sit beside config in a request.
type SchemaField struct {
	Name        string   `json:"name"`
	Path        string   `json:"path,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Default     string   `json:"default,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Label       string   `json:"label,omitempty"`
	Group       string   `json:"group,omitempty"`
	Advanced    bool     `json:"advanced,omitempty"`
	ZeroAllowed bool     `json:"zero_allowed,omitempty"`
	// Type is string, integer, boolean, duration ("90s", "1h"), enum, list (of strings), map (string to integer),
	// string_map (string to string), object or secret (write-only).
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	// Kinds limits the field to these kinds; empty means every kind.
	Kinds []string `json:"kinds,omitempty"`
	Help  string   `json:"help"`
	// Nullable fields are sent as null when cleared, which takes their default (an integer's 0 means something else).
	Nullable bool          `json:"nullable,omitempty"`
	Fields   []SchemaField `json:"fields,omitempty"`
}

// SchemaEntity is one record type.
type SchemaEntity struct {
	Kinds []string `json:"kinds,omitempty"`
	// Record are the fields beside config: enabled, priority, secret.
	Record []SchemaField `json:"record"`
	Config []SchemaField `json:"config"`
	// Tests says whether POST .../test and .../testall exist for this record type.
	Tests bool `json:"tests"`
}

// SettingsSchema is GET /settings/schema.
type SettingsSchema struct {
	SectionNullable map[string]bool          `json:"section_nullable"`
	Sections        map[string][]SchemaField `json:"sections"`
	Indexers        SchemaEntity             `json:"indexers"`
	Providers       SchemaEntity             `json:"providers"`
	Budgets         SchemaEntity             `json:"budgets"`
	// Rules are the write rules every form must follow.
	Rules []string `json:"rules"`
}

func settingsSchema() SettingsSchema {
	secret := SchemaField{Name: "secret", Type: "secret", Help: "API key or token. Write-only: responses show secret_set. " +
		"Omit to keep the stored value; clear_secret: true removes it."}
	clear := SchemaField{Name: "clear_secret", Type: "boolean", Help: "Remove the stored credential."}
	enabled := SchemaField{Name: "enabled", Type: "boolean", Help: "Disabled records stay validated but are not used. New records default to disabled."}
	budget := SchemaField{Name: "budget", Type: "string", Help: "Key of a budget shared by the sources that name it."}
	indexerKinds := []string{"torznab", "dmm", "torrentio", "zilean", "seadex", "cardigann"}
	cardigann := []string{"cardigann"}
	return SettingsSchema{
		Sections:        sectionSchemas(),
		SectionNullable: sectionNullable(),
		Indexers: SchemaEntity{Kinds: indexerKinds, Tests: true,
			Record: []SchemaField{enabled,
				{Name: "priority", Type: "integer", Help: "Search order; lower numbers are searched first. Default 0."},
				secret, clear},
			Config: []SchemaField{
				{Name: "kind", Type: "enum", Required: true, Enum: indexerKinds, Help: "Adapter: torznab (Prowlarr feeds), dmm, torrentio, zilean, seadex, or cardigann (a catalog definition run natively)."},
				{Name: "url", Type: "string", Required: true, Help: "Endpoint URL. Every outbound URL is configured; nothing has a default."},
				{Name: "privacy", Type: "enum", Enum: []string{"public", "private", "semi-private"}, Help: "A label for badges, filters and sorting. Unset shows as private, except on the built-in public sources (Torrentio, DMM, Zilean, SeaDex), where it shows as public."},
				{Name: "providers", Type: "list", Help: "Providers this indexer's releases may go to (provider keys); empty means every provider of the library. The TorBox cache check only sends its hashes when TorBox is allowed."},
				budget,
				{Name: "page_cap", Type: "integer", Help: "Pages per search; 0 means no cap."},
				{Name: "min_seeders", Type: "integer", Nullable: true, Help: "Torrent minimum; default 1 (leave empty), 0 disables the check."},
				{Name: "multi_languages", Type: "list", Help: "Languages a MULTI release holds, by arr language name."},
				{Name: "arr_names", Type: "list", Help: "Other names for this indexer, as the arrs' history or a release push gives them (e.g. \"DMM RD (Prowlarr)\"); matched without regard to case."},
				{Name: "proxy", Type: "string", Help: "Optional egress proxy (http:// or socks5://); the VPN guard checks the exit through it."},
				{Name: "seadex", Type: "object", Kinds: []string{"seadex"}, Help: "SeaDex scoring.", Fields: []SchemaField{
					{Name: "best", Type: "integer", Nullable: true, Help: "Weight of entries marked best. Default 100."},
					{Name: "dual_audio", Type: "integer", Help: "Weight of dual-audio entries."},
					{Name: "size_weight", Type: "integer", Help: "Below 0 favours smaller entries, above 0 larger ones."},
					{Name: "exclude_tags", Type: "list", Help: "releases.moe tags that drop an entry."},
					{Name: "tags", Type: "map", Help: "Weights by releases.moe tag."},
				}},
				{Name: "definition", Type: "string", Kinds: cardigann, Help: "Catalog id of the definition this indexer runs (GET /cardigann/definitions)."},
				{Name: "settings", Type: "string_map", Kinds: cardigann, Help: "The definition's non-secret settings, name=value. Secret ones (password, cookie, API key) go in the credential as a JSON object."},
				{Name: "hosts", Type: "list", Kinds: cardigann, Help: "Hosts other than the site's that the definition reaches (the catalog lists them); they join the gateway's allowlist."},
			}},
		Providers: SchemaEntity{Kinds: []string{"realdebrid", "alldebrid", "torbox"}, Tests: true,
			Record: []SchemaField{enabled, secret, clear,
				{Name: "orphan_active_downloads", Type: "boolean", Required: true, Help: "Required on provider updates, deletes and imports: " +
					"true confirms that disabling or removing a provider fails its active downloads; false refuses such a save."}},
			Config: []SchemaField{
				{Name: "kind", Type: "enum", Required: true, Enum: []string{"realdebrid", "alldebrid", "torbox"}, Help: "Debrid service."},
				{Name: "order", Type: "integer", Required: true, Help: "Fallback rank, 1 first, unique. Libraries list providers in this order."},
				{Name: "url", Type: "string", Required: true, Help: "Provider API base URL."},
				budget,
				{Name: "pacing", Type: "object", Help: "Limits on adds.", Fields: []SchemaField{
					{Name: "adds_per_minute", Type: "integer", Help: "Adds per minute."},
					{Name: "uncached_adds_per_hour", Type: "integer", Help: "Uncached adds per hour."},
					{Name: "cooldown", Type: "duration", Help: "Pause after a refusal."},
					{Name: "backoff_max", Type: "duration", Help: "Longest back-off after failures."},
					{Name: "min_gap", Type: "duration", Nullable: true, Help: "Least time between two adds. Default: Real-Debrid 20s (measured), none elsewhere; 0s off, at most 10m."},
				}},
				{Name: "stalled_after", Type: "duration", Nullable: true, Help: "This provider's stall timeout (router.stalled_after). Default: AllDebrid at least 25m (healthy uncached jobs sit at zero for about 10 minutes), the others the router's value; 0s off, else 1m to 24h."},
				{Name: "magnet_limit", Type: "integer", Nullable: true, Kinds: []string{"alldebrid"}, Help: "How many magnets the account may hold, for Admin › Torrents and a health warning at 90%. Report only. Default 5000, 1 to 1000000."},
			}},
		Budgets: SchemaEntity{
			Record: []SchemaField{},
			Config: []SchemaField{
				{Name: "requests", Type: "integer", Required: true, Help: "Requests per period (token refill)."},
				{Name: "per", Type: "duration", Required: true, Help: "The period, such as 1m or 1h."},
				{Name: "burst", Type: "integer", Help: "Bucket size; default 1."},
				{Name: "hourly", Type: "integer", Help: "Hard cap per UTC hour; 0 none."},
				{Name: "daily", Type: "integer", Help: "Hard cap per UTC day; 0 none, not below hourly."},
			}},
		Rules: []string{
			"Every write sends generation (from the last read); a stale generation answers 409.",
			"Unknown fields are refused. Global switches, library modes, views (/settings/view-bindings) and the other policy sections (/settings/sections) have their own settings editors and confirmations.",
			"Secrets are write-only; *_env names are import-only.",
			"Tests make outbound calls through the gateway (configured VPN requirements, budgets, back-offs); they never save or enable a source. No egress section turns the VPN guard off; require_vpn_for: [] monitors only; omitted require_vpn_for defaults to [indexers].",
			"Testing changes to a saved source at a new scheme, host or port needs secret again; the stored credential only goes to the saved address.",
		},
	}
}
