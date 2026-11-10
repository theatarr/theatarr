package settings

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// sectionEffects names each change of a database-owned section that moves traffic to another host, changes where
// files are read or written, or changes the VPN guard. Tuning values (intervals, limits, events) apply without
// confirmation. Credential fields are left to sectionCredentialEffects. The router asks when cached_only turns off or
// uncached AllDebrid deletes turn on.
func sectionEffects(previous, next *policy.Policy) []ConfirmationEffect {
	effects := importAcquisitionEffects(previous, next)
	for _, spec := range Sections {
		a, errA := sectionJSON(previous, spec.Key)
		b, errB := sectionJSON(next, spec.Key)
		if errA != nil || errB != nil || sameJSON(a, b) {
			continue
		}
		before, after := flattenSection(a), flattenSection(b)
		effectiveLeaves(spec.Key, previous, before)
		effectiveLeaves(spec.Key, next, after)
		paths := map[string]bool{}
		for path := range before {
			paths[path] = true
		}
		for path := range after {
			paths[path] = true
		}
		for _, path := range sortedPolicyKeys(paths) {
			old, now := before[path], after[path]
			if old == now {
				continue
			}
			leaf := path[strings.LastIndex(path, ".")+1:]
			if strings.HasSuffix(leaf, "_env") {
				continue
			}
			what := sectionChangeKind(spec.Key, path, leaf, old, now)
			// A metadata source pointed at its own public address (what Setup's TMDb step stores) is no other host.
			if spec.Key == "metadata" && now != "" && metadataDefaultURLs[path] == strings.TrimSuffix(now, "/") {
				continue
			}
			// Removing a host or path narrows access, except scan roots: removing those widens deletion eligibility.
			if what == "" || now == "" && spec.Key != "egress" && !(spec.Key == "torrents" && path == "scan_roots") {
				continue
			}
			whole := spec.Key == "notify" && leaf == "url"
			effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("%s: %s.%s %s → %s.", what, spec.Key, path,
				effectValue(old, whole), effectValue(now, whole))})
		}
	}
	return effects
}

// metadataDefaultURLs are the public addresses of the known metadata sources, by leaf path: saving a source with its
// own address needs no confirmation; any other address still does.
var metadataDefaultURLs = map[string]string{
	"sources.tmdb.url":        "https://api.themoviedb.org/3",
	"sources.tmdb-images.url": "https://image.tmdb.org/t/p",
}

// sectionCredentialEffects names each *_env field of a database-owned section that is pointed at another credential
// name: the section then sends that credential where it sent its own. Storing or clearing the section's own value
// needs nothing, and neither does falling back to the name the stored section already gave. Names appear, values never.
func sectionCredentialEffects(previous, next *Snapshot) []ConfirmationEffect {
	stored := func(s *Snapshot, key string) map[string]string {
		for _, r := range s.Records.Sections {
			if r.Key == key {
				return flattenSection(r.Config)
			}
		}
		return map[string]string{}
	}
	effective := func(p *policy.Policy, key string) map[string]string {
		raw, err := sectionJSON(p, key)
		if err != nil {
			return map[string]string{}
		}
		return flattenSection(raw)
	}
	var effects []ConfirmationEffect
	for _, r := range next.Records.Sections {
		prevStored, nextStored := stored(previous, r.Key), flattenSection(r.Config)
		prevEff, nextEff := effective(previous.Policy, r.Key), effective(next.Policy, r.Key)
		paths := map[string]bool{}
		for path := range nextStored {
			paths[path] = true
		}
		for path := range nextEff {
			paths[path] = true
		}
		for _, path := range sortedPolicyKeys(paths) {
			if !strings.HasSuffix(path, "_env") {
				continue
			}
			own := settingsSecretEnv("sections/"+r.Key, strings.TrimSuffix(path, "_env"))
			old, now := "", ""
			switch {
			case nextStored[path] != prevStored[path] && nextStored[path] != "" && nextStored[path] != own:
				old, now = prevStored[path], nextStored[path]
			case nextEff[path] != prevEff[path] && nextEff[path] != "" && nextEff[path] != own && nextEff[path] != prevStored[path]:
				old, now = prevEff[path], nextEff[path]
			default:
				continue
			}
			effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("Use another credential: %s.%s %s → %s.",
				r.Key, path, effectValue(old, false), now)})
		}
	}
	return effects
}

// effectiveLeaves replaces leaves whose omission has a meaning with the value in force, so that an omitted value and
// an explicit one compare as what they do: require_vpn_for becomes the classes the guard covers and cached_only the
// router's effective setting; alldebrid_delete_uncached defaults to false.
func effectiveLeaves(section string, p *policy.Policy, leaves map[string]string) {
	switch {
	case section == "egress" && p.Egress != nil:
		var classes []string
		for _, class := range []string{policy.EgressIndexers, policy.EgressProviders, policy.EgressMetadata} {
			if p.Egress.RequiresVPN(class) {
				classes = append(classes, class)
			}
		}
		leaves["require_vpn_for"] = strings.Join(classes, ", ")
	case section == "router":
		leaves["cached_only"] = fmt.Sprint(p.RouterSettings().CachedOnly)
		leaves["alldebrid_delete_uncached"] = fmt.Sprint(p.RouterSettings().AllDebridDeleteUncached)
	}
}

func sectionChangeKind(section, path, leaf, old, now string) string {
	switch {
	case section == "router" && path == "cached_only":
		if old == "true" && now == "false" {
			return "Let the router add releases not known to be cached"
		}
		return ""
	case section == "router" && path == "alldebrid_delete_uncached":
		if old == "false" && now == "true" {
			return "Delete our own uncached AllDebrid adds"
		}
		return ""
	case section == "torrents" && path == "scan_roots":
		return "Change torrent usage scan roots (removed roots can make used torrents appear unused)"
	case section == "egress":
		if leaf == "require_vpn_for" || leaf == "vpn_ips" || leaf == "vpn_cidrs" || path == "" {
			return "Change the VPN guard (what it requires or accepts)"
		}
		return "Change the VPN guard"
	case section == "cardigann" && strings.HasPrefix(path, "definitions"):
		return "Change which indexer definitions the catalog runs"
	case leaf == "url" || strings.HasSuffix(leaf, "_url") || leaf == "zurg_api" || leaf == "allow_hosts" ||
		(leaf == "zurg_log" && (policy.ZurgLogIsURL(old) || policy.ZurgLogIsURL(now))):
		return "Send requests to another host"
	case leaf == "path" || leaf == "link_path" || leaf == "dir" || leaf == "all_dir" || leaf == "roots" ||
		leaf == "zurg_log" || strings.HasSuffix(leaf, "_dir"):
		return "Change where files are read or written"
	}
	return ""
}

func effectValue(v string, whole bool) string {
	if v == "" {
		return "(none)"
	}
	return redactURL(v, whole)
}

// flattenSection maps each leaf of a section's YAML-shaped JSON to its value as text; lists join their items.
func flattenSection(raw json.RawMessage) map[string]string {
	var value any
	_ = json.Unmarshal(raw, &value)
	out := map[string]string{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				walk(joinPath(path, key), child)
			}
		case []any:
			items := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					items = append(items, s)
				} else {
					b, _ := json.Marshal(item)
					items = append(items, string(b))
				}
			}
			sort.Strings(items)
			out[path] = strings.Join(items, ", ")
		case nil:
			out[path] = ""
		default:
			out[path] = fmt.Sprint(v)
		}
	}
	walk("", value)
	return out
}

// viewIdentityEffects names a new view, a view that moves or answers as another arr, one that answers the ids of
// other arr instances, and a view whose API key changes. Key values never appear.
func viewIdentityEffects(previous, next *Snapshot) []ConfirmationEffect {
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Policy.Views) {
		now, old := next.Policy.Views[key], previous.Policy.Views[key]
		if old == nil {
			effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("Create view %s at %s (%s): arr clients with its API key can use it.",
				key, now.Path, now.Flavour)})
			continue
		}
		if old.Path != now.Path || old.Flavour != now.Flavour {
			effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("View %s answers at %s as %s (was %s as %s).",
				key, now.Path, now.Flavour, old.Path, old.Flavour)})
		}
		if !slices.Equal(old.AliasFrom, now.AliasFrom) {
			effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("View %s answers the ids of arr instances %s (was %s).",
				key, switchList(now.AliasFrom), switchList(old.AliasFrom))})
		}
		if previous.secrets != nil && old.APIKeyEnv == now.APIKeyEnv && strings.HasPrefix(now.APIKeyEnv, policy.SettingsSecretPrefix) &&
			!next.SameSecret(previous, "views", key) {
			effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("View %s answers a different API key: clients using the old key lose access.", key)})
		}
	}
	return effects
}

// Importing titles and monitoring into a grabbing version (active with grab on) puts them in wanted searches. An
// unchanged mapping does not. It counts whatever router.enabled is:
// turning the router on later names no import source. Check the effective policies so startup fallback uses the same
// rule.
func importAcquisitionEffects(previous, next *policy.Policy) []ConfirmationEffect {
	if next.Import == nil {
		return nil
	}
	routerEnabled := next.Router != nil && next.Router.Enabled
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Import.Arr) {
		now := next.Import.Arr[key]
		if now == nil || !slices.Contains(next.GrabbingVersions(), now.Version) {
			continue
		}
		var old *policy.ArrSource
		if previous.Import != nil {
			old = previous.Import.Arr[key]
		}
		if old != nil && old.Version == now.Version {
			continue
		}
		effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("Import titles into library %s (Allow downloads for this library is on) from import.arr.%s: monitored missing titles can be searched, and downloaded when Allow new downloads (router.enabled) is on (currently %t).", now.Version, key, routerEnabled)})
	}
	return effects
}
