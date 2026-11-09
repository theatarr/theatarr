package policy

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/secrets"
)

var (
	keyRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	envRE     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	trashIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

var (
	indexerKinds  = setOf("torznab", "dmm", "torrentio", "zilean", "seadex", "cardigann")
	providerKinds = setOf("realdebrid", "alldebrid", "torbox")
	clientKinds   = map[string]Storage{"decypharr": StorageDebrid, "qbittorrent": StorageSeedbox}
	flavours      = map[string]Kind{"radarr": KindMovie, "sonarr": KindSeries}
)

// Validate checks the whole policy and returns an *Errors listing every problem, or nil.
func (p *Policy) Validate() error {
	p.ApplyDefaults()
	e := &Errors{}
	for _, k := range sortedKeys(p.Budgets) {
		p.validateBudget(e, k, p.Budgets[k])
	}
	for _, k := range sortedKeys(p.Formats) {
		p.validateFormat(e, k, p.Formats[k])
	}
	p.validateFormatSets(e)
	for _, k := range sortedKeys(p.Profiles) {
		p.validateProfile(e, k, p.Profiles[k])
	}
	for _, k := range sortedKeys(p.Indexers) {
		p.validateIndexer(e, k, p.Indexers[k])
	}
	p.validateArrNames(e)
	p.validateProviders(e)
	for _, k := range sortedKeys(p.Clients) {
		p.validateClient(e, k, p.Clients[k])
	}
	for _, k := range sortedKeys(p.Versions) {
		p.validateVersion(e, k, p.Versions[k])
	}
	p.validateViews(e)
	p.validateMetadata(e)
	p.validateImport(e)
	p.validateEgress(e)
	p.validateCardigann(e)
	p.validateProviderSync(e)
	p.validateTorrents(e)
	p.validateShadow(e)
	p.validateLibrary(e)
	p.validateFollow(e)
	p.validateRouter(e)
	p.validatePlaylists(e)
	p.validateNotify(e)
	p.validateArt(e)
	p.validateSeriesMeta(e)
	p.validateLists(e)
	p.validateAnimeBuild(e)
	p.validatePlexTV(e)
	return e.err()
}

func checkKey(e *Errors, path, key string) {
	if !keyRE.MatchString(key) {
		e.add(path, "key must be lower case letters, digits, '-' or '_'")
	}
}

func checkEnv(e *Errors, path, v string, required bool) {
	if v == "" {
		if required {
			e.add(path, "required: the name of the environment variable that holds the secret")
		}
		return
	}
	if !envRE.MatchString(v) {
		e.add(path, "%q is not an environment variable name (A-Z, 0-9, _); secrets are never inline", v)
	}
}

func checkURL(e *Errors, path, v string) {
	if v == "" {
		e.add(path, "required: every outbound URL is configured, there are no defaults")
		return
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		e.add(path, "%q is not an http(s) URL", v)
		return
	}
	if u.User != nil {
		e.add(path, "credentials in the URL are not allowed; use the _env fields")
	}
	for _, name := range slices.Sorted(maps.Keys(u.Query())) {
		// Without regard to case or separators: X-Plex-Token, apiKey and APIKEY are credentials too.
		if secrets.CredentialQueryName(name) {
			e.add(path, "secrets in the URL are not allowed (query parameter %q); use the _env fields", name)
			break
		}
	}
}

func checkRef[T any](e *Errors, path, what, ref string, m map[string]*T, required bool) bool {
	if ref == "" {
		if required {
			e.add(path, "required")
		}
		return false
	}
	if _, ok := m[ref]; !ok {
		e.add(path, "unknown %s %q", what, ref)
		return false
	}
	return true
}

func (p *Policy) validateBudget(e *Errors, k string, b *Budget) {
	path := fmt.Sprintf("budgets[%s]", k)
	checkKey(e, path, k)
	if b.Requests <= 0 {
		e.add(path+".requests", "must be greater than 0")
	}
	if b.Per <= 0 {
		e.add(path+".per", "required: a duration such as 1m or 1h")
	}
	if b.Burst < 0 {
		e.add(path+".burst", "must not be negative")
	}
	if b.Hourly < 0 || b.Daily < 0 {
		e.add(path, "hourly and daily caps must not be negative")
	}
	if b.Hourly > 0 && b.Daily > 0 && b.Daily < b.Hourly {
		e.add(path+".daily", "%d is below the hourly cap %d", b.Daily, b.Hourly)
	}
}

// ValidateFormats checks only the format definitions (formats and format_sets): each one's source (trash_id with
// flavour, file or specification) and flavour, and the sets' references and flavours. An import checks this before it
// materializes the formats, whose source fields it then clears.
func (p *Policy) ValidateFormats() error {
	e := &Errors{}
	for _, k := range sortedKeys(p.Formats) {
		p.validateFormat(e, k, p.Formats[k])
	}
	p.validateFormatSets(e)
	return e.err()
}

func (p *Policy) validateFormat(e *Errors, k string, f *FormatRef) {
	path := fmt.Sprintf("formats[%s]", k)
	if f.Ref != "" {
		e.add(path+".ref", "only valid inside format_sets")
	}
	switch {
	case f.TrashID != "" && f.File != "":
		e.add(path, "set either trash_id or file, not both")
	case f.TrashID != "":
		if !trashIDRE.MatchString(f.TrashID) {
			e.add(path+".trash_id", "%q is not a TRaSH id (32 lower-case hex digits)", f.TrashID)
		}
		if _, ok := flavours[f.Flavour]; !ok {
			e.add(path+".flavour", "must be radarr or sonarr (TRaSH ids differ between them), got %q", f.Flavour)
		}
	case f.File != "":
		if filepath.IsAbs(f.File) || strings.HasPrefix(filepath.Clean(f.File), "..") {
			e.add(path+".file", "must be a path inside the policy directory")
		} else if _, err := os.Stat(filepath.Join(p.Dir, f.File)); err != nil {
			e.add(path+".file", "%v", err)
		}
		if f.Flavour != "" {
			if _, ok := flavours[f.Flavour]; !ok {
				e.add(path+".flavour", "must be radarr or sonarr, got %q", f.Flavour)
			}
		}
	case len(f.Specification) != 0:
		if !json.Valid(f.Specification) {
			e.add(path+".specification", "must contain a JSON custom format")
		}
		if f.Flavour != "" {
			if _, ok := flavours[f.Flavour]; !ok {
				e.add(path+".flavour", "must be radarr or sonarr, got %q", f.Flavour)
			}
		}
	default:
		e.add(path, "set trash_id (with flavour) or file")
	}
}

func (p *Policy) validateProfile(e *Errors, k string, pr *Profile) {
	path := fmt.Sprintf("profiles[%s]", k)
	checkKey(e, path, k)
	p.validatePropers(e, path, pr)
	if pr.FromArr != "" {
		p.validateFromArr(e, path, pr)
		return
	}
	if pr.FormatSet != "" {
		checkRef(e, path+".format_set", "format set", pr.FormatSet, p.FormatSets, true)
	}
	validateProfileLanguage(e, path, pr)
	if len(pr.Qualities) == 0 {
		e.add(path+".qualities", "required: at least one quality")
	}
	names, _ := validateQualityItems(e, path+".qualities", pr.Qualities)
	if len(pr.QualityOrder) > 0 {
		validateQualityItems(e, path+".quality_order", pr.QualityOrder)
		var allowed []QualityItem
		for _, q := range pr.QualityOrder {
			if names[q.Name] {
				allowed = append(allowed, q)
			}
		}
		if !slices.EqualFunc(allowed, pr.Qualities, func(a, b QualityItem) bool {
			return a.Name == b.Name && a.Group == b.Group && slices.Equal(a.Qualities, b.Qualities)
		}) {
			e.add(path+".quality_order", "allowed items must match qualities in order and group membership")
		}
	}
	if pr.Cutoff == "" {
		e.add(path+".cutoff", "required")
	} else if !names[pr.Cutoff] {
		e.add(path+".cutoff", "%q is not one of the profile's qualities", pr.Cutoff)
	}
	if pr.CutoffScore < pr.MinScore {
		e.add(path+".cutoff_score", "%d is below min_score %d", pr.CutoffScore, pr.MinScore)
	}
	if pr.MinUpgradeStep != nil && *pr.MinUpgradeStep < 0 {
		e.add(path+".min_upgrade_step", "must not be negative")
	}
	for _, q := range sortedKeys(pr.Sizes) {
		s := pr.Sizes[q]
		sp := fmt.Sprintf("%s.sizes[%s]", path, q)
		// Disabled qualities still run size checks before being rejected by the profile.
		if !radarrQualities[q] && !sonarrQualities[q] {
			e.add(sp, "unknown quality %q", q)
		}
		if s.Min < 0 || s.Preferred < 0 || s.Max < 0 {
			e.add(sp, "sizes must not be negative")
		}
		if s.Max > 0 && (s.Min > s.Max || s.Preferred > s.Max) {
			e.add(sp, "min and preferred must not exceed max")
		}
		if s.Preferred > 0 && s.Preferred < s.Min {
			e.add(sp, "preferred is below min")
		}
		if s.PreferredZero && s.Preferred != 0 {
			e.add(sp, "preferred_zero requires preferred: 0")
		}
	}
	seen := map[string]string{}
	for _, ref := range sortedKeys(pr.Formats) {
		fp := fmt.Sprintf("%s.formats[%s]", path, ref)
		key, f := p.ProfileFormat(pr, ref)
		if f == nil {
			e.add(fp, "unknown format %q; declare it under formats", ref)
			continue
		}
		if prev, dup := seen[key]; dup {
			e.add(fp, "format %q is already scored as %q", key, prev)
		}
		seen[key] = ref
	}
}

func validateQualityItems(e *Errors, path string, items []QualityItem) (map[string]bool, map[string]bool) {
	names := map[string]bool{}
	members := map[string]bool{}
	for i, q := range items {
		qp := fmt.Sprintf("%s[%d]", path, i)
		if q.Name == "" {
			e.add(qp, "empty quality name")
			continue
		}
		if names[q.Name] {
			e.add(qp, "%q listed twice", q.Name)
		}
		names[q.Name] = true
		list := []string{q.Name}
		if q.Group {
			if len(q.Qualities) == 0 {
				e.add(qp, "group %q has no qualities", q.Name)
			}
			list = q.Qualities
		}
		for _, m := range list {
			if !radarrQualities[m] && !sonarrQualities[m] {
				e.add(qp, "unknown quality %q", m)
			}
			if members[m] {
				e.add(qp, "quality %q is in the profile twice", m)
			}
			members[m] = true
		}
	}
	return names, members
}

// checkProfileFits checks that a profile's qualities and formats suit a version of the given kind.
func (p *Policy) checkProfileFits(e *Errors, path string, pr *Profile, kind Kind) {
	if pr.FromArr != "" {
		p.checkFromArrFits(e, path, pr, kind)
		return
	}
	if pr.Language != "" && kind == KindSeries {
		e.add(path+".language", "a series version uses this profile, and Sonarr v4 profiles have no language; remove it")
	}
	valid := qualitiesFor(kind)
	for _, q := range sortedKeys(pr.Sizes) {
		if (radarrQualities[q] || sonarrQualities[q]) && !valid[q] {
			e.add(path+".sizes", "profile size quality %q is not a %s quality", q, kind)
		}
	}
	all := append(append([]QualityItem(nil), pr.Qualities...), pr.QualityOrder...)
	for _, q := range all {
		list := []string{q.Name}
		if q.Group {
			list = q.Qualities
		}
		for _, m := range list {
			if (radarrQualities[m] || sonarrQualities[m]) && !valid[m] {
				e.add(path, "profile quality %q is not a %s quality", m, kind)
			}
		}
	}
	if len(pr.QualityOrder) > 0 {
		_, members := validateQualityItems(&Errors{}, path, pr.QualityOrder)
		for _, q := range sortedKeys(valid) {
			if !members[q] {
				e.add(path+".quality_order", "missing quality %q", q)
			}
		}
	}
	flavour := "radarr"
	if kind == KindSeries {
		flavour = "sonarr"
	}
	if fs := p.FormatSets[pr.FormatSet]; fs != nil && fs.Flavour != flavour {
		e.add(path+".format_set", "a %s set, but the version is a %s", fs.Flavour, kind)
	}
	for _, ref := range sortedKeys(pr.Formats) {
		if key, f := p.ProfileFormat(pr, ref); f != nil && f.Flavour != "" && f.Flavour != flavour {
			e.add(path, "profile format %q is a %s format, but the version is a %s", key, f.Flavour, kind)
		}
	}
}

func (p *Policy) validateIndexer(e *Errors, k string, ix *Indexer) {
	path := fmt.Sprintf("indexers[%s]", k)
	checkKey(e, path, k)
	if !indexerKinds[ix.Kind] {
		e.add(path+".kind", "must be one of %s, got %q", list(indexerKinds), ix.Kind)
	}
	checkURL(e, path+".url", ix.URL)
	checkEnv(e, path+".api_key_env", ix.APIKeyEnv, false)
	switch ix.Privacy {
	case PrivacyPublic, PrivacyPrivate, PrivacySemiPrivate, PrivacyUnknown, "unknown":
	default:
		e.add(path+".privacy", "must be public, private or semi-private, got %q", ix.Privacy)
	}
	for i, pk := range ix.Providers {
		pp := fmt.Sprintf("%s.providers[%d]", path, i)
		if checkRef(e, pp, "provider", pk, p.Providers, true) && slices.Index(ix.Providers, pk) != i {
			e.add(pp, "provider %q listed twice", pk)
		}
	}
	checkRef(e, path+".budget", "budget", ix.Budget, p.Budgets, false)
	if ix.PageCap < 0 {
		e.add(path+".page_cap", "must not be negative")
	}
	if ix.MinSeeders != nil && *ix.MinSeeders < 0 {
		e.add(path+".min_seeders", "must not be negative")
	}
	validateMultiLanguages(e, path+".multi_languages", ix.MultiLanguages)
	p.validateCardigannIndexer(e, path, ix)
	if ix.SeaDex != nil {
		if ix.Kind != "seadex" {
			e.add(path+".seadex", "only valid for a seadex indexer (kind is %s)", ix.Kind)
		}
		for i, t := range ix.SeaDex.ExcludeTags {
			if strings.TrimSpace(t) == "" {
				e.add(fmt.Sprintf("%s.seadex.exclude_tags[%d]", path, i), "empty tag")
			}
		}
		for t := range ix.SeaDex.Tags {
			if strings.TrimSpace(t) == "" {
				e.add(path+".seadex.tags", "empty tag")
			}
		}
	}
}

func (p *Policy) validateProviders(e *Errors) {
	orders := map[int]string{}
	for _, k := range sortedKeys(p.Providers) {
		pv := p.Providers[k]
		path := fmt.Sprintf("providers[%s]", k)
		checkKey(e, path, k)
		if !providerKinds[pv.Kind] {
			e.add(path+".kind", "must be one of %s, got %q", list(providerKinds), pv.Kind)
		}
		if pv.Order <= 0 {
			e.add(path+".order", "must be 1 or more")
		} else if other, dup := orders[pv.Order]; dup {
			e.add(path+".order", "order %d is also used by %s", pv.Order, other)
		} else {
			orders[pv.Order] = k
		}
		checkURL(e, path+".url", pv.URL)
		checkEnv(e, path+".api_key_env", pv.APIKeyEnv, true)
		checkRef(e, path+".budget", "budget", pv.Budget, p.Budgets, false)
		if pv.Pacing.AddsPerMinute < 0 || pv.Pacing.UncachedPerHr < 0 || pv.Pacing.Cooldown < 0 || pv.Pacing.BackoffMax < 0 {
			e.add(path+".pacing", "values must not be negative")
		}
		if g := pv.Pacing.MinGap; g != nil && (g.D() < 0 || g.D() > MaxMinGap) {
			e.add(path+".pacing.min_gap", "%v is outside 0s to %v", g.D(), MaxMinGap)
		}
		if d := pv.StalledAfter; d != nil && d.D() != 0 && (d.D() < MinRouterStalledAfter || d.D() > MaxRouterStalledAfter) {
			e.add(path+".stalled_after", "%v is outside %v to %v (0s turns it off)", d.D(), MinRouterStalledAfter,
				MaxRouterStalledAfter)
		}
		if n := pv.MagnetLimit; n != nil {
			switch {
			case pv.Kind != "alldebrid":
				e.add(path+".magnet_limit", "is for alldebrid providers only")
			case *n < 1 || *n > MaxMagnetLimit:
				e.add(path+".magnet_limit", "%d is outside 1 to %d", *n, MaxMagnetLimit)
			}
		}
	}
}

func (p *Policy) validateClient(e *Errors, k string, c *Client) {
	path := fmt.Sprintf("clients[%s]", k)
	checkKey(e, path, k)
	if _, ok := clientKinds[c.Kind]; !ok {
		e.add(path+".kind", "must be decypharr or qbittorrent, got %q", c.Kind)
	}
	checkURL(e, path+".url", c.URL)
	checkEnv(e, path+".username_env", c.UsernameEnv, false)
	checkEnv(e, path+".password_env", c.PasswordEnv, false)
	switch c.Kind {
	case "decypharr":
		if c.CallbackView == "" {
			e.add(path+".callback_view", "required for decypharr: it logs in with a view's URL and key and calls back")
		} else {
			checkRef(e, path+".callback_view", "view", c.CallbackView, p.Views, true)
		}
	default:
		if c.CallbackView != "" {
			e.add(path+".callback_view", "only for decypharr")
		}
	}
	switch c.ImportFrom {
	case "", ImportFromMount, ImportFromDownloadFolder:
	default:
		e.add(path+".import_from", "must be %s or %s, got %q", ImportFromMount, ImportFromDownloadFolder, c.ImportFrom)
	}
	if (c.RemotePath == "") != (c.LocalPath == "") {
		e.add(path+".remote_path", "remote_path and local_path go together: set both or neither")
	}
	for _, f := range [][2]string{{"remote_path", c.RemotePath}, {"local_path", c.LocalPath}} {
		if f[1] != "" && !filepath.IsAbs(f[1]) {
			e.add(path+"."+f[0], "%q must be an absolute path", f[1])
		}
	}
}

// checkCallbackView checks that the view a client-routed version's decypharr calls back serves the version with
// its flavour (radarr for movies, sonarr for series): decypharr's callbacks must reach a view that knows the download.
func (p *Policy) checkCallbackView(e *Errors, path, k string, v *VersionPolicy, c *Client) {
	view := p.Views[c.CallbackView]
	if view == nil {
		return // reported on the client
	}
	if !slices.Contains(view.Versions, k) {
		e.add(path, "client %q calls back view %q, which does not serve this version", v.Client, c.CallbackView)
	} else if kind, ok := flavours[view.Flavour]; ok && kind != v.Kind {
		e.add(path, "client %q calls back view %q (%s), which does not serve %s versions", v.Client, c.CallbackView,
			view.Flavour, v.Kind)
	}
}

// ReachesDebrid reports whether a version can hand releases to debrid storage or providers.
func (p *Policy) ReachesDebrid(v *VersionPolicy) bool {
	if v.Storage == StorageDebrid || len(v.Providers) > 0 {
		return true
	}
	if c, ok := p.Clients[v.Client]; ok && clientKinds[c.Kind] == StorageDebrid {
		return true
	}
	return false
}

func (p *Policy) validateVersion(e *Errors, k string, v *VersionPolicy) {
	path := fmt.Sprintf("versions[%s]", k)
	checkKey(e, path, k)
	switch v.Mode {
	case ModeShadow, ModeActive:
	default:
		e.add(path+".mode", "must be shadow or active, got %q", v.Mode)
	}
	for i, step := range v.GoliveDone {
		gp := fmt.Sprintf("%s.golive_done[%d]", path, i)
		if step != "homepage" {
			e.add(gp, "must be homepage, got %q", step)
		} else if slices.Index(v.GoliveDone, step) != i {
			e.add(gp, "%q listed twice", step)
		}
	}
	kindOK := v.Kind == KindMovie || v.Kind == KindSeries
	if !kindOK {
		e.add(path+".kind", "must be movie or series, got %q", v.Kind)
	}
	switch v.Enable {
	case "default", "request":
	default:
		e.add(path+".enable", "must be default or request, got %q", v.Enable)
	}
	if checkRef(e, path+".profile", "profile", v.Profile, p.Profiles, true) && kindOK {
		p.checkProfileFits(e, path+".profile", p.Profiles[v.Profile], v.Kind)
	}
	if v.Library == "" {
		e.add(path+".library", "required")
	} else if !filepath.IsAbs(v.Library) {
		e.add(path+".library", "%q must be an absolute path", v.Library)
	}
	if kindOK {
		p.validateNaming(e, k, v)
	}
	validatePacks(e, path, v)
	validateSonarr(e, path, v)
	validateUpgrades(e, path, v)
	validateProviderPreference(e, path, v)
	if strings.ContainsAny(v.Edition, "{}/\\") {
		e.add(path+".edition", "must not contain braces or slashes")
	}

	switch v.Storage {
	case StorageDebrid, StorageSeedbox:
	default:
		e.add(path+".storage", "must be debrid or seedbox, got %q", v.Storage)
	}
	// A debrid version needs no download client when the router adds to the providers itself. A seedbox version
	// does, and so does one that sends its grabs to a client (add_via: client).
	viaClient := false
	switch v.AddVia {
	case AddViaProviders:
	case AddViaClient:
		viaClient = true
		if v.Storage != StorageDebrid {
			e.add(path+".add_via", "a client adds to debrid: storage must be debrid, got %q", v.Storage)
		}
	default:
		e.add(path+".add_via", "must be %s or %s, got %q", AddViaProviders, AddViaClient, v.AddVia)
	}
	if checkRef(e, path+".client", "client", v.Client, p.Clients, v.Storage != StorageDebrid || viaClient) &&
		v.Storage != "" {
		cl := p.Clients[v.Client]
		if want := clientKinds[cl.Kind]; want != "" && want != v.Storage {
			e.add(path+".client", "client %q (%s) does not serve %s storage", v.Client, cl.Kind, v.Storage)
		} else if viaClient && want == StorageDebrid {
			p.checkCallbackView(e, path+".client", k, v, cl)
			if cl.Category == "" {
				e.add(path+".client", "client %q has no category: decypharr needs one for the adds of an "+
					"add_via: client version", v.Client)
			}
		}
	}
	switch v.Import {
	case "symlink", "hardlink", "copy":
	default:
		e.add(path+".import", "must be symlink, hardlink or copy, got %q", v.Import)
	}
	if v.Storage == StorageDebrid && v.Import != "symlink" {
		e.add(path+".import", "debrid versions symlink into the mount, got %q", v.Import)
	}

	// Providers: known, no repeats, debrid only, listed in fallback order.
	lastOrder := 0
	for i, pk := range v.Providers {
		pp := fmt.Sprintf("%s.providers[%d]", path, i)
		if !checkRef(e, pp, "provider", pk, p.Providers, true) {
			continue
		}
		if slices.Index(v.Providers, pk) != i {
			e.add(pp, "provider %q listed twice", pk)
			continue
		}
		o := p.Providers[pk].Order
		if o < lastOrder {
			e.add(pp, "%q listed out of fallback order (providers[%s].order is %d)", pk, pk, o)
		}
		lastOrder = o
	}
	if v.Storage == StorageDebrid && len(v.Providers) == 0 && !viaClient {
		e.add(path+".providers", "required for debrid storage (or add_via: client)")
	}
	if v.Storage == StorageSeedbox && len(v.Providers) > 0 {
		e.add(path+".providers", "only for debrid storage")
	}

	// Indexers.
	if len(v.Indexers) == 0 {
		e.add(path+".indexers", "required: list the indexers this version may search")
	}
	for i, ik := range v.Indexers {
		ip := fmt.Sprintf("%s.indexers[%d]", path, i)
		if !checkRef(e, ip, "indexer", ik, p.Indexers, true) {
			continue
		}
		if slices.Index(v.Indexers, ik) != i {
			e.add(ip, "indexer %q listed twice", ik)
		}
	}
}

func (p *Policy) validateViews(e *Errors) {
	paths := map[string]string{}
	for _, k := range sortedKeys(p.Views) {
		v := p.Views[k]
		path := fmt.Sprintf("views[%s]", k)
		checkKey(e, path, k)
		kind, ok := flavours[v.Flavour]
		if !ok {
			e.add(path+".flavour", "must be radarr or sonarr, got %q", v.Flavour)
		}
		switch {
		case v.Path == "":
			e.add(path+".path", "required, e.g. /radarr")
		case !strings.HasPrefix(v.Path, "/") || strings.HasSuffix(v.Path, "/") || strings.ContainsAny(v.Path, "?#"):
			e.add(path+".path", "%q must start with / and not end with one", v.Path)
		case strings.HasPrefix(v.Path, "/v/") || v.Path == "/v" || strings.HasPrefix(v.Path, "/api/theatarr") ||
			v.Path == "/healthz" || v.Path == "/readyz" || v.Path == "/metrics":
			e.add(path+".path", "%q is reserved", v.Path)
		default:
			if other, dup := paths[v.Path]; dup {
				e.add(path+".path", "%q is also used by views[%s]", v.Path, other)
			}
			paths[v.Path] = k
		}
		checkEnv(e, path+".api_key_env", v.APIKeyEnv, true)
		if len(v.Versions) == 0 {
			e.add(path+".versions", "required")
		}
		in := map[string]bool{}
		for i, vk := range v.Versions {
			vp := fmt.Sprintf("%s.versions[%d]", path, i)
			if !checkRef(e, vp, "version", vk, p.Versions, true) {
				continue
			}
			if in[vk] {
				e.add(vp, "version %q listed twice", vk)
			}
			in[vk] = true
			if ok && p.Versions[vk].Kind != kind {
				e.add(vp, "version %q is a %s version; a %s view serves %ss", vk, p.Versions[vk].Kind, v.Flavour, kind)
			}
		}
		for _, name := range sortedKeys(v.Profiles) {
			pp := fmt.Sprintf("%s.profiles[%s]", path, name)
			if len(v.Profiles[name]) == 0 {
				e.add(pp, "lists no versions")
			}
			for _, vk := range v.Profiles[name] {
				if !in[vk] {
					e.add(pp, "version %q is not served by this view", vk)
				}
			}
		}
		for i, inst := range v.AliasFrom {
			if inst == "" {
				e.add(fmt.Sprintf("%s.alias_from[%d]", path, i), "empty instance name")
			}
		}
	}
}

func (p *Policy) validateMetadata(e *Errors) {
	m := p.Metadata
	if m == nil {
		return
	}
	if m.OverridesDir != "" {
		dp := "metadata.overrides_dir"
		if !filepath.IsAbs(m.OverridesDir) && strings.HasPrefix(filepath.Clean(m.OverridesDir), "..") {
			e.add(dp, "must be a directory inside the policy directory")
		} else if st, err := os.Stat(p.OverridesPath()); err != nil {
			e.add(dp, "%v", err)
		} else if !st.IsDir() {
			e.add(dp, "%q is not a directory", m.OverridesDir)
		}
	}
	for _, k := range sortedKeys(m.Sources) {
		s := m.Sources[k]
		path := fmt.Sprintf("metadata.sources[%s]", k)
		checkKey(e, path, k)
		checkURL(e, path+".url", s.URL)
		checkEnv(e, path+".api_key_env", s.APIKeyEnv, false)
		checkRef(e, path+".budget", "budget", s.Budget, p.Budgets, false)
	}
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func list(m map[string]bool) string {
	ks := sortedKeys(m)
	return strings.Join(ks, ", ")
}
