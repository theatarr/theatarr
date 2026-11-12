package golive

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/preflight"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/uipages"
)

func (c *checker) policy(ctx context.Context) {
	v, k := c.v, c.o.Version
	switch v.Mode {
	case policy.ModeActive:
		c.add(SecPolicy, "mode", Info, "", "%s is already Live; the checks below describe it as it runs", k)
	default:
		c.add(SecPolicy, "mode", Pass, "", "%s is Watch only; nothing grabs or links for it yet", k)
	}
	if v.Grab {
		c.add(SecPolicy, "grab", Info, "", "versions.%s.grab is on: while Live and with router.enabled (currently %t) "+
			"the router adds torrents for it", k, c.o.Policy.RouterSettings().Enabled)
	} else if v.Mode == policy.ModeActive && c.o.Policy.RouterSettings().Enabled {
		c.add(SecPolicy, "grab", Warn, "turn versions."+k+".grab on (versions editor, confirmed), or set it back to "+
			"Watch only", "versions.%s.grab is off while it is Live and router.enabled is true: it links but adds "+
			"nothing, and no automatic search (wanted, add-time, RSS) runs for it", k)
	} else {
		c.add(SecPolicy, "grab", Info, "turn versions."+k+".grab on (versions editor, confirmed) when it should grab",
			"versions.%s.grab is off: it decides and links but adds nothing, and gets no automatic search, even "+
				"with router.enabled", k)
	}
	if follower := c.o.Policy.FollowedBy(k); follower != "" {
		c.add(SecPolicy, "cutover", Info, "", "cut over through its follower %s (docs/migration.md#5-hand-over-clients-and-plex-retire-the-arr)", follower)
	}
	c.profile()
	c.libraryPolicy(ctx)
	c.storage()
	c.rules()
	c.budgets()
}

func (c *checker) profile() {
	p, v := c.o.Policy, c.v
	pr := p.Profiles[v.Profile]
	if pr == nil {
		c.add(SecPolicy, "profile", Fail, "set versions."+c.o.Version+".profile to a profile of the policy",
			"profile %q is not in the policy", v.Profile)
		return
	}
	if pr.FromArr != "" {
		c.add(SecPolicy, "profile", Warn, "give the version its own profile ("+uipages.Profiles+") before the arr retires",
			"profile %q is the arr's own (from_arr: %s): the shadow evidence measures arr parity, and the profile "+
				"stops loading once the instance leaves import.arr", v.Profile, pr.FromArr)
	}
	build := func(name string) {
		if c.o.Profile == nil {
			c.add(SecPolicy, "profile build", Warn, "run with the decision engine (theatarr golive check builds it unless "+
				"-no-engine)", "not checked: profile %q was not built", name)
			return
		}
		if err := c.o.Profile(name, v.Kind); err != nil {
			c.add(SecPolicy, "profile build", Fail, "fix the profile (theatarr policy validate)",
				"profile %q does not build for %s: %v", name, v.Kind, err)
			return
		}
		c.add(SecPolicy, "profile build", Pass, "", "profile %q builds for a %s version", name, v.Kind)
	}
	build(v.Profile)
}

func (c *checker) libraryPolicy(ctx context.Context) {
	p, v, k := c.o.Policy, c.v, c.o.Version
	lib := p.LibrarySettings()
	if lib.Enabled {
		c.add(SecPolicy, "library.enabled", Info, "", "library.enabled is on (the gate still needs mode: active)")
	} else {
		c.add(SecPolicy, "library.enabled", Info, "turn Write library links on before the version grabs",
			"library.enabled is off: nothing links or calls Plex")
	}
	lv := lib.Versions[k]
	switch {
	case v.Library == "":
		c.add(SecPolicy, "library root", Fail, "set versions."+k+".library to the arr's root folder",
			"versions.%s.library is not set", k)
	case lv == nil || len(lv.Roots) == 0:
		c.add(SecPolicy, "library root", Fail, "add library.versions."+k+".roots: ["+v.Library+"]",
			"library.versions.%s is missing: the library gate never writes for this version", k)
	}
	if lv != nil {
		follower := p.FollowedBy(k)
		for _, root := range lv.Roots {
			if follower != "" {
				c.dir("library root", root, false, "")
				c.add(SecPolicy, "library root", Info, "", "read-only by design: followed by %s; cutover switches to %s's folders", follower, follower)
			} else {
				c.dir("library root", root, true, "bind "+root+" read-write into the Theatarr container (theatarr-compose.yaml) at the "+
					"same path Plex uses; a host change the user approves")
			}
		}
		if lv.PlexSection == 0 {
			c.add(SecPolicy, "plex section", Warn, "set library.versions."+k+".plex_section to the Plex library that "+
				"shows "+v.Library, "no Plex section: new links wait for Plex's own scheduled scan")
		} else if lib.Plex == nil {
			c.add(SecPolicy, "plex section", Fail, "configure library.plex", "plex_section %d set but library.plex is not",
				lv.PlexSection)
		} else {
			c.add(SecPolicy, "plex section", Pass, "", "Plex section %d is scanned after each link", lv.PlexSection)
		}
	}
	if lib.TrashDir == "" {
		c.add(SecPolicy, "trash", Fail, "set library.trash.dir (outside every root and the mount)",
			"library.trash.dir is not set: replaced links have nowhere to go")
	} else {
		c.dir("trash", lib.TrashDir, true, "bind "+lib.TrashDir+" read-write into the Theatarr container; a host change")
	}
	if lib.MountPath == "" {
		c.add(SecPolicy, "mount", Fail, "set library.mount.path", "library.mount.path is not set")
	} else {
		c.dir("mount", filepath.Join(lib.MountPath, lib.AllDir), false, "bind the zurg mount read-only into the container")
	}
	c.naming(ctx)
}

// dir checks that a directory exists and, when write is set, that Theatarr may write in it.
func (c *checker) dir(check, path string, write bool, fix string) {
	fact := preflight.InspectDirectory(c.o.FS, path, write)
	switch {
	case !fact.Checked:
		c.add(SecPolicy, check, Warn, "run inside the container", "not checked: %s (no filesystem access)", path)
	case fact.Problem != nil:
		c.add(SecPolicy, check, Fail, fix, "%s", fact.Problem)
	case !write:
		c.add(SecPolicy, check, Pass, "", "%s is there", path)
	default:
		c.add(SecPolicy, check, Pass, "", "%s is there and writable by Theatarr", path)
	}
}

func (c *checker) naming(ctx context.Context) {
	v, k := c.v, c.o.Version
	n := v.Naming
	switch {
	case n.FromArr != "":
		msg := fmt.Sprintf("names come from %s (from_arr)", n.FromArr)
		if c.o.DB == nil {
			c.add(SecPolicy, "naming", Pass, "", "%s", msg)
			return
		}
		var rows int
		if err := c.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_naming WHERE instance = ?`,
			n.FromArr).Scan(&rows); err != nil || rows == 0 {
			c.add(SecPolicy, "naming", Warn, "theatarr library naming-sync (GET from the arr), or let theatarr serve's daily sync run",
				"%s, but the database holds no naming read from it yet", msg)
			return
		}
		c.add(SecPolicy, "naming", Pass, "", "%s; the database holds it", msg)
	case n.FromVersion != "":
		c.add(SecPolicy, "naming", Pass, "", "naming comes from version %s", n.FromVersion)
	case n.File == "" && n.Folder == "":
		c.add(SecPolicy, "naming", Warn, "set versions."+k+".naming.from_arr to the owning instance (or copy its "+
			"templates) so new links get the arr's names",
			"no naming: the %s defaults apply, which may differ from the arr's names in this root", v.Kind)
	default:
		c.add(SecPolicy, "naming", Pass, "", "explicit naming templates")
	}
}

func (c *checker) storage() {
	p, v, k := c.o.Policy, c.v, c.o.Version
	if v.Storage == policy.StorageSeedbox {
		c.add(SecPolicy, "storage", Info, "", "seedbox storage through client %q", v.Client)
		return
	}
	if v.ViaClient() {
		c.client() // the client adds to its own account: the version's providers are not used
		return
	}
	facts := preflight.InspectProviders(p, v.Providers, c.o.Credential)
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].Order < facts[j].Order })
	provs := make([]string, 0, len(facts))
	for _, fact := range facts {
		provs = append(provs, fact.Key)
	}
	if len(provs) == 0 {
		c.add(SecPolicy, "router fallback", Fail, "list providers in versions."+k+".providers",
			"a debrid version with no providers")
		return
	}
	var keyed, unkeyed []string
	for _, fact := range facts {
		if fact.Credential {
			keyed = append(keyed, fact.Key)
		} else {
			unkeyed = append(unkeyed, fact.Key)
		}
	}
	switch {
	case len(keyed) == 0:
		c.add(SecPolicy, "router fallback", Fail, "store the providers' keys under "+uipages.Providers+" (or "+
			"set the env variables they name)",
			"fallback %s, and none has a key: nothing could be added", strings.Join(provs, " → "))
	case len(unkeyed) > 0:
		c.add(SecPolicy, "router fallback", Warn, "set the missing keys, or drop those providers from versions."+k+
			".providers", "fallback %s; without a key (skipped): %s", strings.Join(provs, " → "), strings.Join(unkeyed, ", "))
	default:
		c.add(SecPolicy, "router fallback", Pass, "", "fallback %s, every provider has a key", strings.Join(provs, " → "))
	}
	for _, pk := range provs {
		end, ok := c.o.ProviderExpiry[pk]
		if !ok {
			continue
		}
		left := end.Sub(c.o.Now)
		switch {
		case left <= 0:
			c.add(SecPolicy, "provider plan", Warn, "renew it, or drop "+pk+" from versions."+k+".providers",
				"%s's plan ended %s: the router finds it refusing and tries the next provider", pk, end.Format("2006-01-02"))
		case left <= 30*24*time.Hour:
			c.add(SecPolicy, "provider plan", Warn, "renew it before then, or drop "+pk+" from versions."+k+
				".providers", "%s's plan ends %s (%.0f days)", pk, end.Format("2006-01-02"), left.Hours()/24)
		default:
			c.add(SecPolicy, "provider plan", Pass, "", "%s's plan runs to %s", pk, end.Format("2006-01-02"))
		}
	}
	c.client()
}

// client: the version's download client, and what it does for the version.
func (c *checker) client() {
	p, v, k := c.o.Policy, c.v, c.o.Version
	if v.Client == "" && v.Storage == policy.StorageDebrid {
		c.add(SecPolicy, "client", Info, "", "no download client: adds go to the providers directly")
	} else if cl := p.Clients[v.Client]; cl == nil {
		c.add(SecPolicy, "client", Warn, "set versions."+k+".client", "no download client %q in the policy", v.Client)
	} else if v.ViaClient() {
		c.add(SecPolicy, "client", Info, "", "grabs go to client %s (add_via: client), which adds to its own debrid "+
			"account; it calls back through view %q", v.Client, cl.CallbackView)
		c.add(SecPolicy, "add via client", Warn, "set cached-only adds, pacing and stalled-download removal on client "+
			v.Client, "client %s's own settings decide cached-only adds, add pacing and stalled downloads for %s, not "+
			"router.cached_only, the providers' pacing or stalled_after", v.Client, k)
	} else if cl.Kind == "decypharr" {
		c.add(SecPolicy, "client", Info, "repoint decypharr's arr entry (handover)",
			"decypharr calls back through view %q; adds go to the providers directly", cl.CallbackView)
	}
}

// rules: cached_only, trackers, pushes.
func (c *checker) rules() {
	p, v := c.o.Policy, c.v
	rs := p.RouterSettings()
	switch {
	case v.ViaClient():
		// The client decides cached-only adds (the "add via client" warning says so): nothing to check here.
	case rs.CachedOnly:
		c.add(SecPolicy, "cached_only", Pass, "", "router.cached_only is on: known-uncached releases are skipped, "+
			"uncached RD adds of ours deleted again")
	default:
		c.add(SecPolicy, "cached_only", Warn, "set router.cached_only: true unless uncached downloads are wanted",
			"router.cached_only is off: uncached adds stay on the provider and download")
	}
	for _, pk := range v.Providers {
		if pv := p.Providers[pk]; pv != nil && pv.Kind == "alldebrid" && rs.CachedOnly && !v.ViaClient() {
			if rs.AllDebridDeleteUncached {
				c.add(SecPolicy, "cached_only", Info, "", "our own just-made uncached AllDebrid add on %s is deleted "+
					"and the next provider tried (alldebrid_delete_uncached on; list/upload race accepted)", pk)
			} else {
				c.add(SecPolicy, "cached_only", Info, "", "an uncached AllDebrid add of ours is kept (downloading on %s) "+
					"and the next provider tried (alldebrid_delete_uncached off)", pk)
			}
		}
	}
	if len(rs.Trackers) > 0 {
		c.add(SecPolicy, "trackers", Info, "", "magnets carry %d public trackers", len(rs.Trackers))
	}
	c.add(SecPolicy, "release push", Info, "", "a push needs an API key, and its indexer must be a configured one "+
		"this version lists (matched by name)")
}

func (c *checker) budgets() {
	p, v, k := c.o.Policy, c.v, c.o.Version
	var none []string
	for _, ik := range v.Indexers {
		if ix := p.Indexers[ik]; ix != nil && ix.Budget == "" {
			none = append(none, "indexer "+ik)
		}
	}
	for _, pk := range v.Providers {
		if pv := p.Providers[pk]; pv != nil && pv.Budget == "" {
			none = append(none, "provider "+pk)
		}
	}
	if len(none) > 0 {
		c.add(SecPolicy, "budgets", Warn, "give each a budget (budgets section) so the wanted loop cannot hammer it",
			"no budget: %s", strings.Join(none, ", "))
	} else {
		c.add(SecPolicy, "budgets", Pass, "", "every indexer and provider of %s has a budget", k)
	}
	w := p.WantedSettings()
	c.add(SecPolicy, "wanted loop", Info, "", "after the switch the wanted loop searches every %v, at most %d an hour, "+
		"an item again after %v (missing %v, cutoff unmet %s)", w.Interval, w.PerHour, w.AgainAfter, w.Missing,
		w.CutoffUnmet)
}

// secrets lists, by name, every secret the active version will use, and whether it is set.
func (c *checker) secrets() {
	p, v, k := c.o.Policy, c.v, c.o.Version
	// A need is one credential: a provider or indexer record (entity, key), or an *_env name. One the settings
	// database holds is named by its settings path, never by its derived env name.
	type need struct {
		entity, key string
		name, use   string
		st          Status // when unset
		fix         string
	}
	var needs []need
	addEnv := func(name, use string, st Status, fix string) {
		if name != "" {
			needs = append(needs, need{name: name, use: use, st: st, fix: fix})
		}
	}
	addRecord := func(entity, key, env, use string) {
		needs = append(needs, need{entity: entity, key: key, name: env, use: use, st: Warn,
			fix: "store the key under " + map[string]string{"providers": uipages.Providers, "indexers": uipages.Indexers}[entity] +
				" (or set the env variable it names)"})
	}
	setFix := "set it in the container's environment; a host change"
	for _, pk := range v.Providers {
		if pv := p.Providers[pk]; pv != nil {
			addRecord("providers", pk, pv.APIKeyEnv, "provider "+pk)
		}
	}
	for _, ik := range v.Indexers {
		// An indexer without a key (public sites) needs none.
		if ix := p.Indexers[ik]; ix != nil && ix.APIKeyEnv != "" {
			addRecord("indexers", ik, ix.APIKeyEnv, "indexer "+ik)
		}
	}
	// A download client's login matters only when the version routes to it: seedbox storage. Debrid versions add to
	// the providers directly, so a configured but unused qBittorrent client is only noted.
	if cl := p.Clients[v.Client]; cl != nil && v.ViaClient() {
		// add_via: client: every grab goes through this client, which adds to its own debrid account.
		addEnv(cl.UsernameEnv, "client "+v.Client, Warn, setFix)
		addEnv(cl.PasswordEnv, "client "+v.Client, Warn, setFix)
	} else if cl != nil && cl.Kind == "qbittorrent" && v.Storage == policy.StorageSeedbox {
		addEnv(cl.UsernameEnv, "client "+v.Client, Fail, setFix)
		addEnv(cl.PasswordEnv, "client "+v.Client, Fail, setFix)
	} else {
		for _, ck := range sortedClients(p) {
			cl := p.Clients[ck]
			if cl.Kind != "qbittorrent" {
				continue
			}
			var unset []string
			for _, n := range []string{cl.UsernameEnv, cl.PasswordEnv} {
				if n != "" && !c.o.Credential("", "", n) {
					unset = append(unset, n)
				}
			}
			if len(unset) > 0 {
				c.add(SecSecrets, "client "+ck, Info, "", "%s not set; %s does not route to client %s", strings.Join(unset, ", "), k, ck)
			}
		}
	}
	if lib := p.LibrarySettings(); lib.Plex != nil {
		addEnv(lib.Plex.TokenEnv, "Plex scans", Warn, setFix)
	}
	src := func(name string) *policy.MetadataSource {
		if p.Metadata == nil {
			return nil
		}
		return p.Metadata.Sources[name]
	}
	tmdb := src("tmdb")
	if tmdb != nil {
		addEnv(tmdb.APIKeyEnv, "TMDb metadata", Warn, setFix)
	}
	if v.Kind == policy.KindSeries {
		tvdb := src("tvdb")
		switch {
		case tvdb == nil && tmdb == nil:
			c.add(SecSecrets, "series metadata", Fail, "configure metadata.sources.tvdb",
				"no TVDB or TMDb source: nothing refreshes the series once the import stops")
		case tvdb != nil:
			st := Fail
			if tmdb != nil && !(v.Anime != nil && *v.Anime) {
				st = Warn // TMDb is the fallback for non-anime series
			}
			addEnv(tvdb.APIKeyEnv, "TVDB series metadata", st, setFix)
		}
	}
	var viewKeys []string
	for _, vk := range sortedViews(p) {
		vw := p.Views[vk]
		for _, x := range vw.Versions {
			if x == k {
				addEnv(vw.APIKeyEnv, "view "+vk+" ("+vw.Path+")", Warn,
					"set it, or create one API key per client in "+uipages.Users+" and give clients those")
				viewKeys = append(viewKeys, vk)
				break
			}
		}
	}
	if len(viewKeys) == 0 {
		c.add(SecSecrets, "views", Warn, "add the version to a view (views section) so Seerr and the apps reach it",
			"no configured view serves %s; only /v/%s/… answers", k, k)
	}
	if ns := p.NotifySettings(); ns.Enabled {
		for _, tk := range sortedTargets(ns.Targets) {
			addEnv(ns.Targets[tk].AuthHeaderEnv, "webhook "+tk, Warn, setFix)
		}
	}
	// One line per credential, with every use; unset, it takes the strictest status of its uses.
	var order []string
	byName := map[string]*need{}
	for _, n := range needs {
		id := n.name
		if n.entity != "" {
			id = n.entity + "/" + n.key
		}
		x := byName[id]
		if x == nil {
			cp := n
			byName[id] = &cp
			order = append(order, id)
			continue
		}
		if !strings.Contains(x.use, n.use) {
			x.use += ", " + n.use
		}
		if x.st != Fail && n.st == Fail {
			x.st, x.fix = Fail, n.fix
		}
	}
	for _, id := range order {
		n := byName[id]
		label := n.name
		if n.name == settings.CredentialSentinel || strings.HasPrefix(n.name, policy.SettingsSecretPrefix) {
			label = n.use + " (settings)"
			if n.entity == "" {
				n.fix = "set it in " + uipages.Settings + ", where it is stored"
			}
		}
		if c.o.Credential(n.entity, n.key, n.name) {
			c.add(SecSecrets, label, Pass, "", "set (%s)", n.use)
		} else {
			c.add(SecSecrets, label, n.st, n.fix, "not set (%s)", n.use)
		}
	}
}

func sortedClients(p *policy.Policy) []string {
	out := make([]string, 0, len(p.Clients))
	for k := range p.Clients {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedViews(p *policy.Policy) []string {
	out := make([]string, 0, len(p.Views))
	for k := range p.Views {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedTargets(m map[string]*policy.NotifyTarget) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
