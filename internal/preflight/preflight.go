// Package preflight is the install-wide readiness check (`theatarr preflight`, POST /preflight, the first-run screen):
// what a fresh install still needs before it can be useful, and what is set up but broken. Each item is ok, info, warn or
// fail, with why and the exact next step. It checks the install around every version: mount, roots, trash, providers,
// download clients, indexers, metadata keys, the arr views, Plex and the VPN egress.
//
// Nothing here writes or calls out on its own: the filesystem is read with stat and access(2), and every outbound
// check (provider and indexer tests, Plex, the arr views) is a function the caller supplies, so the service runs
// them through its gateway and tests run on fixtures. A check whose input is missing says "not checked" as a warn,
// never ok. The existing health checks (internal/health) are reused as given, not run again here.
package preflight

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/secrets"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/uipages"
)

// Status of one item.
type Status string

const (
	OK   Status = "ok"
	Info Status = "info"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Areas, in the order they print.
const (
	AreaVersions  = "versions"
	AreaLibrary   = "library"
	AreaProviders = "providers"
	AreaIndexers  = "indexers"
	AreaMetadata  = "metadata"
	AreaViews     = "views"
	AreaPlex      = "plex"
	AreaEgress    = "egress"
	AreaSwitches  = "switches"
	AreaHealth    = "health"
)

// Item is one check's verdict.
type Item struct {
	Area   string `json:"area"`
	Check  string `json:"check"`
	Status Status `json:"status"`
	// Why says what was seen.
	Why string `json:"why"`
	// Next is the exact next step for a warn or fail; empty when ok.
	Next string `json:"next,omitempty"`
}

// Report is one run.
type Report struct {
	At    time.Time `json:"at"`
	Items []Item    `json:"items"`
	OK    int       `json:"ok"`
	Warn  int       `json:"warn"`
	Fail  int       `json:"fail"`
}

// Source is the outcome of one provider or indexer test (the settings tests' result, reduced).
type Source struct {
	Key string
	// Status is ok, failed, refused, cooldown, busy or timeout, as the settings tests answer.
	Status   string
	Category string
	Message  string
	// Results is an indexer probe's release count.
	Results *int
	// Caps are the query types an indexer answers (rss, movie, series, text).
	Caps []string
	// Premium and Expires are a provider account's.
	Premium bool
	Expires time.Time
}

// View is one arr-compatible view as the service answers it.
type View struct {
	Path string
	// Code is the HTTP status of GET <path>/api/v3/system/status without a key; 0 when it could not be asked.
	Code int
	Err  string
}

// Options are the inputs. Policy is required; every other input is optional and its checks say "not checked"
// without it.
type Options struct {
	Policy *policy.Policy
	// ValidationError is a loaded policy validation failure; static checks still run.
	ValidationError error
	// FS reads the filesystem (OSFS in the service and the CLI).
	FS FS
	// Device is the filesystem id of a path (stat's st_dev), for links that need one filesystem. nil skips that check.
	Device func(path string) (uint64, error)
	// Mount checks the debrid mount's directory (health.Mount in the CLI, the service's guarded check in serve).
	Mount func(ctx context.Context, dir string) health.Result
	// Credentials is the same generation-aware resolver used by Setup; it exposes presence only.
	Credentials settings.Credentials
	// Sources tests the providers or indexers ("providers"/"indexers"); nil when they are not tested.
	Sources func(ctx context.Context, entity string) []Source
	// Views asks each view; nil when they are not asked.
	Views func(ctx context.Context) []View
	// APIKeys is the number of active API keys (Seerr and the other clients need one); negative when unknown.
	APIKeys int
	// Plex asks the Plex server for its identity; nil when not asked.
	Plex func(ctx context.Context) error
	// Health is the service's health results; only the egress and indexer back-off checks are read from it.
	Health []health.Result
	// Disabled names the stored providers and indexers ("providers"/"indexers") that are disabled, when Policy is
	// the running set without them (the CLI): a check that finds none enabled names them.
	Disabled map[string][]string
	Now      time.Time
}

// Run checks the install. It never fails: a check that cannot run says so.
func Run(ctx context.Context, o Options) *Report {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	o.Credentials.Policy = o.Policy
	r := &Report{At: o.Now.UTC()}
	p := o.Policy
	if p == nil {
		p = &policy.Policy{}
	}
	c := &checker{o: o, p: p, r: r}
	if o.ValidationError != nil {
		c.add("policy", "validation", Fail, "correct the configuration in "+uipages.Settings+" or the policy YAML, then run preflight again", "%v", o.ValidationError)
	}
	c.versions()
	c.library(ctx)
	c.providers(ctx)
	c.clients()
	c.indexers(ctx)
	c.metadata()
	c.views(ctx)
	c.plex(ctx)
	c.egress()
	c.switches()
	c.credentialNotes()
	for _, it := range r.Items {
		switch it.Status {
		case OK:
			r.OK++
		case Warn:
			r.Warn++
		case Fail:
			r.Fail++
		}
	}
	return r
}

type checker struct {
	o Options
	p *policy.Policy
	r *Report
}

func (c *checker) add(area, check string, st Status, next, format string, args ...any) {
	if st == OK {
		next = ""
	}
	c.r.Items = append(c.r.Items, Item{Area: area, Check: check, Status: st, Why: fmt.Sprintf(format, args...), Next: next})
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *checker) debridVersions() []string {
	var out []string
	for _, k := range sortedKeys(c.p.Versions) {
		if c.p.Versions[k].Storage == policy.StorageDebrid {
			out = append(out, k)
		}
	}
	return out
}

func (c *checker) versions() {
	if len(c.p.Versions) == 0 {
		c.add(AreaVersions, "versions", Warn, "create a first library from a preset on the setup screen ("+uipages.Setup+") "+
			"or in "+uipages.Libraries+"; it starts as Watch only with downloads off, unless Setup's one-step start is used",
			"no libraries: nothing is searched, grabbed or linked")
		return
	}
	var active, shadow []string
	for _, k := range sortedKeys(c.p.Versions) {
		if c.p.Versions[k].Mode == policy.ModeActive {
			active = append(active, k)
		} else {
			shadow = append(shadow, k)
		}
	}
	msg := "1 library"
	if len(c.p.Versions) != 1 {
		msg = fmt.Sprintf("%d libraries", len(c.p.Versions))
	}
	if len(shadow) > 0 {
		msg += "; shadow (compare only): " + strings.Join(shadow, ", ")
	}
	if len(active) > 0 {
		msg += "; active: " + strings.Join(active, ", ")
	}
	c.add(AreaVersions, "versions", OK, "", "%s", msg)
}

func (c *checker) library(ctx context.Context) {
	lib := c.p.LibrarySettings()
	debrid := c.debridVersions()
	mountDir := ""
	switch {
	case lib.MountPath == "" && len(debrid) > 0:
		c.add(AreaLibrary, "mount", Fail, "set the mount path in "+uipages.FileManagement+" to where the debrid mount "+
			"(zurg, rclone or similar) is visible inside the container, and bind it read-only",
			"library.mount.path is not set: downloads for %s can never be imported", strings.Join(debrid, ", "))
	case lib.MountPath == "":
		c.add(AreaLibrary, "mount", Warn, "set the mount path in "+uipages.FileManagement+" once a debrid version exists",
			"library.mount.path is not set: no debrid downloads can be imported")
	default:
		mountDir = filepath.Join(lib.MountPath, lib.AllDir)
		if c.o.Mount == nil {
			c.add(AreaLibrary, "mount", Warn, "run theatarr preflight inside the container", "not checked: %s", mountDir)
		} else {
			res := c.o.Mount(ctx, mountDir)
			switch res.Type {
			case health.OK, health.Notice:
				c.add(AreaLibrary, "mount", OK, "", "%s is visible and readable: %s", mountDir, res.Message)
			default:
				c.add(AreaLibrary, "mount", Fail, "check that the mount runs on the host and is bound read-only into "+
					"the container at "+lib.MountPath, "%s: %s", mountDir, res.Message)
			}
		}
	}

	for _, k := range sortedKeys(c.p.Versions) {
		v := c.p.Versions[k]
		lv := lib.Versions[k]
		if lv == nil || len(lv.Roots) == 0 {
			c.add(AreaLibrary, "roots "+k, Fail, "add a root to library "+k+" in "+uipages.Libraries,
				"library %s has no root: every library write for it is refused", k)
			continue
		}
		for _, root := range lv.Roots {
			// A Watch-only library writes nothing: its root need only be readable.
			if v.Mode == policy.ModeActive {
				c.dir(AreaLibrary, "root "+k, root, true, "create "+root+" on the host and bind it read-write into the container at the same path the media server uses")
			} else {
				c.dir(AreaLibrary, "root "+k, root, false, "make "+root+" readable inside the container")
			}
			if lib.MountPath != "" && under(root, lib.MountPath) {
				c.add(AreaLibrary, "root "+k, Fail, "move the root out of the debrid mount",
					"%s is inside the debrid mount %s: the library never writes there", root, lib.MountPath)
			}
			if v.Import == "hardlink" {
				c.sameFS("root "+k, root, lib.MountPath, "hardlinks need the root and the downloads on one filesystem")
			}
		}
		if v.Mode == policy.ModeActive && lib.Plex != nil && lv.PlexSection == 0 {
			c.add(AreaLibrary, "plex library "+k, Warn, "choose "+k+"'s Plex library in "+uipages.Plex,
				"library %s has no Plex library: new files appear only after Plex's own scan", k)
		}
	}

	switch {
	case lib.TrashDir == "" && c.libraryWrites():
		c.add(AreaLibrary, "trash", Fail, "set the trash folder in "+uipages.FileManagement+" to a writable directory outside "+
			"every root and the mount", "library.trash.dir is not set: upgrades and removals keep no copy of the "+
			"replaced link")
	case lib.TrashDir == "" && len(c.p.Versions) > 0:
		c.add(AreaLibrary, "trash", Warn, "set the trash folder in "+uipages.FileManagement+" to a writable directory outside "+
			"every root and the mount", "library.trash.dir is not set: upgrades and removals keep no copy of the "+
			"replaced link")
	case lib.TrashDir == "":
		c.add(AreaLibrary, "trash", Warn, "set the trash folder in "+uipages.FileManagement+" before the first library goes active",
			"library.trash.dir is not set")
	default:
		c.dir(AreaLibrary, "trash", lib.TrashDir, true, "create "+lib.TrashDir+" and bind it read-write into the container")
	}
}

// libraryWrites reports whether any library may write now, by the library gate's own rule.
func (c *checker) libraryWrites() bool {
	gate := library.NewGate(c.p)
	for k := range c.p.Versions {
		if gate.Active(k) {
			return true
		}
	}
	return false
}

// under reports whether path is dir or inside it.
func under(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func (c *checker) dir(area, check, path string, write bool, next string) {
	fact := InspectDirectory(c.o.FS, path, write)
	switch {
	case !fact.Checked:
		c.add(area, check, Warn, "run theatarr preflight inside the container", "not checked: %s (no filesystem access)", path)
	case fact.Problem != nil:
		c.add(area, check, Fail, next, "%s", fact.Problem)
	case !write:
		c.add(area, check, OK, "", "%s is there", path)
	default:
		c.add(area, check, OK, "", "%s is there and writable", path)
	}
}

func (c *checker) sameFS(check, a, b, why string) {
	if c.o.Device == nil || a == "" || b == "" {
		return
	}
	da, errA := c.o.Device(a)
	db, errB := c.o.Device(b)
	switch {
	case errA != nil || errB != nil:
		c.add(AreaLibrary, check, Warn, "make both paths visible in the container", "not checked: %s and %s (%s)", a, b, why)
	case da != db:
		c.add(AreaLibrary, check, Fail, "put "+a+" on the same filesystem as "+b+", or import with symlinks",
			"%s and %s are on different filesystems: %s", a, b, why)
	default:
		c.add(AreaLibrary, check, OK, "", "%s and %s are on one filesystem", a, b)
	}
}

// credential reports whether a source has its credential: in the settings database, or its *_env set.
func (c *checker) credential(entity, key, env string) bool {
	return c.o.Credentials.Resolves(entity, key, env)
}

// addedButDisabled says which stored sources are disabled: "rd is added but disabled".
func addedButDisabled(keys []string) string {
	if len(keys) == 1 {
		return keys[0] + " is added but disabled"
	}
	return strings.Join(keys, ", ") + " are added but disabled"
}

func (c *checker) providers(ctx context.Context) {
	if len(c.p.Providers) == 0 {
		st := Warn
		if len(c.debridVersions()) > 0 {
			st = Fail
		}
		if off := c.o.Disabled["providers"]; len(off) > 0 {
			c.add(AreaProviders, "providers", st, "test it and enable it in "+uipages.Providers,
				"no enabled debrid provider: %s", addedButDisabled(off))
			return
		}
		c.add(AreaProviders, "providers", st, "add Real-Debrid, AllDebrid or TorBox in "+uipages.Providers+" "+
			"with its API key", "no debrid provider: nothing can be added to debrid")
		return
	}
	tested := c.sources(ctx, "providers")
	for _, fact := range InspectProviders(c.p, sortedKeys(c.p.Providers), c.credential) {
		k := fact.Key
		check := k + " (" + fact.Kind + ")"
		if !fact.Credential {
			c.add(AreaProviders, check, Fail, "set the API key in "+uipages.Providers,
				"no API key: the provider is skipped")
			continue
		}
		s, ok := tested[k]
		switch {
		case tested == nil:
			c.add(AreaProviders, check, Warn, "run preflight with source tests (the default) to check the key",
				"not tested")
		case !ok:
			c.add(AreaProviders, check, Warn, "enable it in "+uipages.Providers+" if it should be used",
				"not tested: disabled")
		case s.Status == "ok":
			msg := "the account answered"
			if !s.Premium {
				c.add(AreaProviders, check, Warn, "renew the plan: without premium the provider refuses adds",
					"the account answered but has no premium plan")
				continue
			}
			if !s.Expires.IsZero() {
				msg += "; premium until " + s.Expires.Format("2006-01-02")
			}
			c.add(AreaProviders, check, OK, "", "%s", msg)
		default:
			c.add(AreaProviders, check, sourceStatus(s), sourceNext(s, uipages.Providers), "%s", sourceWhy(s))
		}
	}
}

// clients checks the login of each qBittorrent client a library sends downloads to.
func (c *checker) clients() {
	for _, ck := range sortedKeys(c.p.Clients) {
		cl := c.p.Clients[ck]
		if cl.Kind != "qbittorrent" {
			continue
		}
		var libs []string
		st := Warn
		for _, k := range sortedKeys(c.p.Versions) {
			v := c.p.Versions[k]
			if v.Client != ck || (!v.ViaClient() && v.Storage != policy.StorageSeedbox) {
				continue
			}
			libs = append(libs, k)
			if v.Storage == policy.StorageSeedbox {
				st = Fail
			}
		}
		if len(libs) == 0 {
			continue
		}
		for _, env := range []string{cl.UsernameEnv, cl.PasswordEnv} {
			if env != "" && !c.credential("", "", env) {
				c.add(AreaProviders, "client "+ck, st, "set the login in "+uipages.Providers,
					"download client %s has no login set: %s cannot send downloads to it", ck, strings.Join(libs, ", "))
				break
			}
		}
	}
}

func (c *checker) indexers(ctx context.Context) {
	if len(c.p.Indexers) == 0 {
		st := Warn
		if off := c.o.Disabled["indexers"]; len(off) > 0 {
			c.add(AreaIndexers, "indexers", st, "test it and enable it in "+uipages.Indexers,
				"no enabled indexer: %s", addedButDisabled(off))
			return
		}
		c.add(AreaIndexers, "indexers", st, "add indexers in "+uipages.Indexers+": Torznab feeds "+
			"(Prowlarr or Jackett) or the native catalog", "no indexers: nothing can be searched")
		return
	}
	tested := c.sources(ctx, "indexers")
	for _, k := range sortedKeys(c.p.Indexers) {
		ix := c.p.Indexers[k]
		check := k + " (" + ix.Kind + ")"
		if ix.APIKeyEnv != "" && !c.credential("indexers", k, ix.APIKeyEnv) {
			c.add(AreaIndexers, check, Fail, "set the API key in "+uipages.Indexers,
				"no API key: searches of this indexer fail")
			continue
		}
		s, ok := tested[k]
		switch {
		case tested == nil:
			c.add(AreaIndexers, check, Warn, "run preflight with source tests (the default)", "not tested")
		case !ok:
			c.add(AreaIndexers, check, Warn, "enable it in "+uipages.Indexers+" if it should be used",
				"not tested: disabled")
		case s.Status == "ok":
			msg := "reachable"
			if s.Results != nil {
				msg += fmt.Sprintf("; the probe query returned %d release(s)", *s.Results)
			}
			if len(s.Caps) > 0 {
				msg += "; answers " + strings.Join(s.Caps, ", ") + " queries"
			}
			if ix.Privacy.IsPrivate() {
				msg += "; labelled private"
			}
			c.add(AreaIndexers, check, OK, "", "%s", msg)
		default:
			c.add(AreaIndexers, check, sourceStatus(s), sourceNext(s, uipages.Indexers), "%s", sourceWhy(s))
		}
	}
	for _, r := range c.o.Health {
		if r.Source == "IndexerStatusCheck" && r.Type != health.OK {
			c.add(AreaIndexers, "back-offs", healthStatus(r.Type), "wait for the back-off, or check the indexer's "+
				"budget in "+uipages.Limits, "%s", r.Message)
		}
	}
}

// sources runs the tests of one kind; nil when there is no tester.
func (c *checker) sources(ctx context.Context, entity string) map[string]Source {
	if c.o.Sources == nil {
		return nil
	}
	out := map[string]Source{}
	for _, s := range c.o.Sources(ctx, entity) {
		out[s.Key] = s
	}
	return out
}

func sourceStatus(s Source) Status {
	switch s.Status {
	case "cooldown", "busy":
		return Warn
	case "refused":
		if s.Category == "vpn" || s.Category == "backoff" || s.Category == "budget" {
			return Warn
		}
	}
	return Fail
}

func sourceWhy(s Source) string {
	msg := s.Message
	if msg == "" {
		msg = s.Status
		if s.Category != "" {
			msg += " (" + s.Category + ")"
		}
	}
	return msg
}

func sourceNext(s Source, page string) string {
	switch s.Category {
	case "auth":
		return "check the API key in " + page + " and save it again"
	case "credential":
		return "set the API key in " + page
	case "connectivity", "timeout":
		return "check the URL and that the container can reach it"
	case "http":
		return "check the URL (and its path) in " + page
	case "dmm_provider_key":
		return "link the TorBox key in DMM, or remove tb from the URL in " + page
	case "vpn":
		return "bring the VPN up (see the egress item) and run preflight again"
	case "backoff", "budget":
		return "wait for the back-off or budget window, then run preflight again"
	case "cooldown", "busy":
		return "run preflight again in a minute"
	}
	return "test it in " + page
}

func (c *checker) metadata() {
	var src map[string]*policy.MetadataSource
	if c.p.Metadata != nil {
		src = c.p.Metadata.Sources
	}
	key := func(name string) (configured, set bool) {
		s := src[name]
		if s == nil {
			return false, false
		}
		return true, s.APIKeyEnv == "" || c.credential("", "", s.APIKeyEnv)
	}
	switch cfg, set := key("tmdb"); {
	case !cfg:
		c.add(AreaMetadata, "tmdb", Fail, "add a metadata service named tmdb with its URL and API key in "+uipages.Metadata+
			" (or metadata.sources.tmdb with api_key_env in metadata.yaml)", "no TMDb source: adding titles, search, art and release dates are off")
	case !set:
		c.add(AreaMetadata, "tmdb", Fail, c.missingCredential(src["tmdb"].APIKeyEnv, uipages.Metadata),
			"TMDb is configured but its key %s is not set", credentialName(src["tmdb"].APIKeyEnv))
	default:
		// Presence only: no request goes to TMDb here, so a wrong key or an unreachable host still passes.
		c.add(AreaMetadata, "tmdb", OK, "", "TMDb key set (configured; the connection is not tested here: a search shows whether TMDb answers)")
	}
	series := false
	for _, v := range c.p.Versions {
		series = series || v.Kind == policy.KindSeries
	}
	switch cfg, set := key("tvdb"); {
	case !cfg && series:
		c.add(AreaMetadata, "tvdb", Warn, "add a metadata service named tvdb with the address https://api4.thetvdb.com/v4 and "+
			"your TVDB key in "+uipages.Metadata+" for Sonarr-style series numbering", "no TVDB source: series use TMDb's episode numbering")
	case !cfg:
		c.add(AreaMetadata, "tvdb", OK, "", "no TVDB source (only needed for series)")
	case !set:
		c.add(AreaMetadata, "tvdb", Fail, c.missingCredential(src["tvdb"].APIKeyEnv, uipages.Metadata),
			"TVDB is configured but its key %s is not set", credentialName(src["tvdb"].APIKeyEnv))
	default:
		c.add(AreaMetadata, "tvdb", OK, "", "TVDB key set")
	}
}

func (c *checker) views(ctx context.Context) {
	switch {
	case len(c.p.Versions) == 0:
		c.add(AreaViews, "views", Warn, "create a library first; every library gets its own view at /v/<key>/radarr "+
			"or /sonarr", "no libraries, so the arr views list nothing")
	case c.o.Views == nil:
		c.add(AreaViews, "views", Warn, "run preflight from the service ("+uipages.Setup+") to ask the views", "not asked")
	default:
		for _, v := range c.o.Views(ctx) {
			switch {
			case v.Err != "":
				c.add(AreaViews, v.Path, Fail, "check the view in the views section", "%s does not answer: %s", v.Path, v.Err)
			case v.Code == 200 || v.Code == 401:
				c.add(AreaViews, v.Path, OK, "", "%s/api/v3 answers (HTTP %d without a key, as the arrs do)", v.Path, v.Code)
			default:
				c.add(AreaViews, v.Path, Fail, "check the view in the views section", "%s/api/v3/system/status answered HTTP %d", v.Path, v.Code)
			}
		}
	}
	switch {
	case c.o.APIKeys == 0:
		c.add(AreaViews, "api keys", Warn, "create an API key in "+uipages.Users+" and give it to Seerr, Homepage "+
			"and the other arr clients", "no API keys: Seerr and the arr clients cannot sign in to the views")
	case c.o.APIKeys > 0:
		c.add(AreaViews, "api keys", OK, "", "%d active API key(s)", c.o.APIKeys)
	}
}

func (c *checker) plex(ctx context.Context) {
	lib := c.p.LibrarySettings()
	if lib.Plex == nil {
		c.add(AreaPlex, "plex", Warn, "set the Plex server's URL and token in "+uipages.Plex+" so new links are "+
			"scanned at once", "no Plex server: new links wait for the media server's own scheduled scan")
		return
	}
	if lib.Plex.TokenEnv != "" && !c.credential("", "", lib.Plex.TokenEnv) {
		c.add(AreaPlex, "plex", Fail, c.missingCredential(lib.Plex.TokenEnv, uipages.Plex), "the Plex token %s is not set",
			credentialName(lib.Plex.TokenEnv))
		return
	}
	plexURL := (secrets.URLMasker{}).Mask(lib.Plex.URL)
	if c.o.Plex == nil {
		c.add(AreaPlex, "plex", Warn, "run preflight with source tests (the default)", "not asked: %s", plexURL)
		return
	}
	if err := c.o.Plex(ctx); err != nil {
		c.add(AreaPlex, "plex", Fail, "check the Plex server URL in "+uipages.Plex+" and that the container can reach it", "%s: %v",
			plexURL, strings.ReplaceAll(err.Error(), lib.Plex.URL, plexURL))
		return
	}
	c.add(AreaPlex, "plex", OK, "", "%s answers", plexURL)
}

// The guard proves Theatarr's configured HTTP paths, not DNS or a helper's outbound connections.
const egressLimits = "; the VPN guard does not cover DNS or outbound traffic from Prowlarr, Zilean, trawl or other helpers; route those through the VPN separately"

func (c *checker) egress() {
	for _, r := range c.o.Health {
		if r.Source == "VpnEgressCheck" {
			next := "check the VPN (see docs/quickstart.md, VPN egress)"
			c.add(AreaEgress, "vpn egress", healthStatus(r.Type), next, "%s%s", r.Message, egressLimits)
			return
		}
	}
	if c.p.Egress == nil {
		st := OK
		if len(c.p.Indexers) > 0 {
			st = Warn
		}
		c.add(AreaEgress, "vpn egress", st, "set up the VPN guard in "+uipages.Indexers+" to check the VPN exit before "+
			"indexer traffic", "no egress section: VPN guard off%s", egressLimits)
		return
	}
	c.add(AreaEgress, "vpn egress", Warn, "run preflight from the service ("+uipages.Setup+") for the live egress state",
		"not checked here%s", egressLimits)
}

// switches lists the global switches. They are reported, never changed: Admin › Settings changes them (DB settings).
func (c *checker) switches() {
	const next = uipages.Switches
	for _, sw := range []struct {
		key, check    string
		on            bool
		onWhy, offWhy string
	}{
		{"router", "router.enabled", c.p.RouterSettings().Enabled, "on: active libraries with grab on add to debrid", "off: nothing is added to debrid (dry runs only)"},
		{"library", "library.enabled", c.p.LibrarySettings().Enabled, "on: active libraries link into their roots", "off: no library writes and no Plex calls"},
	} {
		switch {
		case slices.Contains(c.p.ForcedOff, sw.key):
			c.add(AreaSwitches, sw.check, OK, "remove "+sw.key+" from THEATARR_FORCE_OFF and restart", "forced off by environment (THEATARR_FORCE_OFF): %s", strings.TrimPrefix(sw.offWhy, "off: "))
		case sw.on:
			c.add(AreaSwitches, sw.check, OK, next, "%s", sw.onWhy)
		default:
			c.add(AreaSwitches, sw.check, OK, next, "%s", sw.offWhy)
		}
	}
	for _, unknown := range c.p.ForceOffUnknown {
		c.add(AreaSwitches, "force off", Warn, "use router, library or notify (comma-separated) and restart",
			"%s is not a switch: it forces nothing off", unknown)
	}
}

func healthStatus(t string) Status {
	switch t {
	case health.Error:
		return Fail
	case health.Warning:
		return Warn
	}
	return OK
}

// Print writes the report as text: one line per item with its next step indented below, then the counts.
func Print(w io.Writer, r *Report) {
	for _, it := range r.Items {
		fmt.Fprintf(w, "%-4s  %-9s  %-24s %s\n", strings.ToUpper(string(it.Status)), it.Area, it.Check, it.Why)
		if it.Next != "" {
			fmt.Fprintf(w, "%42s next: %s\n", "", it.Next)
		}
	}
	fmt.Fprintf(w, "-- %d FAIL, %d WARN, %d OK\n", r.Fail, r.Warn, r.OK)
}

// Missing is the features a policy leaves off, one line each: the setup status the health checks and the first-run
// screen show. Empty when nothing basic is missing. indexers and providers are the sources that would run
// (settings.Snapshot.Ready: enabled, with their credential set when they take one), not the records configured.
func Missing(p *policy.Policy, indexers, providers int) []string {
	if p == nil {
		p = &policy.Policy{}
	}
	// Never nil: GET /setup sends it as a JSON list, and a complete install has an empty one.
	out := []string{}
	if len(p.Versions) == 0 {
		out = append(out, "no libraries: nothing is searched, grabbed or linked")
	}
	if indexers == 0 {
		out = append(out, "no enabled indexer (a Torznab or DMM one also needs its key): nothing can be searched")
	}
	if providers == 0 {
		out = append(out, "no enabled debrid provider with its API key: nothing can be added to debrid")
	}
	if p.Metadata == nil || p.Metadata.Sources["tmdb"] == nil {
		out = append(out, "no TMDb source: adding titles and search are off")
	}
	if p.LibrarySettings().MountPath == "" {
		out = append(out, "no debrid mount: nothing can be imported")
	}
	return slices.Clip(out)
}

func credentialName(env string) string {
	if env == settings.CredentialSentinel || strings.HasPrefix(env, policy.SettingsSecretPrefix) {
		return "(settings)"
	}
	return env
}

func (c *checker) missingCredential(env, page string) string {
	if credentialName(env) == "(settings)" {
		return "set the credential in " + page
	}
	return "set " + env + " in the container"
}

// Mark stored credentials without exposing their value or their internal derived name.
func (c *checker) credentialNotes() {
	notes := map[string]string{}
	note := func(area, check, entity, key, env string) {
		if !c.credential(entity, key, env) {
			return
		}
		stored := credentialName(env) == "(settings)"
		if snap := c.o.Credentials.Snap; snap != nil && entity != "" {
			stored = stored || snap.CredentialSet(entity, key, nil)
		}
		if stored {
			notes[area+"/"+check] = "; credential stored in settings"
		}
	}
	for k, pv := range c.p.Providers {
		note(AreaProviders, k+" ("+pv.Kind+")", "providers", k, pv.APIKeyEnv)
	}
	for k, ix := range c.p.Indexers {
		note(AreaIndexers, k+" ("+ix.Kind+")", "indexers", k, ix.APIKeyEnv)
	}
	if c.p.Metadata != nil {
		for k, src := range c.p.Metadata.Sources {
			note(AreaMetadata, k, "", "", src.APIKeyEnv)
		}
	}
	if plex := c.p.LibrarySettings().Plex; plex != nil {
		note(AreaPlex, "plex", "", "", plex.TokenEnv)
	}
	for i := range c.r.Items {
		it := &c.r.Items[i]
		it.Why += notes[it.Area+"/"+it.Check]
	}
}
