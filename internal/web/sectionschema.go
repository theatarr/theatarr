package web

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// Bounds for durations are seconds; byte sizes are bytes. ZeroAllowed is distinct
// from Min: zero may turn a feature off while positive values have a minimum.
func sectionSchemas() map[string][]SchemaField {
	var docs map[string]map[string]string
	if err := json.Unmarshal(sectionFieldDocs, &docs); err != nil {
		panic(err)
	}
	out := map[string][]SchemaField{}
	p := reflect.TypeOf(policy.Policy{})
	for _, section := range settings.Sections {
		for i := 0; i < p.NumField(); i++ {
			f := p.Field(i)
			if strings.Split(f.Tag.Get("yaml"), ",")[0] == section.Key {
				t := f.Type
				if t.Kind() == reflect.Map {
					out[section.Key] = []SchemaField{sectionType(t, section.Key, "", docs)}
				} else {
					out[section.Key] = sectionFields(t, section.Key, docs)
				}
			}
		}
	}
	return out
}
func sectionFields(t reflect.Type, path string, docs map[string]map[string]string) []SchemaField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []SchemaField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		full := path + "." + name
		if full == "library.enabled" || full == "notify.enabled" || full == "router.enabled" || full == "library.versions.*.roots" {
			continue
		}
		// library.mount.decypharr has no control yet: the form never names the key, so a section saved from it keeps
		// the block as stored, set or absent.
		if full == "library.mount.decypharr" {
			continue
		}
		sf := sectionType(f.Type, full, name, docs)
		sf.Help = docs[t.Name()][name]
		if strings.HasSuffix(name, "_env") && f.Type.Kind() == reflect.String {
			secret := sf
			secret.Name = strings.TrimSuffix(name, "_env")
			secret.Label = humanLabel(secret.Name)
			if label := sectionLabels[strings.TrimSuffix(full, "_env")]; label != "" {
				secret.Label = label
			}
			secret.Type = "secret"
			secret.Path = strings.TrimSuffix(full, "_env")
			secret.Help = "Stored credential. Leave unchanged to keep the current value."
			out = append(out, secret)
			sf.Advanced = true
			sf.Help += " Environment variable fallback."
			sf.Label = secret.Label + " environment variable fallback"
		}
		out = append(out, sf)
	}
	return out
}
func sectionType(t reflect.Type, path, name string, docs map[string]map[string]string) SchemaField {
	f := SchemaField{Name: name, Label: humanLabel(name)}
	if t.Kind() == reflect.Pointer {
		f.Nullable = true
		t = t.Elem()
	}
	switch t {
	case reflect.TypeOf(policy.Duration(0)):
		f.Type = "duration"
	case reflect.TypeOf(policy.ByteSize(0)):
		f.Type = "bytes"
	default:
		switch t.Kind() {
		case reflect.Bool:
			f.Type = "boolean"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint64:
			f.Type = "integer"
		case reflect.Float32, reflect.Float64:
			f.Type = "number"
		case reflect.String:
			f.Type = "string"
		case reflect.Struct:
			f.Type = "object"
			f.Fields = sectionFields(t, path, docs)
		case reflect.Map:
			e := t.Elem()
			if e.Kind() == reflect.Pointer {
				e = e.Elem()
			}
			switch e.Kind() {
			case reflect.Struct:
				f.Type = "keyed"
				f.Fields = sectionFields(e, path+".*", docs)
			case reflect.String:
				f.Type = "string_map"
			case reflect.Int:
				f.Type = "map"
			default:
				panic(fmt.Sprintf("unsupported section map %s", path))
			}
		case reflect.Slice:
			e := t.Elem()
			if e.Kind() == reflect.Pointer {
				e = e.Elem()
			}
			if e.Kind() == reflect.Struct {
				f.Type = "object_list"
				f.Fields = sectionFields(e, path+".*", docs)
			} else {
				f.Type = "list"
			}
		default:
			panic(fmt.Sprintf("unsupported section field %s: %s", path, t))
		}
	}
	if name == "" {
		f.Label = humanLabel(path)
		f.Help = "Named connections."
	}
	if a, ok := sectionAnnotations[path]; ok {
		f.Enum = a.Enum
		f.Min = a.Min
		f.Max = a.Max
		f.Default = a.Default
		f.Unit = a.Unit
		f.Group = a.Group
		f.ZeroAllowed = a.ZeroAllowed
		f.Advanced = a.Advanced
		if len(a.Enum) > 0 && f.Type == "string" {
			f.Type = "enum"
		}
		if a.Label != "" {
			f.Label = a.Label
		}
	}
	return f
}
func humanLabel(s string) string {
	words := strings.Split(s, "_")
	for i, w := range words {
		switch w {
		case "url", "api", "vpn", "rss", "ttl", "rc":
			words[i] = strings.ToUpper(w)
		default:
			if i == 0 && len(w) > 0 {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
	}
	return strings.Join(words, " ")
}
func bounds(min, max float64, def string) SchemaField {
	f := SchemaField{Min: &min, Default: def}
	if max > 0 {
		f.Max = &max
	}
	return f
}
func durationBounds(min, max time.Duration, def string) SchemaField {
	return bounds(min.Seconds(), max.Seconds(), def)
}

var sectionAnnotations = map[string]SchemaField{
	"router.wanted.enabled":                       {Default: "false"},
	"metadata.series.interval":                    durationBounds(policy.MinSeriesMetaInterval, 0, "1h"),
	"metadata.series.per_run":                     bounds(1, policy.MaxSeriesMetaPerRun, "50"),
	"metadata.lists.interval":                     durationBounds(policy.MinListInterval, 0, "24h"),
	"metadata.anime.refresh_interval":             durationBounds(policy.MinAnimeRefreshInterval, 0, "168h"),
	"provider_sync.interval":                      durationBounds(policy.MinProviderSyncInterval, 0, "6h"),
	"clients.*.kind":                              {Enum: []string{"decypharr", "qbittorrent"}},
	"clients.*.import_from":                       {Enum: []string{policy.ImportFromMount, policy.ImportFromDownloadFolder}, Default: policy.ImportFromMount},
	"import.arr.*.flavour":                        {Enum: []string{"radarr", "sonarr"}},
	"router.wanted.cutoff_unmet":                  {Enum: []string{string(policy.CutoffQuality), string(policy.CutoffScore), string(policy.CutoffOff)}},
	"metadata.art.prewarm":                        {Enum: append([]string{"off"}, policy.ImageSizes...)},
	"notify.targets.*.events":                     {Enum: policy.NotifyEvents},
	"notify.targets.*.format":                     {Enum: []string{policy.NotifyFormatDiscord, policy.NotifyFormatNtfy}},
	"notify.digest_at":                            {Default: policy.DefaultDigestAt},
	"playlists.orders":                            {Enum: []string{"release", "watch", "story"}},
	"egress.require_vpn_for":                      {Enum: []string{policy.EgressIndexers, policy.EgressProviders, policy.EgressMetadata}},
	"torrents.backup.every":                       {Min: floatPtr(policy.MinTorrentBackupEvery.Seconds()), ZeroAllowed: true, Default: "0s"},
	"torrents.backup.keep":                        bounds(1, policy.MaxTorrentBackupKeep, fmt.Sprint(policy.DefaultTorrentBackupKeep)),
	"notify.retries":                              bounds(0, 20, "5"),
	"notify.poll":                                 durationBounds(time.Second, 10*time.Minute, "15s"),
	"egress.interval":                             durationBounds(10*time.Second, 0, "2m"),
	"egress.max_age":                              durationBounds(0, 0, "3 × interval"),
	"egress.timeout":                              durationBounds(0, 0, "10s"),
	"library.probe.max_bytes":                     bounds(1<<20, policy.MaxLibraryProbeBytes, "16777216"),
	"library.sample.min_runtime_ratio":            bounds(0, 1, "0.5"),
	"library.trash.keep_days":                     bounds(0, 0, "30"),
	"library.versions.*.plex_section":             bounds(0, 0, "0"),
	"library.plex.prewarm_rate":                   {Min: floatPtr(policy.MinPrewarmRate), Default: "25MB", Unit: "bytes/s"},
	"library.plex.prewarm_max_size":               {Default: "20GB"},
	"ui.images.max_bytes":                         bounds(policy.MinImageMaxBytes, 0, "2GiB"),
	"metadata.art.per_run":                        bounds(1, policy.MaxArtPerRun, "200"),
	"metadata.art.interval":                       durationBounds(policy.MinArtInterval, 0, "6h"),
	"cardigann.definitions.refresh":               durationBounds(time.Hour, 0, "24h"),
	"playlists.interval":                          durationBounds(policy.MinPlaylistInterval, 0, "6h"),
	"library.mount.wait_timeout":                  durationBounds(time.Minute, 0, "6m"),
	"library.mount.poll_interval":                 durationBounds(policy.MinLibraryPollInterval, 0, "20s"),
	"library.mount.stat_timeout":                  durationBounds(time.Second, 0, "5s"),
	"library.plex.debounce":                       durationBounds(time.Second, 0, "30s"),
	"library.relink_interval":                     durationBounds(policy.MinLibraryRelinkInterval, 0, "1h"),
	"library.dangling.interval":                   durationBounds(policy.MinLibraryRelinkInterval, 0, "6h"),
	"library.mount.zurg_log_interval":             durationBounds(policy.MinZurgLogInterval, policy.MaxZurgLogInterval, "15s"),
	"library.mount.zurg_api_interval":             durationBounds(policy.MinZurgAPIInterval, policy.MaxZurgAPIInterval, "1m"),
	"library.mount.zurg_api_repair_grace":         durationBounds(policy.MinZurgRepairGrace, policy.MaxZurgRepairGrace, "2h"),
	"shadow.import_interval":                      durationBounds(policy.MinShadowImportInterval, 0, "15m"),
	"shadow.rss_interval":                         durationBounds(policy.MinShadowRSSInterval, 0, "15m"),
	"shadow.search_reuse":                         durationBounds(time.Minute, policy.MaxShadowSearchReuse, "6h"),
	"shadow.link_window":                          durationBounds(policy.MinShadowLinkWindow, policy.MaxShadowLinkWindow, "24h"),
	"shadow.searches_per_hour":                    bounds(1, policy.MaxShadowSearchesHour, "20"),
	"shadow.keep_results":                         bounds(1, policy.MaxShadowKeepResults, "10"),
	"router.miss_ttl":                             durationBounds(policy.MinRouterMissTTL, policy.MaxRouterMissTTL, "6h"),
	"router.poll_interval":                        durationBounds(policy.MinRouterPollInterval, policy.MaxRouterPollInterval, "1m"),
	"router.cached_grace":                         durationBounds(policy.MinRouterCachedGrace, policy.MaxRouterCachedGrace, "2s"),
	"router.ready_timeout":                        durationBounds(policy.MinRouterReadyTimeout, policy.MaxRouterReadyTimeout, "6h"),
	"router.max_candidates":                       bounds(1, policy.MaxRouterMaxCandidates, "5"),
	"router.wanted.interval":                      durationBounds(policy.MinWantedInterval, policy.MaxWantedInterval, "15m"),
	"router.wanted.again_after":                   durationBounds(policy.MinWantedAgainAfter, policy.MaxWantedAgainAfter, "24h"),
	"router.wanted.per_hour":                      bounds(1, policy.MaxWantedPerHour, "10"),
	"router.cache_evidence.ttl":                   durationBounds(policy.MinCacheEvidenceTTL, policy.MaxCacheEvidenceTTL, "24h"),
	"router.cache_evidence.torbox_check.top":      bounds(1, policy.MaxTorBoxCheckTop, "10"),
	"router.cache_evidence.torbox_check.per_hour": bounds(1, policy.MaxTorBoxCheckHour, "60"),
}

func floatPtr(v float64) *float64 { return &v }
func init() {
	for _, path := range []string{"metadata.series.enabled", "metadata.anime.auto_build", "library.sample.resolution", "library.sample.mislabel", "library.plex.now_playing", "library.plex.prewarm", "shadow.rss", "shadow.search", "playlists.routes", "router.cached_only", "router.wanted.missing", "router.cache_evidence.torbox_check.enabled"} {
		a := sectionAnnotations[path]
		a.Default = "true"
		sectionAnnotations[path] = a
	}

	for _, prefix := range []string{"library.mount.zurg_log", "library.mount.zurg_api"} {
		group := "Zurg log"
		if strings.HasSuffix(prefix, "api") {
			group = "Zurg API"
		}
		for _, suffix := range []string{"", "_budget", "_interval", "_repair_grace"} {
			path := prefix + suffix
			if suffix == "_repair_grace" && strings.HasSuffix(prefix, "log") {
				continue
			}
			a := sectionAnnotations[path]
			a.Group = group
			sectionAnnotations[path] = a
		}
	}
	for _, path := range []string{"metadata.overrides_dir", "router.miss_ttl", "router.cached_grace", "router.poll_interval", "library.mount.stat_timeout"} {
		a := sectionAnnotations[path]
		a.Advanced = true
		sectionAnnotations[path] = a
	}

	for _, v := range []struct {
		path     string
		min, max time.Duration
		def      string
	}{
		{"router.stalled_after", policy.MinRouterStalledAfter, policy.MaxRouterStalledAfter, "10m"},
		{"router.downloading_timeout", policy.MinRouterDownloadingTimeout, policy.MaxRouterDownloadingTimeout, "24h"},
	} {
		a := durationBounds(v.min, v.max, v.def)
		a.ZeroAllowed = true
		sectionAnnotations[v.path] = a
	}
	for path, label := range sectionLabels {
		a := sectionAnnotations[path]
		a.Label = label
		sectionAnnotations[path] = a
	}
}

// Each section is a pointer or map in Policy and may be removed as a whole.
func sectionNullable() map[string]bool {
	out := map[string]bool{}
	typ := reflect.TypeOf(policy.Policy{})
	for _, spec := range settings.Sections {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Split(f.Tag.Get("yaml"), ",")[0] == spec.Key {
				out[spec.Key] = f.Type.Kind() == reflect.Pointer || f.Type.Kind() == reflect.Map
			}
		}
	}
	return out
}

var sectionLabels = map[string]string{
	"cardigann.definitions":                       "Indexer definitions",
	"cardigann.definitions.repo":                  "Git repository",
	"cardigann.definitions.refresh":               "Refresh interval",
	"cardigann.flaresolverr_url":                  "FlareSolverr address",
	"cardigann.prowlarr.url":                      "Prowlarr address",
	"clients":                                     "Download clients",
	"clients.*.kind":                              "Client type",
	"clients.*.url":                               "Client address",
	"clients.*.callback_view":                     "Notification view",
	"clients.*.import_from":                       "Import finished downloads from",
	"clients.*.remote_path":                       "Client path (remote)",
	"clients.*.local_path":                        "Same path in Theatarr (local)",
	"egress.check_url":                            "Exit address check URL",
	"egress.vpn_ips":                              "VPN exit IPs",
	"egress.vpn_cidrs":                            "VPN address ranges",
	"egress.interval":                             "Check interval",
	"egress.max_age":                              "Maximum check age",
	"egress.timeout":                              "Check timeout",
	"import.arr":                                  "Sonarr/Radarr instances",
	"import.arr.*.flavour":                        "Sonarr or Radarr",
	"import.arr.*.url":                            "Server address",
	"library.mount.path":                          "Mount folder",
	"library.mount.link_path":                     "Library link destination",
	"library.mount.all_dir":                       "All torrents folder",
	"library.mount.wait_timeout":                  "Wait for files timeout",
	"library.mount.poll_interval":                 "File check interval",
	"library.mount.stat_timeout":                  "File access timeout",
	"library.mount.zurg_log_interval":             "Zurg log check interval",
	"library.mount.zurg_api":                      "Zurg API address",
	"library.mount.zurg_api_interval":             "Zurg API check interval",
	"library.mount.zurg_api_repair_grace":         "Zurg repair wait",
	"library.mount.rclone_rc":                     "rclone remote control",
	"library.mount.rclone_rc.url":                 "rclone address",
	"library.mount.rclone_rc.forget_release_root": "Clear cached release root listing",
	"library.probe":                               "Media inspection",
	"library.probe.max_bytes":                     "Maximum size",
	"library.sample":                              "Sample detection",
	"library.sample.min_runtime_ratio":            "Minimum runtime fraction",
	"library.sample.resolution":                   "Check video resolution",
	"library.sample.mislabel":                     "Refuse mislabelled remuxes and 2160p",
	"library.trash.dir":                           "Folder",
	"library.trash.keep_days":                     "Days to keep deleted files",
	"library.plex.url":                            "Plex address",
	"library.plex.debounce":                       "Scan delay",
	"library.plex.prewarm":                        "Preload next episode",
	"library.plex.prewarm_rate":                   "Preload speed limit",
	"library.plex.prewarm_max_size":               "Maximum preload size",
	"library.versions.*.plex_section":             "Plex library ID",
	"library.relink_interval":                     "Library link check interval",
	"library.dangling":                            "Broken links",
	"library.dangling.interval":                   "Check interval",
	"library.dangling.roots":                      "Folders to check",
	"metadata.overrides_dir":                      "Metadata overrides folder",
	"metadata.sources.*.url":                      "Source address",
	"metadata.art":                                "Artwork",
	"metadata.art.per_run":                        "Titles per refresh",
	"metadata.art.interval":                       "Refresh interval",
	"metadata.art.prewarm":                        "Artwork sizes to preload",
	"metadata.series.interval":                    "Refresh interval",
	"metadata.series.per_run":                     "Series per refresh",
	"metadata.lists.interval":                     "Refresh interval",
	"metadata.anime":                              "Anime franchises",
	"metadata.anime.auto_build":                   "Build franchises automatically",
	"metadata.anime.refresh_interval":             "Rebuild interval",
	"notify.allow_hosts":                          "Allowed destination hosts",
	"notify.poll":                                 "Delivery check interval",
	"notify.targets":                              "Notification destinations",
	"notify.targets.*.url":                        "Destination URL",
	"notify.targets.*.views":                      "Sonarr/Radarr views",
	"notify.targets.*.native":                     "Send Theatarr events",
	"notify.targets.*.format":                     "Message format",
	"notify.targets.*.warnings":                   "Also send warnings",
	"notify.digest_at":                            "Daily digest time",
	"notify.targets.*.auth_header":                "Authorization header",
	"playlists.orders":                            "Watch orders",
	"playlists.routes":                            "Create alternate route playlists",
	"playlists.name":                              "Playlist name template",
	"playlists.interval":                          "Sync interval",
	"provider_sync.interval":                      "Sync interval",
	"router.alldebrid_delete_uncached":            "Delete uncached AllDebrid adds",
	"router.cached_only":                          "Only add cached releases",
	"router.cached_grace":                         "Cached download startup wait",
	"router.stalled_after":                        "Stalled download timeout",
	"router.downloading_timeout":                  "No progress timeout",
	"router.ready_timeout":                        "Ready files timeout",
	"router.miss_ttl":                             "Remember uncached results for",
	"router.poll_interval":                        "Download check interval",
	"router.max_candidates":                       "Maximum candidate releases",
	"router.wanted":                               "Automatic wanted searches",
	"router.wanted.interval":                      "Search interval",
	"router.wanted.per_hour":                      "Searches per hour",
	"router.wanted.again_after":                   "Wait before searching again",
	"router.wanted.missing":                       "Search for missing files",
	"router.wanted.cutoff_unmet":                  "Upgrade search target",
	"router.wanted.retry_new":                     "Retry new episodes sooner",
	"router.cache_evidence":                       "Provider cache results",
	"router.cache_evidence.ttl":                   "Remember cached results for",
	"router.cache_evidence.torbox_check":          "Check TorBox cache",
	"router.cache_evidence.torbox_check.top":      "Maximum releases checked per search",
	"router.cache_evidence.torbox_check.per_hour": "Cache checks per hour",
	"shadow.import_interval":                      "Sonarr/Radarr import interval",
	"shadow.rss_interval":                         "RSS feed check interval",
	"shadow.rss":                                  "Check RSS feeds",
	"shadow.search":                               "Search after Sonarr/Radarr grabs",
	"shadow.search_reuse":                         "Reuse search results for",
	"shadow.keep_results":                         "Results to keep per grab",
	"shadow.link_window":                          "Wait for matching Sonarr/Radarr grabs",
	"torrents.scan_roots":                         "Folders to scan",
	"torrents.backup.every":                       "Backup interval",
	"torrents.backup.keep":                        "Backups to keep",
	"ui.images.dir":                               "Folder",
	"ui.images.max_bytes":                         "Maximum size",
	"ui.images.original":                          "Allow original-size images",
}

// Wanted's default depends on whether any version is active, unlike the other switches.
func settingsSchemaForPolicy(p *policy.Policy) SettingsSchema {
	schema := settingsSchema()
	for i := range schema.Sections["router"] {
		f := &schema.Sections["router"][i]
		if f.Name == "wanted" {
			for j := range f.Fields {
				if f.Fields[j].Name == "enabled" {
					f.Fields[j].Default = fmt.Sprint(p.WantedSettings().Enabled)
				}
			}
		}
	}
	return schema
}
