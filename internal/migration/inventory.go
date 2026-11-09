// Package migration is the guided move from an existing arr stack to Theatarr (docs/migration.md): inspect reads the
// old services with GET only and returns an inventory; plan maps it to Theatarr settings and saves the plan in the
// database; apply runs the plan's automatic steps through the settings coordinator, after a rollback checkpoint, with
// everything landing disabled or in shadow mode; verify reports per version whether it is ready to hand over.
//
// API keys are named, never carried: sources refer to each key by a name the caller resolves (Keys), the inventory
// and the plan hold only those names, and nothing here logs or returns a key. The old services are never written:
// every request goes through internal/arr's GET-only clients.
package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/recyclarr"
	"go.yaml.in/yaml/v3"
)

// SchemaVersion is the version of the inventory, plan and verify JSON. It changes only with an incompatible change.
const SchemaVersion = 1

// Sources are the old services to read. Key fields name the API key (resolved through Keys), never hold it.
type Sources struct {
	Arr      []ArrSource    `json:"arr" yaml:"arr"`
	Prowlarr *ServiceSource `json:"prowlarr,omitempty" yaml:"prowlarr"`
	Seerr    *ServiceSource `json:"seerr,omitempty" yaml:"seerr"`
	// Recyclarr is the path of a recyclarr.yml to read, if any.
	Recyclarr string `json:"recyclarr,omitempty" yaml:"recyclarr"`
}

// ArrSource is one Sonarr or Radarr instance.
type ArrSource struct {
	Name    string `json:"name" yaml:"name"`
	Flavour string `json:"flavour" yaml:"flavour"`
	URL     string `json:"url" yaml:"url"`
	Key     string `json:"key" yaml:"key"`
}

// ServiceSource is Prowlarr or Seerr.
type ServiceSource struct {
	URL string `json:"url" yaml:"url"`
	Key string `json:"key" yaml:"key"`
}

// Keys resolves a key name to its value.
type Keys func(name string) (string, bool)

// MapKeys is Keys over a map.
func MapKeys(m map[string]string) Keys {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok && v != ""
	}
}

var keyNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
var instanceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Validate checks the sources before anything is read.
func (s *Sources) Validate() error {
	if len(s.Arr) == 0 && s.Prowlarr == nil && s.Seerr == nil && s.Recyclarr == "" {
		return errors.New("no sources: give at least one arr instance, Prowlarr, Seerr or a recyclarr.yml")
	}
	seen := map[string]bool{}
	for i, a := range s.Arr {
		p := fmt.Sprintf("arr[%d]", i)
		if !instanceRE.MatchString(a.Name) {
			return fmt.Errorf("%s.name %q: lower case letters, digits, '-' or '_'", p, a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("%s.name %q listed twice", p, a.Name)
		}
		seen[a.Name] = true
		if a.Flavour != "radarr" && a.Flavour != "sonarr" {
			return fmt.Errorf("%s.flavour must be radarr or sonarr, got %q", p, a.Flavour)
		}
		if err := checkSource(p, a.URL, a.Key); err != nil {
			return err
		}
	}
	if s.Prowlarr != nil {
		if err := checkSource("prowlarr", s.Prowlarr.URL, s.Prowlarr.Key); err != nil {
			return err
		}
	}
	if s.Seerr != nil {
		if err := checkSource("seerr", s.Seerr.URL, s.Seerr.Key); err != nil {
			return err
		}
	}
	return nil
}

func checkSource(path, raw, key string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return fmt.Errorf("%s.url must be an http(s) base URL without credentials or query", path)
	}
	if !keyNameRE.MatchString(key) {
		return fmt.Errorf("%s.key must name the API key (letters, digits, '_', '.', '-'), not hold it", path)
	}
	return nil
}

// Getter is a GET-only client (internal/arr.Client).
type Getter interface {
	GetJSON(ctx context.Context, endpoint string, query url.Values, v any) error
}

// Service kinds for a Dialer.
const (
	ServiceArr      = "arr"
	ServiceProwlarr = "prowlarr"
	ServiceSeerr    = "seerr"
)

// Dialer opens a GET-only client for one service.
type Dialer func(service, baseURL, key string) (Getter, error)

// DefaultDialer uses internal/arr's GET-only, LAN-only clients.
var DefaultDialer = NewDialer(arr.Options{Retries: 1, Timeout: 30 * time.Second})

// NewDialer opens internal/arr's GET-only clients with base's options, in the mode each service needs.
func NewDialer(base arr.Options) Dialer {
	return func(service, baseURL, key string) (Getter, error) {
		opt := base
		switch service {
		case ServiceProwlarr:
			opt.ProwlarrInventory = true
		case ServiceSeerr:
			opt.Seerr = true
		}
		return arr.New(baseURL, key, opt)
	}
}

// Inventory is what inspect read. It names keys, never holds them.
type Inventory struct {
	SchemaVersion int           `json:"schema_version"`
	InspectedAt   time.Time     `json:"inspected_at"`
	Arr           []ArrInstance `json:"arr"`
	Prowlarr      *Prowlarr     `json:"prowlarr,omitempty"`
	Seerr         *Seerr        `json:"seerr,omitempty"`
	Recyclarr     *Recyclarr    `json:"recyclarr,omitempty"`
}

// ArrInstance is one Sonarr or Radarr instance as read.
type ArrInstance struct {
	Name    string `json:"name"`
	Flavour string `json:"flavour"`
	URL     string `json:"url"`
	Key     string `json:"key"`
	// Error is why the instance could not be read; the rest is then empty.
	Error           string           `json:"error,omitempty"`
	AppVersion      string           `json:"app_version,omitempty"`
	Titles          TitleCounts      `json:"titles"`
	RootFolders     []RootFolder     `json:"root_folders"`
	QualityProfiles []NamedCount     `json:"quality_profiles"`
	CustomFormats   []string         `json:"custom_formats"`
	Tags            []string         `json:"tags"`
	Indexers        []ArrIndexer     `json:"indexers"`
	DownloadClients []DownloadClient `json:"download_clients"`
	// Notifications are the Connect targets (Plex, Discord, webhooks); ImportLists the lists that add titles.
	Notifications []ArrNotification `json:"notifications"`
	ImportLists   []ArrImportList   `json:"import_lists"`
	// DelayProfiles and ReleaseProfiles count the arr's profiles of those kinds; Theatarr has neither.
	DelayProfiles   int `json:"delay_profiles"`
	ReleaseProfiles int `json:"release_profiles"`
	// Hardlinks is the media management "use hardlinks instead of copy" switch.
	Hardlinks bool `json:"hardlinks"`
	// Raw holds the GET bodies the plan freezes profiles and naming from. It is not kept with a saved plan.
	Raw map[string]json.RawMessage `json:"raw,omitempty"`
}

// TitleCounts summarises the instance's library.
type TitleCounts struct {
	Total     int `json:"total"`
	Monitored int `json:"monitored"`
	WithFiles int `json:"with_files"`
	// Anime counts series of series type anime (Sonarr).
	Anime int `json:"anime"`
}

// RootFolder is one root folder and how many titles live under it.
type RootFolder struct {
	Path   string `json:"path"`
	Titles int    `json:"titles"`
}

// NamedCount is a quality profile and how many titles use it.
type NamedCount struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Titles int    `json:"titles"`
}

// ArrIndexer is one indexer of an arr. ProwlarrID is set for one Prowlarr syncs (its feed URL names the id).
type ArrIndexer struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	Protocol       string `json:"protocol"`
	RSS            bool   `json:"rss"`
	Search         bool   `json:"search"`
	Priority       int    `json:"priority"`
	BaseURL        string `json:"base_url,omitempty"`
	ProwlarrID     int    `json:"prowlarr_id,omitempty"`
}

// DownloadClient is one download client of an arr. Bridge names the debrid or Usenet bridge it looks like, if any.
type DownloadClient struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	Protocol       string `json:"protocol"`
	Enabled        bool   `json:"enabled"`
	Priority       int    `json:"priority"`
	Host           string `json:"host,omitempty"`
	Category       string `json:"category,omitempty"`
	Bridge         string `json:"bridge,omitempty"`
}

// ArrNotification is one Connect target of an arr. Target is its host only (scheme, host, port): a webhook URL's
// path or query can hold a token (Discord's does), and secret-looking fields are never read.
type ArrNotification struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	// Triggers are the events it fires on ("Grab", "Download", ...); none means it never fires.
	Triggers []string `json:"triggers"`
	Target   string   `json:"target,omitempty"`
}

// ArrImportList is one import list of an arr. Source is the list's host only (an arr-to-arr list's base URL, an
// RSS list's server), for the same reason as ArrNotification.Target.
type ArrImportList struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	// Enabled is Radarr's "enabled" switch; Sonarr has none and syncs a list only with enableAutomaticAdd on.
	Enabled bool `json:"enabled"`
	// AutoAdd is "add new titles automatically" (Radarr enableAuto, Sonarr enableAutomaticAdd).
	AutoAdd bool   `json:"auto_add"`
	Source  string `json:"source,omitempty"`
}

// Prowlarr is Prowlarr's indexers and the applications it syncs them to.
type Prowlarr struct {
	URL        string `json:"url"`
	Key        string `json:"key"`
	Error      string `json:"error,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	// Indexers keep only non-secret fields; Prowlarr masks secrets anyway, and names that look secret are dropped.
	Indexers     []cardigann.ProwlarrIndexer `json:"indexers"`
	Applications []ProwlarrApp               `json:"applications"`
}

// ProwlarrApp is one application Prowlarr syncs indexers to.
type ProwlarrApp struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	SyncLevel      string `json:"sync_level"`
	BaseURL        string `json:"base_url,omitempty"`
	// Instance is the inspected arr instance at BaseURL, if any.
	Instance string `json:"instance,omitempty"`
}

// Seerr is the Radarr and Sonarr servers Seerr (or Overseerr, Jellyseerr) sends requests to.
type Seerr struct {
	URL        string        `json:"url"`
	Key        string        `json:"key"`
	Error      string        `json:"error,omitempty"`
	AppVersion string        `json:"app_version,omitempty"`
	Radarr     []SeerrServer `json:"radarr"`
	Sonarr     []SeerrServer `json:"sonarr"`
}

// SeerrServer is one arr server entry of Seerr. Seerr's copy of the arr's API key is never read into it.
type SeerrServer struct {
	ID                     int    `json:"id"`
	Name                   string `json:"name"`
	Hostname               string `json:"hostname"`
	Port                   int    `json:"port"`
	UseSSL                 bool   `json:"use_ssl"`
	BaseURL                string `json:"base_url,omitempty"`
	Is4K                   bool   `json:"is_4k"`
	IsDefault              bool   `json:"is_default"`
	ActiveProfileID        int    `json:"active_profile_id"`
	ActiveProfileName      string `json:"active_profile_name"`
	ActiveDirectory        string `json:"active_directory"`
	ActiveAnimeProfileName string `json:"active_anime_profile_name,omitempty"`
	ActiveAnimeDirectory   string `json:"active_anime_directory,omitempty"`
	// Instance is the inspected arr instance this entry points at, matched by host, port and base URL.
	Instance string `json:"instance,omitempty"`
}

// Recyclarr is what a recyclarr.yml manages. Its API keys are never read into it.
type Recyclarr struct {
	Path      string              `json:"path"`
	Error     string              `json:"error,omitempty"`
	Instances []RecyclarrInstance `json:"instances"`
}

// RecyclarrInstance is one instance block of recyclarr.yml.
type RecyclarrInstance struct {
	Name     string `json:"name"`
	Flavour  string `json:"flavour"`
	BaseURL  string `json:"base_url,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Profiles are the quality profiles it manages, with the TRaSH profile each is built from.
	Profiles []RecyclarrProfile `json:"profiles"`
}

// RecyclarrProfile is one quality profile Recyclarr manages.
type RecyclarrProfile struct {
	Name     string `json:"name"`
	TrashID  string `json:"trash_id,omitempty"`
	ScoreSet string `json:"score_set,omitempty"`
}

// Inspect reads every source with GET only. A service that cannot be read is recorded with its error; bad sources or
// a missing key are an error before anything is read.
func Inspect(ctx context.Context, src Sources, keys Keys, dial Dialer, now time.Time) (*Inventory, error) {
	if err := src.Validate(); err != nil {
		return nil, err
	}
	if dial == nil {
		dial = DefaultDialer
	}
	lookup := func(name string) (string, error) {
		if keys != nil {
			if v, ok := keys(name); ok && v != "" {
				return v, nil
			}
		}
		return "", fmt.Errorf("key %s is not set", name)
	}
	for _, a := range src.Arr {
		if _, err := lookup(a.Key); err != nil {
			return nil, fmt.Errorf("arr %s: %w", a.Name, err)
		}
	}
	for name, s := range map[string]*ServiceSource{"prowlarr": src.Prowlarr, "seerr": src.Seerr} {
		if s != nil {
			if _, err := lookup(s.Key); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	inv := &Inventory{SchemaVersion: SchemaVersion, InspectedAt: now.UTC(), Arr: []ArrInstance{}}
	var secrets []string
	for _, a := range src.Arr {
		key, _ := lookup(a.Key)
		secrets = append(secrets, key)
		inst := ArrInstance{Name: a.Name, Flavour: a.Flavour, URL: strings.TrimRight(a.URL, "/"), Key: a.Key}
		if g, err := dial(ServiceArr, a.URL, key); err != nil {
			inst.Error = scrub(err, key)
		} else if err := readArr(ctx, g, &inst); err != nil {
			inst = ArrInstance{Name: inst.Name, Flavour: inst.Flavour, URL: inst.URL, Key: inst.Key, Error: scrub(err, key)}
		}
		inv.Arr = append(inv.Arr, inst)
	}
	if s := src.Prowlarr; s != nil {
		key, _ := lookup(s.Key)
		secrets = append(secrets, key)
		p := &Prowlarr{URL: strings.TrimRight(s.URL, "/"), Key: s.Key}
		if g, err := dial(ServiceProwlarr, s.URL, key); err != nil {
			p.Error = scrub(err, key)
		} else if err := readProwlarr(ctx, g, p); err != nil {
			p = &Prowlarr{URL: p.URL, Key: p.Key, Error: scrub(err, key)}
		}
		inv.Prowlarr = p
	}
	if s := src.Seerr; s != nil {
		key, _ := lookup(s.Key)
		secrets = append(secrets, key)
		p := &Seerr{URL: strings.TrimRight(s.URL, "/"), Key: s.Key}
		if g, err := dial(ServiceSeerr, s.URL, key); err != nil {
			p.Error = scrub(err, key)
		} else if err := readSeerr(ctx, g, p); err != nil {
			p = &Seerr{URL: p.URL, Key: p.Key, Error: scrub(err, key)}
		}
		inv.Seerr = p
	}
	if src.Recyclarr != "" {
		inv.Recyclarr = readRecyclarr(src.Recyclarr)
	}
	inv.match()
	// A key a service echoed back somewhere unexpected must not survive into the inventory.
	if err := inv.scrubKeys(secrets); err != nil {
		return nil, err
	}
	return inv, nil
}

// scrub is an error's text with key masked.
func scrub(err error, key string) string {
	return arr.Mask(err.Error(), key)
}

func readArr(ctx context.Context, g Getter, inst *ArrInstance) error {
	var status struct {
		Version string `json:"version"`
	}
	if err := g.GetJSON(ctx, "system/status", nil, &status); err != nil {
		return err
	}
	inst.AppVersion = status.Version
	inst.Raw = map[string]json.RawMessage{}
	for _, ep := range []string{"qualityprofile", "customformat", "qualitydefinition", "config/mediamanagement",
		"config/indexer", "config/naming"} {
		var raw json.RawMessage
		if err := g.GetJSON(ctx, ep, nil, &raw); err != nil {
			return err
		}
		inst.Raw[ep] = raw
	}
	var roots []struct {
		Path string `json:"path"`
	}
	if err := g.GetJSON(ctx, "rootfolder", nil, &roots); err != nil {
		return err
	}
	var profiles []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(inst.Raw["qualityprofile"], &profiles); err != nil {
		return fmt.Errorf("qualityprofile: %w", err)
	}
	var formats []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(inst.Raw["customformat"], &formats); err != nil {
		return fmt.Errorf("customformat: %w", err)
	}
	var mm struct {
		CopyUsingHardlinks bool `json:"copyUsingHardlinks"`
	}
	_ = json.Unmarshal(inst.Raw["config/mediamanagement"], &mm)
	inst.Hardlinks = mm.CopyUsingHardlinks
	var tags []struct {
		Label string `json:"label"`
	}
	if err := g.GetJSON(ctx, "tag", nil, &tags); err != nil {
		return err
	}
	var indexers []arrThing
	if err := g.GetJSON(ctx, "indexer", nil, &indexers); err != nil {
		return err
	}
	var clients []arrThing
	if err := g.GetJSON(ctx, "downloadclient", nil, &clients); err != nil {
		return err
	}
	var notifications, lists []json.RawMessage
	if err := g.GetJSON(ctx, "notification", nil, &notifications); err != nil {
		return err
	}
	if err := g.GetJSON(ctx, "importlist", nil, &lists); err != nil {
		return err
	}
	var delay, release []json.RawMessage
	if err := g.GetJSON(ctx, "delayprofile", nil, &delay); err != nil {
		return err
	}
	if err := g.GetJSON(ctx, "releaseprofile", nil, &release); err != nil {
		return err
	}
	// The default delay profile always exists; only the others are the user's.
	inst.DelayProfiles, inst.ReleaseProfiles = max(len(delay)-1, 0), len(release)
	var titles []struct {
		Monitored        bool   `json:"monitored"`
		HasFile          bool   `json:"hasFile"`
		RootFolderPath   string `json:"rootFolderPath"`
		Path             string `json:"path"`
		QualityProfileID int    `json:"qualityProfileId"`
		SeriesType       string `json:"seriesType"`
		Statistics       struct {
			EpisodeFileCount int `json:"episodeFileCount"`
		} `json:"statistics"`
	}
	ep := "movie"
	if inst.Flavour == "sonarr" {
		ep = "series"
	}
	if err := g.GetJSON(ctx, ep, nil, &titles); err != nil {
		return err
	}
	byRoot := map[string]int{}
	byProfile := map[int]int{}
	for _, t := range titles {
		inst.Titles.Total++
		if t.Monitored {
			inst.Titles.Monitored++
		}
		if t.HasFile || t.Statistics.EpisodeFileCount > 0 {
			inst.Titles.WithFiles++
		}
		if t.SeriesType == "anime" {
			inst.Titles.Anime++
		}
		byProfile[t.QualityProfileID]++
		root := strings.TrimRight(t.RootFolderPath, "/")
		if root == "" {
			for _, r := range roots {
				if p := strings.TrimRight(r.Path, "/"); p != "" && strings.HasPrefix(t.Path, p+"/") {
					root = p
				}
			}
		}
		byRoot[root]++
	}
	inst.RootFolders = []RootFolder{}
	for _, r := range roots {
		p := strings.TrimRight(r.Path, "/")
		inst.RootFolders = append(inst.RootFolders, RootFolder{Path: p, Titles: byRoot[p]})
	}
	inst.QualityProfiles = []NamedCount{}
	for _, p := range profiles {
		inst.QualityProfiles = append(inst.QualityProfiles, NamedCount{ID: p.ID, Name: p.Name, Titles: byProfile[p.ID]})
	}
	inst.CustomFormats = []string{}
	for _, f := range formats {
		inst.CustomFormats = append(inst.CustomFormats, f.Name)
	}
	inst.Tags = []string{}
	for _, t := range tags {
		inst.Tags = append(inst.Tags, t.Label)
	}
	inst.Indexers = []ArrIndexer{}
	for _, ix := range indexers {
		a := ArrIndexer{Name: ix.Name, Implementation: ix.Implementation, Protocol: ix.Protocol, RSS: ix.EnableRSS,
			Search: ix.EnableAutomaticSearch, Priority: ix.Priority, BaseURL: cleanURL(ix.field("baseUrl"))}
		a.ProwlarrID = prowlarrFeedID(a.BaseURL)
		inst.Indexers = append(inst.Indexers, a)
	}
	inst.DownloadClients = []DownloadClient{}
	for _, c := range clients {
		dc := DownloadClient{Name: c.Name, Implementation: c.Implementation, Protocol: c.Protocol, Enabled: c.Enable,
			Priority: c.Priority, Host: hostPort(c.field("host"), c.field("port"), c.field("useSsl"))}
		for _, f := range []string{"movieCategory", "tvCategory", "category"} {
			if v := c.field(f); v != "" {
				dc.Category = v
				break
			}
		}
		dc.Bridge = bridgeOf(dc)
		inst.DownloadClients = append(inst.DownloadClients, dc)
	}
	inst.Notifications = []ArrNotification{}
	for _, raw := range notifications {
		var n arrThing
		var flags map[string]any
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("notification: %w", err)
		}
		_ = json.Unmarshal(raw, &flags)
		an := ArrNotification{Name: n.Name, Implementation: n.Implementation, Triggers: []string{}, Target: n.target()}
		for _, k := range sortedKeys(flags) {
			if on, _ := flags[k].(bool); on && strings.HasPrefix(k, "on") && len(k) > 2 {
				an.Triggers = append(an.Triggers, k[2:])
			}
		}
		inst.Notifications = append(inst.Notifications, an)
	}
	inst.ImportLists = []ArrImportList{}
	for _, raw := range lists {
		var l struct {
			arrThing
			Enabled            *bool `json:"enabled"`
			EnableAuto         bool  `json:"enableAuto"`
			EnableAutomaticAdd bool  `json:"enableAutomaticAdd"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			return fmt.Errorf("importlist: %w", err)
		}
		al := ArrImportList{Name: l.Name, Implementation: l.Implementation, Enabled: l.EnableAutomaticAdd,
			AutoAdd: l.EnableAuto || l.EnableAutomaticAdd, Source: l.target()}
		if l.Enabled != nil {
			al.Enabled = *l.Enabled
		}
		inst.ImportLists = append(inst.ImportLists, al)
	}
	return nil
}

// urlFields are the settings a Connect target or an import list keeps its address in, by implementation.
var urlFields = []string{"url", "baseUrl", "serverUrl", "server", "webHookUrl"}

// target is where a notification sends or a list reads from: host and port of its host field or of the first URL
// field, never a path, query or credential. A URL field without a scheme (Email's server is a plain hostname) reads
// as host and port.
func (t arrThing) target() string {
	if h := t.field("host"); h != "" {
		if strings.Contains(h, "://") {
			return hostOnly(h)
		}
		return hostPort(h, t.field("port"), t.field("useSsl"))
	}
	for _, f := range urlFields {
		v := t.field(f)
		if strings.Contains(v, "://") {
			v = hostOnly(v)
		} else {
			v = bareHost(v, t.field("port"))
		}
		if v != "" {
			return v
		}
	}
	return ""
}

// bareHost is a scheme-less host's hostname and port (port when it names none), without userinfo or path; anything
// unparsable reads empty.
func bareHost(host, port string) string {
	if host == "" {
		return ""
	}
	u, err := url.Parse("//" + host)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	if u.Port() == "" && port != "" {
		return u.Host + ":" + port
	}
	return u.Host
}

// hostOnly is a URL's scheme, host and port; anything unparsable reads empty.
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// arrThing is the shared shape of an arr indexer and download client.
type arrThing struct {
	Name                  string `json:"name"`
	Implementation        string `json:"implementation"`
	Protocol              string `json:"protocol"`
	Enable                bool   `json:"enable"`
	EnableRSS             bool   `json:"enableRss"`
	EnableAutomaticSearch bool   `json:"enableAutomaticSearch"`
	Priority              int    `json:"priority"`
	SyncLevel             string `json:"syncLevel"`
	Fields                []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	} `json:"fields"`
}

// field is a non-secret setting's value as text; secret-looking names always read empty.
func (t arrThing) field(name string) string {
	if secretField(name) {
		return ""
	}
	for _, f := range t.Fields {
		if f.Name == name {
			switch v := f.Value.(type) {
			case string:
				return strings.TrimSpace(v)
			case float64:
				return fmt.Sprint(v)
			case bool:
				return fmt.Sprint(v)
			}
		}
	}
	return ""
}

var secretWords = []string{"key", "pass", "token", "secret", "cookie", "auth", "session", "hash"}

// secretField reports whether a setting's name looks like it holds a credential.
func secretField(name string) bool {
	n := strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// cleanURL drops credentials, query and fragment from a URL; anything unparsable reads empty.
func cleanURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimRight(u.String(), "/")
}

var feedIDRE = regexp.MustCompile(`/(\d+)(?:/api)?/?$`)

// prowlarrFeedID is the Prowlarr indexer id in a synced feed URL (http://prowlarr:9696/12/), or 0.
func prowlarrFeedID(base string) int {
	m := feedIDRE.FindStringSubmatch(base)
	if m == nil {
		return 0
	}
	var id int
	fmt.Sscan(m[1], &id)
	return id
}

// hostPort is a host setting as a URL: scheme from useSsl, hostname and port, never userinfo ("user:pass@host").
func hostPort(host, port, ssl string) string {
	h := bareHost(host, port)
	if h == "" {
		return ""
	}
	scheme := "http"
	if ssl == "true" {
		scheme = "https"
	}
	return scheme + "://" + h
}

// bridges are the names a debrid or Usenet bridge goes by in a download client's name, implementation or host.
var bridges = []string{"decypharr", "altmount", "riven", "rdt-client", "rdtclient", "scryer", "torbox", "realdebrid",
	"real-debrid", "alldebrid", "debrid", "zurg"}

func bridgeOf(c DownloadClient) string {
	text := strings.ToLower(c.Name + " " + c.Implementation + " " + c.Host)
	for _, b := range bridges {
		if strings.Contains(text, b) {
			return b
		}
	}
	return ""
}

func readProwlarr(ctx context.Context, g Getter, p *Prowlarr) error {
	var status struct {
		Version string `json:"version"`
	}
	if err := g.GetJSON(ctx, "system/status", nil, &status); err != nil {
		return err
	}
	p.AppVersion = status.Version
	if err := g.GetJSON(ctx, "indexer", nil, &p.Indexers); err != nil {
		return err
	}
	if p.Indexers == nil {
		p.Indexers = []cardigann.ProwlarrIndexer{}
	}
	for i := range p.Indexers {
		var kept []cardigann.ProwlarrField
		for _, f := range p.Indexers[i].Fields {
			if s, ok := f.Value.(string); secretField(f.Name) || (ok && strings.Trim(s, "*") == "" && s != "") {
				continue
			}
			switch f.Value.(type) {
			case string, bool, float64:
				kept = append(kept, f)
			}
		}
		p.Indexers[i].Fields = kept
		var urls []string
		for _, u := range p.Indexers[i].IndexerURLs {
			if c := cleanURL(u); c != "" {
				urls = append(urls, c)
			}
		}
		p.Indexers[i].IndexerURLs = urls
	}
	var apps []arrThing
	if err := g.GetJSON(ctx, "applications", nil, &apps); err != nil {
		return err
	}
	p.Applications = []ProwlarrApp{}
	for _, a := range apps {
		p.Applications = append(p.Applications, ProwlarrApp{Name: a.Name, Implementation: a.Implementation,
			SyncLevel: a.SyncLevel, BaseURL: cleanURL(a.field("baseUrl"))})
	}
	return nil
}

func readSeerr(ctx context.Context, g Getter, s *Seerr) error {
	var status struct {
		Version string `json:"version"`
	}
	if err := g.GetJSON(ctx, "status", nil, &status); err != nil {
		return err
	}
	s.AppVersion = status.Version
	for _, ep := range []string{"settings/radarr", "settings/sonarr"} {
		var servers []struct {
			ID                     int    `json:"id"`
			Name                   string `json:"name"`
			Hostname               string `json:"hostname"`
			Port                   int    `json:"port"`
			UseSSL                 bool   `json:"useSsl"`
			BaseURL                string `json:"baseUrl"`
			Is4K                   bool   `json:"is4k"`
			IsDefault              bool   `json:"isDefault"`
			ActiveProfileID        int    `json:"activeProfileId"`
			ActiveProfileName      string `json:"activeProfileName"`
			ActiveDirectory        string `json:"activeDirectory"`
			ActiveAnimeProfileName string `json:"activeAnimeProfileName"`
			ActiveAnimeDirectory   string `json:"activeAnimeDirectory"`
		}
		if err := g.GetJSON(ctx, ep, nil, &servers); err != nil {
			return err
		}
		out := []SeerrServer{}
		for _, v := range servers {
			out = append(out, SeerrServer{ID: v.ID, Name: v.Name, Hostname: v.Hostname, Port: v.Port, UseSSL: v.UseSSL,
				BaseURL: strings.TrimRight(v.BaseURL, "/"), Is4K: v.Is4K, IsDefault: v.IsDefault,
				ActiveProfileID: v.ActiveProfileID, ActiveProfileName: v.ActiveProfileName,
				ActiveDirectory: v.ActiveDirectory, ActiveAnimeProfileName: v.ActiveAnimeProfileName,
				ActiveAnimeDirectory: v.ActiveAnimeDirectory})
		}
		if ep == "settings/radarr" {
			s.Radarr = out
		} else {
			s.Sonarr = out
		}
	}
	return nil
}

// maxRecyclarr bounds the recyclarr.yml read.
const maxRecyclarr = 1 << 20

func readRecyclarr(path string) *Recyclarr {
	r := &Recyclarr{Path: path, Instances: []RecyclarrInstance{}}
	b, err := readFileLimit(path, maxRecyclarr)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	cfg, err := recyclarr.Parse(b)
	if err != nil {
		r.Error = "parse: " + err.Error()
		return r
	}
	// base_url is read on its own: recyclarr.Parse drops it, and a tagged value (!env_var, !secret) stays unread.
	var urls map[string]map[string]struct {
		BaseURL yaml.Node `yaml:"base_url"`
	}
	_ = yaml.Unmarshal(b, &urls)
	for _, in := range cfg.Instances {
		ri := RecyclarrInstance{Name: in.Name, Flavour: string(in.Flavour), Profiles: []RecyclarrProfile{}}
		if n := urls[string(in.Flavour)][in.Name].BaseURL; n.Kind == yaml.ScalarNode && (n.Tag == "" || n.Tag == "!!str") {
			ri.BaseURL = cleanURL(n.Value)
		}
		for _, qp := range in.QualityProfiles {
			ri.Profiles = append(ri.Profiles, RecyclarrProfile{Name: qp.Name, TrashID: qp.TrashID, ScoreSet: qp.ScoreSet})
		}
		r.Instances = append(r.Instances, ri)
	}
	return r
}

func readFileLimit(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s", path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	b := make([]byte, st.Size())
	if _, err := f.ReadAt(b, 0); err != nil && st.Size() > 0 {
		return nil, fmt.Errorf("cannot read %s", path)
	}
	return b, nil
}

// match links Seerr servers, Prowlarr applications and Recyclarr instances to the inspected arr instances.
func (inv *Inventory) match() {
	byAddr := map[string]string{}
	for _, a := range inv.Arr {
		if k := addrKey(a.URL); k != "" {
			byAddr[k] = a.Name
		}
	}
	if inv.Seerr != nil {
		for _, list := range [][]SeerrServer{inv.Seerr.Radarr, inv.Seerr.Sonarr} {
			for i := range list {
				s := &list[i]
				scheme := "http"
				if s.UseSSL {
					scheme = "https"
				}
				s.Instance = byAddr[addrKey(fmt.Sprintf("%s://%s:%d%s", scheme, s.Hostname, s.Port, s.BaseURL))]
			}
		}
	}
	if inv.Prowlarr != nil {
		for i := range inv.Prowlarr.Applications {
			inv.Prowlarr.Applications[i].Instance = byAddr[addrKey(inv.Prowlarr.Applications[i].BaseURL)]
		}
	}
	if inv.Recyclarr != nil {
		for i := range inv.Recyclarr.Instances {
			ri := &inv.Recyclarr.Instances[i]
			ri.Instance = byAddr[addrKey(ri.BaseURL)]
			if ri.Instance == "" {
				for _, a := range inv.Arr {
					if a.Flavour == ri.Flavour && strings.EqualFold(a.Name, ri.Name) {
						ri.Instance = a.Name
					}
				}
			}
		}
	}
}

// addrKey is host:port/path of a URL, with the scheme's default port, for matching one service's addresses.
func addrKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return strings.ToLower(u.Hostname()) + ":" + port + strings.TrimRight(u.Path, "/")
}

// scrubKeys refuses an inventory that holds any key value: the services must never hand one back to us.
func (inv *Inventory) scrubKeys(keys []string) error {
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if len(k) >= 6 && strings.Contains(string(b), k) {
			return errors.New("a service echoed an API key back in a field the inventory keeps; nothing was kept")
		}
	}
	return nil
}

// Instance returns the inspected arr instance by name.
func (inv *Inventory) Instance(name string) *ArrInstance {
	for i := range inv.Arr {
		if inv.Arr[i].Name == name {
			return &inv.Arr[i]
		}
	}
	return nil
}

// WithoutRaw is a copy of the inventory without the arrs' raw GET bodies, as a saved plan keeps it.
func (inv *Inventory) WithoutRaw() *Inventory {
	cp := *inv
	cp.Arr = make([]ArrInstance, len(inv.Arr))
	for i, a := range inv.Arr {
		a.Raw = nil
		cp.Arr[i] = a
	}
	return &cp
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
