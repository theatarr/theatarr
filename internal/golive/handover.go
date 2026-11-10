package golive

import (
	"context"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/uipages"
)

// Arr resources, as much of them as the handover reads.
type arrItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Monitored bool   `json:"monitored"`
}

type arrQueue struct {
	TotalRecords int `json:"totalRecords"`
	Records      []struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"records"`
}

type arrIndexer struct {
	Name                  string `json:"name"`
	EnableRss             bool   `json:"enableRss"`
	EnableAutomaticSearch bool   `json:"enableAutomaticSearch"`
}

type arrDownloadClient struct {
	Name   string `json:"name"`
	Enable bool   `json:"enable"`
}

func (c *checker) handover(ctx context.Context) {
	c.homepage()
	if c.v.Follows != "" {
		c.followerHandover()
		return
	}
	k := c.o.Version
	if follower := c.o.Policy.FollowedBy(k); follower != "" {
		c.add(SecHandover, "cutover", Info, "move clients and Plex libraries/sections to "+follower+"'s roots; retire the arr and roots for "+k,
			"followed version cutover: clients and Plex move to %s's folders; %s's arr and roots retire", follower, k)
		// The arr-side checks below still apply before the arr retires: its queue, unknown titles, grabbing.
	}
	owners := c.owners()
	if len(owners) == 0 {
		c.add(SecHandover, "owner", Warn, "nothing to stop on the arr side for this version, but check which arr serves "+
			c.v.Library+" today and stop it too",
			"no import.arr instance feeds %s: Theatarr holds only the titles added through its own views", k)
	}
	for _, inst := range owners {
		a := c.o.Policy.Import.Arr[inst]
		c.add(SecHandover, "owner", Info, "retire "+inst+"'s import entry once clients and Plex use the follower "+
			"(docs/migration.md#5-hand-over-clients-and-plex-retire-the-arr)", "%s owns %s today (%s %s)", inst, k, a.Flavour, a.URL)
		c.arr(ctx, inst, a)
	}
	c.clients(owners)
}

func (c *checker) arr(ctx context.Context, inst string, a *policy.ArrSource) {
	if c.o.Arr == nil {
		c.add(SecHandover, inst, Warn, "run with the arr's key in the env ("+a.APIKeyEnv+")",
			"not checked: the arr was not read")
		return
	}
	g, err := c.o.Arr(inst)
	if err != nil {
		c.add(SecHandover, inst, Warn, "set "+a.APIKeyEnv+" (GET-only reads)", "not checked: %v", err)
		return
	}
	resource, unknown := "movie", "includeUnknownMovieItems"
	if a.Flavour == "sonarr" {
		resource, unknown = "series", "includeUnknownSeriesItems"
	}
	// Titles the arr monitors that Theatarr has not imported yet.
	var items []arrItem
	if err := g.GetJSON(ctx, resource, nil, &items); err != nil {
		c.add(SecHandover, "arr titles", Warn, "check the arr", "GET %s: %v", resource, err)
	} else if c.o.DB != nil {
		known := map[int64]bool{}
		rows, err := c.o.DB.QueryContext(ctx, `SELECT arr_id FROM arr_title WHERE instance = ?`, inst)
		if err == nil {
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					known[id] = true
				}
			}
			rows.Close()
		}
		var missing []string
		monitored := 0
		for _, it := range items {
			if !it.Monitored {
				continue
			}
			monitored++
			if !known[it.ID] {
				missing = append(missing, it.Title)
			}
		}
		if len(missing) > 0 {
			c.add(SecHandover, "arr titles", Warn, "let the next arr.import run (every 15 min) before removing "+inst+
				" from import.arr; the last import is what Theatarr keeps",
				"%s monitors %d %s titles Theatarr has not imported: %s", inst, len(missing), resource, list(missing, 3))
		} else {
			c.add(SecHandover, "arr titles", Pass, "", "Theatarr knows all %d titles %s monitors", monitored, inst)
		}
	} else {
		c.add(SecHandover, "arr titles", Warn, noDB, "%s has %d titles; not compared with Theatarr's (no database)",
			inst, len(items))
	}
	var q arrQueue
	if err := g.GetJSON(ctx, "queue", url.Values{"page": {"1"}, "pageSize": {"20"}, unknown: {"true"}}, &q); err != nil {
		c.add(SecHandover, "arr queue", Warn, "check the arr", "GET queue: %v", err)
	} else if q.TotalRecords > 0 {
		var names []string
		for _, r := range q.Records {
			if !slices.Contains(names, r.Title) {
				names = append(names, r.Title)
			}
		}
		c.add(SecHandover, "arr queue", Warn, "after stopping the arr's grabbing, let these finish and import before "+
			"removing its download client; Theatarr does not adopt the arr's queue",
			"%s has %d downloads queued: %s", inst, q.TotalRecords, list(names, 3))
	} else {
		c.add(SecHandover, "arr queue", Pass, "", "%s's queue is empty", inst)
	}
	active := c.v.Mode == policy.ModeActive
	var ixs []arrIndexer
	if err := g.GetJSON(ctx, "indexer", nil, &ixs); err != nil {
		c.add(SecHandover, "arr grabbing", Warn, "check the arr", "GET indexer: %v", err)
	} else {
		rss, auto := 0, 0
		for _, ix := range ixs {
			if ix.EnableRss {
				rss++
			}
			if ix.EnableAutomaticSearch {
				auto++
			}
		}
		switch {
		case rss+auto == 0:
			c.add(SecHandover, "arr grabbing", Pass, "", "%s has RSS and automatic search off on all %d indexers", inst,
				len(ixs))
		case active:
			c.add(SecHandover, "arr grabbing", Fail, "turn RSS and automatic search off in "+inst+" now: both grab the "+
				"same titles", "%s still grabs (%d RSS, %d automatic search indexers) while %s is active", inst, rss,
				auto, c.o.Version)
		default:
			c.add(SecHandover, "arr grabbing", Info, "stopped when the arr retires (docs/migration.md#5-hand-over-clients-and-plex-retire-the-arr)",
				"%s grabs today: %d of %d indexers with RSS, %d with automatic search", inst, rss, len(ixs), auto)
		}
	}
	var dcs []arrDownloadClient
	if err := g.GetJSON(ctx, "downloadclient", nil, &dcs); err != nil {
		c.add(SecHandover, "arr client", Warn, "check the arr", "GET downloadclient: %v", err)
	} else {
		var on []string
		for _, d := range dcs {
			if d.Enable {
				on = append(on, d.Name)
			}
		}
		switch {
		case len(on) == 0:
			c.add(SecHandover, "arr client", Pass, "", "%s has no enabled download client", inst)
		case active:
			c.add(SecHandover, "arr client", Warn, "disable it once its queue is empty", "%s still has download "+
				"clients enabled: %s", inst, strings.Join(on, ", "))
		default:
			c.add(SecHandover, "arr client", Info, "disabled once its queue is empty when the arr retires (docs/migration.md#5-hand-over-clients-and-plex-retire-the-arr)",
				"%s sends to: %s", inst, strings.Join(on, ", "))
		}
	}
}

func (c *checker) homepage() {
	p, k := c.o.Policy, c.o.Version
	source, target := k, k
	owners := c.owners()
	if c.v.Follows != "" {
		source = c.v.Follows
		if p.Import != nil {
			for inst, a := range p.Import.Arr {
				if a.Version == source {
					owners = append(owners, inst)
				}
			}
		}
		sort.Strings(owners)
	} else if follower := p.FollowedBy(k); follower != "" {
		target = follower
	}
	old := "the arr for " + source
	if len(owners) > 0 {
		old = strings.Join(owners, ", ")
	}
	flavour := "radarr"
	if c.v.Kind == policy.KindSeries {
		flavour = "sonarr"
	}
	base := c.o.PublicURL + "/v/" + target + "/" + flavour
	fix := "at cutover, point the Homepage widget for " + old + " at " + base + " with a view key"
	switch {
	case slices.Contains(c.v.GoliveDone, "homepage"):
		c.add(SecHandover, "client Homepage", Pass, "", "Homepage cutover acknowledged in versions.%s.golive_done", k)
	case c.v.Mode == policy.ModeActive && p.RouterSettings().Enabled:
		c.add(SecHandover, "client Homepage", Warn, fix, "Homepage tiles for %s have not been acknowledged; Theatarr cannot read services.yaml", old)
	default:
		c.add(SecHandover, "client Homepage", Info, fix, "Homepage tiles for %s need repointing at cutover; Theatarr cannot read services.yaml", old)
	}
}

// clients lists, per client that talks to the owning arr today, the change it needs (docs/design/devices.md,
// docs/design/arrview.md "Cutover").
func (c *checker) clients(owners []string) {
	p, k := c.o.Policy, c.o.Version
	old := "the arr"
	if len(owners) > 0 {
		var us []string
		for _, o := range owners {
			us = append(us, o+" ("+p.Import.Arr[o].URL+")")
		}
		old = strings.Join(us, ", ")
	}
	var views []*policy.View
	var names []string
	for _, vk := range sortedViews(p) {
		for _, x := range p.Views[vk].Versions {
			if x == k {
				views = append(views, p.Views[vk])
				names = append(names, vk)
				break
			}
		}
	}
	flavour := "radarr"
	if c.v.Kind == policy.KindSeries {
		flavour = "sonarr"
	}
	base := c.o.PublicURL + "/v/" + k + "/" + flavour
	key := "a Theatarr API key (" + uipages.Users + ")"
	if len(views) > 0 {
		base = c.o.PublicURL + views[0].Path
		key = views[0].APIKeyEnv + " or a per-client key from Settings"
		if strings.HasPrefix(views[0].APIKeyEnv, policy.SettingsSecretPrefix) {
			// A key kept in the settings database: its derived name is not something to type into a client.
			key = "the view's API key or a per-client key (" + uipages.Apps + ")"
		}
	}
	fix := "back up the client's settings first; the rollback restores them"
	say := func(client, format string, args ...any) {
		c.add(SecHandover, "client "+client, Info, fix, format, args...)
	}
	profile := "Rules default"
	if len(views) > 0 {
		for _, name := range sortedProfiles(views[0].Profiles) {
			if vs := views[0].Profiles[name]; len(vs) == 1 && vs[0] == k {
				profile = name
				break
			}
		}
	}
	say("Seerr", "Settings › Services: the %s server for %s → %s (host, port, URL base), key %s; while other versions "+
		"are still on their arrs pick profile %q, root %s, re-select tags; stored ids keep answering through alias_from. "+
		"After the last cutover Seerr keeps one Radarr and one Sonarr server on the views with profile \"Rules default\" "+
		"and no anime override rules: Theatarr picks the versions and decides anime (docs/design/arrview.md \"Cutover\")",
		arrName(flavour), old, base, key, profile, c.v.Library)
	cb := ""
	if cl := p.Clients[c.v.Client]; cl != nil && cl.Kind == "decypharr" && cl.CallbackView != "" {
		cb = " (callback view " + cl.CallbackView + ")"
	}
	say("decypharr", "its arr entry for %s → %s%s, key %s; repairs then go through Theatarr (file deletes answer 409 "+
		"while the library gate is closed)", old, base, cb, key)
	push := "a push is decided like a search result: its indexer must be a configured one this version lists"
	say("autobrr", "its %s client for %s → %s; %s", arrName(flavour), old, base, push)
	say("Ruddarr/LunaSea", "the instance %s → %s with a key; Ruddarr's webhook is re-installed through the view and "+
		"sent only with notify.enabled", old, base)
	if len(names) > 0 {
		c.add(SecHandover, "views", Info, "", "clients reach %s through view %s; other versions in it keep their own "+
			"mode", k, strings.Join(names, ", "))
	}
}

func arrName(flavour string) string {
	if flavour == "sonarr" {
		return "Sonarr"
	}
	return "Radarr"
}

func sortedProfiles(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
