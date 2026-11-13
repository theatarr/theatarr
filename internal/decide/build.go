package decide

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// Profile is a policy profile built for one flavour: the engine profile (qualities, cutoff, format items and
// scores), every format the engine computes (the arr's "all formats", scored or not), and the engine with the
// quality definitions and settings it is judged with.
type Profile struct {
	baseEngine *profile.Engine // stable cache identity across per-version copies
	Key        string
	Flavour    format.Flavour
	Profile    *profile.Profile
	Formats    []*format.Format
	// CustomFormats retains the JSON used to build this runtime, for arr-view reads.
	CustomFormats []format.FormatJSON
	Engine        *profile.Engine
}

// Options are what building needs besides the policy.
type Options struct {
	// TrashRoot is a TRaSH-Guides checkout (its docs/json is read), for formats referenced by trash_id.
	TrashRoot string
	// Now is the engine clock (release ages); nil is time.Now.
	Now func() time.Time
	// Previous reuses profiles whose policy inputs did not change.
	Previous *Engine
}

// Engine decides releases for the versions of one policy. It holds the built profiles; it is safe for concurrent
// use once built.
type Engine struct {
	Policy   *policy.Policy
	profiles map[string]*Profile // by profileKey(key, flavour)
	library  Library
	// usable reports whether routes can add to a policy provider (SetUsableProviders); nil counts every one.
	usable func(key string) bool
	// Warnings are formats that could not be built but that no used profile scores; they are left out.
	Warnings []string
}

func profileKey(key string, fl format.Flavour) string { return key + "/" + string(fl) }

// flavourOf is the arr flavour of a version kind.
func flavourOf(k policy.Kind) format.Flavour {
	if k == policy.KindSeries {
		return format.Sonarr
	}
	return format.Radarr
}

// New builds the profile of every version in the policy. Problems are returned as *policy.Errors, each with the
// policy path it concerns (profiles[x].formats[y], formats[z].file, ...).
func New(pol *policy.Policy, opt Options) (*Engine, error) {
	b := &builder{pol: pol, opt: opt, trash: map[format.Flavour]*format.Trash{}, errs: &policy.Errors{}}
	e := &Engine{Policy: pol, profiles: map[string]*Profile{}}
	keys := make([]string, 0, len(pol.Versions))
	for k := range pol.Versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, vk := range keys {
		v := pol.Versions[vk]
		fl := flavourOf(v.Kind)
		pk := profileKey(v.Profile, fl)
		if _, done := e.profiles[pk]; done || pol.Profiles[v.Profile] == nil {
			continue
		}
		if reusableProfile(pol, opt.Previous, v.Profile) {
			if previous := opt.Previous.Profile(v.Profile, fl); previous != nil {
				e.profiles[pk] = previous
				continue
			}
		}
		if p := b.profile(v.Profile, fl); p != nil {
			e.profiles[pk] = p
		}
	}
	e.Warnings = b.warnings
	if len(b.errs.List) > 0 {
		sort.SliceStable(b.errs.List, func(i, j int) bool { return b.errs.List[i].Path < b.errs.List[j].Path })
		return e, b.errs
	}
	return e, nil
}

func reusableProfile(pol *policy.Policy, previous *Engine, key string) bool {
	if previous == nil || previous.Policy == nil || !reflect.DeepEqual(pol.Profiles[key], previous.Policy.Profiles[key]) {
		return false
	}
	profile := pol.Profiles[key]
	if profile == nil {
		return false
	}
	oldProfile := previous.Policy.Profiles[key]
	if profile.FormatSet != "" {
		currentSet, oldSet := pol.FormatSets[profile.FormatSet], previous.Policy.FormatSets[profile.FormatSet]
		if currentSet == nil || oldSet == nil || currentSet.Flavour != oldSet.Flavour {
			return false
		}
	}
	current, old := profileFormats(pol, profile), profileFormats(previous.Policy, oldProfile)
	if !reflect.DeepEqual(current, old) {
		return false
	}
	for name, ref := range current {
		if ref == nil || ref.File == "" || len(ref.Specification) != 0 {
			continue
		}
		before, err := os.ReadFile(filepath.Join(pol.Dir, ref.File))
		if err != nil {
			return false
		}
		after, err := os.ReadFile(filepath.Join(previous.Policy.Dir, old[name].File))
		if err != nil || !reflect.DeepEqual(before, after) {
			return false
		}
	}
	return true
}

func profileFormats(pol *policy.Policy, profile *policy.Profile) map[string]*policy.FormatRef {
	refs := pol.ProfileFormats(profile)
	out := make(map[string]*policy.FormatRef, len(refs))
	for key, ref := range refs {
		out[key] = pol.ResolveFormat(ref)
	}
	return out
}

// Profile returns the built profile for a policy profile key and flavour, or nil.
func (e *Engine) Profile(key string, fl format.Flavour) *Profile {
	return e.profiles[profileKey(key, fl)]
}

type builder struct {
	pol      *policy.Policy
	opt      Options
	trash    map[format.Flavour]*format.Trash
	trashErr map[format.Flavour]error
	errs     *policy.Errors
	warnings []string
	once     sync.Mutex
}

func (b *builder) fail(path, f string, args ...any) {
	b.errs.List = append(b.errs.List, policy.FieldError{Path: path, Msg: fmt.Sprintf(f, args...)})
}

func (b *builder) profile(key string, fl format.Flavour) *Profile {
	pr := b.pol.Profiles[key]
	path := fmt.Sprintf("profiles[%s]", key)
	p := b.fromPolicy(path, key, pr, fl)
	if p == nil {
		return nil
	}
	if pr.Propers != "" {
		p.Engine.Config.DownloadPropersAndRepacks = propersSetting(pr.Propers)
	}
	p.Engine.Now = b.opt.Now
	return p
}

func propersSetting(s string) profile.ProperDownloadTypes {
	switch s {
	case policy.PropersDoNotUpgrade:
		return profile.DoNotUpgrade
	case policy.PropersDoNotPrefer:
		return profile.DoNotPrefer
	}
	return profile.PreferAndUpgrade
}

// fromPolicy builds a profile from the policy's qualities, sizes and format scores.
func (b *builder) fromPolicy(path, key string, pr *policy.Profile, fl format.Flavour) *Profile {
	// Every format in scope, ordered by display name; ids 1..n in that order.
	if pr.FormatSet != "" {
		fs := b.pol.FormatSets[pr.FormatSet]
		if fs == nil || format.Flavour(fs.Flavour) != fl {
			b.fail(path+".format_set", "unknown set or wrong flavour: %q", pr.FormatSet)
			return nil
		}
	}
	refs := b.pol.ProfileFormats(pr)
	type built struct {
		key  string
		f    *format.Format
		json format.FormatJSON
		err  error
	}
	var all []built
	fkeys := make([]string, 0, len(refs))
	for k := range refs {
		fkeys = append(fkeys, k)
	}
	sort.Strings(fkeys)
	for _, k := range fkeys {
		ref := b.pol.ResolveFormat(refs[k])
		if ref == nil {
			b.fail(path+".format_set", "unknown format definition %q", k)
			continue
		}
		if ref.Flavour != "" && format.Flavour(ref.Flavour) != fl {
			continue
		}
		f, j, err := b.format(k, ref, fl)
		all = append(all, built{k, f, j, err})
	}

	// Scores by format key; a reference may be a key or a trash_id.
	scores := map[string]int{}
	ok := true
	for ref, score := range pr.Formats {
		k, _ := b.pol.ProfileFormat(pr, ref)
		if k == "" {
			b.fail(fmt.Sprintf("%s.formats[%s]", path, ref), "unknown format %q; declare it under formats", ref)
			ok = false
			continue
		}
		scores[k] = score
	}
	var formats []*format.Format
	var customFormats []format.FormatJSON
	var items []format.FormatItem
	for _, x := range all {
		if x.err != nil {
			if _, scored := scores[x.key]; scored {
				b.fail(fmt.Sprintf("%s.formats[%s]", path, x.key), "%v", x.err)
				ok = false
			} else {
				b.warn(fmt.Sprintf("formats[%s]: %v (not scored by profiles[%s], left out)", x.key, x.err, key))
			}
			continue
		}
		x.f.ID = len(formats) + 1
		formats = append(formats, x.f)
		x.json.ID, x.json.Name = x.f.ID, x.f.Name
		x.json.IncludeCustomFormatWhenRenaming = x.f.IncludeWhenRenaming
		customFormats = append(customFormats, x.json)
		items = append(items, format.FormatItem{Format: x.f.ID, Name: x.f.Name, Score: scores[x.key]})
	}
	for k := range scores {
		if _, f := b.pol.ProfileFormat(pr, k); f != nil && f.Flavour != "" && format.Flavour(f.Flavour) != fl {
			b.fail(fmt.Sprintf("%s.formats[%s]", path, k), "a %s format, but the profile is used by a %s version", f.Flavour, fl)
			ok = false
		}
	}

	var wanted []format.TrashQualityItem
	allowed := map[string]bool{}
	for _, q := range pr.Qualities {
		allowed[q.Name] = true
	}
	order := pr.Qualities
	if len(pr.QualityOrder) > 0 {
		order = pr.QualityOrder
	}
	for _, q := range order {
		it := format.TrashQualityItem{Name: q.Name, Allowed: allowed[q.Name]}
		if q.Group {
			it.Items = q.Qualities
		}
		wanted = append(wanted, it)
	}
	upgrades := pr.Upgrades == nil || *pr.Upgrades
	qp, err := profile.FromItems(fl, key, wanted, pr.Cutoff, upgrades)
	if err != nil {
		b.fail(path+".qualities", "%v", err)
		return nil
	}
	step := 1
	if pr.MinUpgradeStep != nil {
		step = *pr.MinUpgradeStep
	}
	qp.Profile = format.Profile{Name: key, Flavour: fl, FormatItems: items, MinFormatScore: pr.MinScore,
		CutoffFormatScore: pr.CutoffScore, MinUpgradeFormatScore: step}
	if fl == format.Radarr {
		qp.Language = pr.ProfileLanguage()
	}

	defs := profile.DefaultDefinitions(fl)
	for _, qn := range sortedKeys(pr.Sizes) {
		s := pr.Sizes[qn]
		q, found := profile.QualityByName(fl, qn)
		if !found {
			b.fail(fmt.Sprintf("%s.sizes[%s]", path, qn), "not a %s quality", fl)
			ok = false
			continue
		}
		d := *defs.Get(q)
		d.MinSize = ptr(s.Min)
		d.MaxSize, d.PreferredSize = nil, nil
		if s.Max > 0 {
			d.MaxSize = ptr(s.Max)
		}
		if s.Preferred > 0 || s.PreferredZero {
			d.PreferredSize = ptr(s.Preferred)
		}
		defs.Set(d)
	}
	if !ok {
		return nil
	}
	return &Profile{Key: key, Flavour: fl, Profile: qp, Formats: formats, CustomFormats: customFormats,
		Engine: &profile.Engine{Flavour: fl, Config: profile.Config{PreferredProtocol: profile.ProtocolTorrent}, Definitions: defs}}
}

func (b *builder) warn(s string) {
	for _, w := range b.warnings {
		if w == s {
			return
		}
	}
	b.warnings = append(b.warnings, s)
}

// format builds one policy format in flavour fl, named by its policy key.
func (b *builder) format(key string, ref *policy.FormatRef, fl format.Flavour) (*format.Format, format.FormatJSON, error) {
	var j format.FormatJSON
	raw := ref.Specification
	if len(raw) == 0 && ref.TrashID != "" {
		t, err := b.loadTrash(fl)
		if err != nil {
			return nil, j, err
		}
		raw = t.Raw(ref.TrashID)
		if len(raw) == 0 {
			return nil, j, fmt.Errorf("TRaSH has no %s format %s", fl, ref.TrashID)
		}
	} else if len(raw) == 0 && ref.File != "" {
		var err error
		raw, err = os.ReadFile(filepath.Join(b.pol.Dir, ref.File))
		if err != nil {
			return nil, j, err
		}
	}
	if len(raw) == 0 {
		return nil, j, fmt.Errorf("neither trash_id nor file")
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, j, fmt.Errorf("custom format JSON: %w", err)
	}
	f, err := j.Build(fl)
	if err != nil {
		return nil, j, err
	}
	f.Name = key
	if ref.IncludeWhenRenaming != nil {
		f.IncludeWhenRenaming = *ref.IncludeWhenRenaming
	}
	return f, j, nil
}

func (b *builder) loadTrash(fl format.Flavour) (*format.Trash, error) {
	b.once.Lock()
	defer b.once.Unlock()
	if t := b.trash[fl]; t != nil {
		return t, nil
	}
	if err := b.trashErr[fl]; err != nil {
		return nil, err
	}
	var t *format.Trash
	var err error
	if b.opt.TrashRoot == "" {
		err = fmt.Errorf("a trash_id format needs the TRaSH-Guides JSON, and no TRaSH directory is set")
	} else {
		t, err = format.LoadTrash(b.opt.TrashRoot, fl)
	}
	if err != nil {
		if b.trashErr == nil {
			b.trashErr = map[format.Flavour]error{}
		}
		b.trashErr[fl] = err
		return nil, err
	}
	b.trash[fl] = t
	return t, nil
}

func ptr(v float64) *float64 { return &v }

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
