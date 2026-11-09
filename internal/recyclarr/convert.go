package recyclarr

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// Options are the inputs besides the recyclarr config.
type Options struct {
	// TrashRoot is a TRaSH-Guides checkout (metadata.json and docs/json).
	TrashRoot string
	// QualitySizes applies quality_definition (TRaSH's quality sizes) to the profiles: the instance's to every
	// profile it converts, a profile's own instead of it. Off, quality_definition is noted as not applied.
	QualitySizes bool
	// Reconcile converts for a settings sync that applies the profiles' managed fields to existing profiles: a
	// profile the config only assigns scores to (no qualities) converts without qualities.
	Reconcile bool
}

// Output is one instance converted: a self-contained policy fragment (formats and profiles).
type Output struct {
	Instance string
	Flavour  Flavour
	Formats  []*OutFormat // by name
	Profiles []*OutProfile
	// Notes say what the reader must know: keys not converted, quality sizes not applied or skipped.
	Notes []string
}

// OutFormat is one format of the output: a TRaSH format recyclarr syncs (policy trash_id).
type OutFormat struct {
	Name    string
	TrashID string
}

// OutProfile is one quality profile of the output.
type OutProfile struct {
	// Key is the policy profile key: <instance>-<slug of the profile name>.
	Key     string
	ArrName string
	Policy  *policy.Profile
	// Managed lists the format names whose scores came from recyclarr.
	Managed []string
	// Fields are the profile values the config sets, by policy name: qualities, cutoff, upgrades, min_score,
	// cutoff_score, min_upgrade_step, "formats/<format name>" (a score it assigns) and "sizes/<quality>" (a size from
	// quality_definition). A settings sync touches only these on an existing profile.
	Fields []string
	// Reset is set when the profile resets unmatched scores: it reports whether a score of format name is reset.
	Reset func(name string) bool
}

// Convert converts every instance of the config.
func Convert(cfg *Config, opt Options) ([]*Output, error) {
	var out []*Output
	trash := map[Flavour]*format.Trash{}
	for _, inst := range cfg.Instances {
		t := trash[inst.Flavour]
		if t == nil {
			var err error
			if t, err = format.LoadTrash(opt.TrashRoot, format.Flavour(inst.Flavour)); err != nil {
				return nil, fmt.Errorf("recyclarr: TRaSH: %w", err)
			}
			trash[inst.Flavour] = t
		}
		o, err := convertOne(inst, t, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func convertOne(inst *Instance, t *format.Trash, opt Options) (*Output, error) {
	o := &Output{Instance: inst.Name, Flavour: inst.Flavour}
	for _, k := range inst.Ignored {
		o.Notes = append(o.Notes, fmt.Sprintf("%s: not converted (no effect on formats or scores)", k))
	}
	hasSizes := inst.QualityDefinition != nil
	for _, qp := range inst.QualityProfiles {
		hasSizes = hasSizes || qp.QualityDefinition != nil
	}
	var sizes *profile.TrashSizes
	if hasSizes && opt.QualitySizes {
		var err error
		if sizes, err = profile.LoadTrashSizes(opt.TrashRoot, format.Flavour(inst.Flavour)); err != nil {
			return nil, fmt.Errorf("recyclarr: TRaSH quality sizes: %w", err)
		}
	} else if hasSizes {
		o.Notes = append(o.Notes, "quality_definition: not applied")
	}

	// Formats recyclarr manages, by name. A name TRaSH gives twice (two trash ids) is an error, as in the arr.
	formats := map[string]*OutFormat{}
	managedByID := map[string]*format.Format{}
	for i, e := range inst.CustomFormats {
		for _, id := range e.TrashIDs {
			f, ok := t.Format(id)
			if !ok {
				return nil, fmt.Errorf("recyclarr: %s.custom_formats[%d]: TRaSH has no %s format %s", inst.Name, i,
					inst.Flavour, id)
			}
			if x := formats[f.Name]; x != nil && x.TrashID != id {
				return nil, fmt.Errorf("recyclarr: %s: two formats named %q (%s, %s)", inst.Name, f.Name, x.TrashID, id)
			}
			formats[f.Name] = &OutFormat{Name: f.Name, TrashID: id}
			managedByID[id] = f
		}
	}
	for _, f := range formats {
		o.Formats = append(o.Formats, f)
	}
	sortFormats(o.Formats)

	// Profiles: every profile recyclarr configures or assigns scores to.
	var names []string
	for _, qp := range inst.QualityProfiles {
		if !contains(names, qp.Name) {
			names = append(names, qp.Name)
		}
	}
	for _, e := range inst.CustomFormats {
		for _, a := range e.AssignTo {
			if !contains(names, a.Name) {
				names = append(names, a.Name)
			}
		}
	}
	for _, name := range names {
		var qp *QualityProfile
		for i := range inst.QualityProfiles {
			if inst.QualityProfiles[i].Name == name {
				qp = &inst.QualityProfiles[i]
			}
		}
		p, err := convertProfile(inst, name, qp, t, formats, opt.Reconcile)
		if err != nil {
			return nil, err
		}
		if err := applySizes(o, inst, qp, p, sizes); err != nil {
			return nil, err
		}
		o.Profiles = append(o.Profiles, p)
	}
	return o, nil
}

func convertProfile(inst *Instance, name string, qp *QualityProfile, t *format.Trash,
	formats map[string]*OutFormat, reconcile bool) (*OutProfile, error) {
	op := &OutProfile{Key: ProfileKey(inst.Name, name), ArrName: name}
	if (qp == nil || len(qp.Qualities) == 0) && !reconcile {
		return nil, fmt.Errorf("recyclarr: %s profile %q: no qualities in the config", inst.Name, name)
	}
	if qp != nil && qp.TrashID != "" {
		return nil, fmt.Errorf("recyclarr: %s profile %q: guide-backed profiles (trash_id) are not converted",
			inst.Name, name)
	}
	base := &policy.Profile{Formats: map[string]int{}}
	one := 1
	base.MinUpgradeStep = &one
	if qp != nil {
		for _, f := range []struct {
			name string
			set  bool
		}{{"qualities", len(qp.Qualities) > 0}, {"cutoff", qp.UntilQuality != ""}, {"upgrades", qp.UpgradeAllowed != nil},
			{"min_score", qp.MinFormatScore != nil}, {"cutoff_score", qp.UntilScore != nil},
			{"min_upgrade_step", qp.MinUpgradeFormatScore != nil}} {
			if f.set {
				op.Fields = append(op.Fields, f.name)
			}
		}
		if qp.ResetUnmatched {
			op.Reset = func(n string) bool { return !resetExcepted(qp, n) }
		}
		if len(qp.Qualities) > 0 {
			base.Qualities = nil
			for _, q := range qp.Qualities {
				if !q.Enabled {
					continue
				}
				if len(q.Qualities) > 0 {
					base.Qualities = append(base.Qualities, policy.QualityItem{Name: q.Name, Group: true, Qualities: q.Qualities})
				} else {
					base.Qualities = append(base.Qualities, policy.QualityItem{Name: q.Name})
				}
			}
		}
		if qp.MinFormatScore != nil {
			base.MinScore = *qp.MinFormatScore
		}
		if qp.MinUpgradeFormatScore != nil {
			v := *qp.MinUpgradeFormatScore
			base.MinUpgradeStep = &v
		}
		if qp.UpgradeAllowed != nil {
			v := *qp.UpgradeAllowed
			base.Upgrades = &v
		}
		if qp.UntilScore != nil {
			base.CutoffScore = *qp.UntilScore
		}
		if qp.UntilQuality != "" {
			base.Cutoff = qp.UntilQuality
		}
	}
	if base.Cutoff == "" && len(base.Qualities) > 0 {
		base.Cutoff = base.Qualities[0].Name
	}
	scoreSet := ""
	if qp != nil {
		scoreSet = qp.ScoreSet
	}
	// Scores recyclarr assigns, in config order (a later entry wins, as recyclarr applies them in order).
	for _, e := range inst.CustomFormats {
		for _, a := range e.AssignTo {
			if a.Name != name {
				continue
			}
			for _, id := range e.TrashIDs {
				f, _ := t.Format(id)
				score, ok := 0, false
				switch {
				case a.Score != nil:
					score, ok = *a.Score, true
				case e.Score != nil:
					score, ok = *e.Score, true
				default:
					score, ok = format.TrashScore(f, scoreSet)
				}
				if !ok {
					continue
				}
				base.Formats[f.Name] = score
				if !contains(op.Managed, f.Name) {
					op.Managed = append(op.Managed, f.Name)
				}
			}
		}
	}
	sort.Strings(op.Managed)
	for _, n := range op.Managed {
		op.Fields = append(op.Fields, "formats/"+n)
	}
	for n, s := range base.Formats {
		if s == 0 && !contains(op.Managed, n) {
			delete(base.Formats, n)
		}
		if formats[n] == nil {
			return nil, fmt.Errorf("recyclarr: %s profile %q scores %q, which is not converted", inst.Name, name, n)
		}
	}
	op.Policy = base
	return op, nil
}

// applySizes sets the profile's sizes from its quality_definition (else the instance's), when sizes were loaded.
func applySizes(o *Output, inst *Instance, qp *QualityProfile, p *OutProfile, sizes *profile.TrashSizes) error {
	qd := inst.QualityDefinition
	if qp != nil && qp.QualityDefinition != nil {
		qd = qp.QualityDefinition
	}
	if sizes == nil || qd == nil {
		return nil
	}
	limits, notes, err := DefinitionSizes(sizes, qd)
	if err != nil {
		return fmt.Errorf("recyclarr: %s profile %q: %w", inst.Name, p.ArrName, err)
	}
	for _, n := range notes {
		if !contains(o.Notes, n) {
			o.Notes = append(o.Notes, n)
		}
	}
	if p.Policy.Sizes == nil {
		p.Policy.Sizes = map[string]policy.SizeLimit{}
	}
	names := make([]string, 0, len(limits))
	for q := range limits {
		names = append(names, q)
	}
	sort.Strings(names)
	for _, q := range names {
		p.Policy.Sizes[q] = limits[q]
		p.Fields = append(p.Fields, "sizes/"+q)
	}
	return nil
}

// DefinitionSizes is recyclarr's QualitySizePlanComponent and QualitySizeCalculator for one quality_definition, in
// policy sizes (MB per minute; 0 max or preferred is unlimited): TRaSH's sizes of the type with the config's
// overrides, max and preferred capped at the service limits (at or above the limit: unlimited), and preferred at
// preferred_ratio between min and max when given. A quality Theatarr does not know is skipped with a note.
func DefinitionSizes(ts *profile.TrashSizes, qd *QualityDefinition) (map[string]policy.SizeLimit, []string, error) {
	file, ok := ts.ByType(qd.Type)
	if !ok {
		return nil, nil, fmt.Errorf("quality_definition type %q is not a TRaSH %s quality-size type", qd.Type, ts.Flavour)
	}
	maxLimit, prefLimit := profile.SizeLimits(ts.Flavour)
	var notes []string
	ratio := qd.PreferredRatio
	if ratio != nil && (*ratio < 0 || *ratio > 1) {
		r := min(max(*ratio, 0), 1)
		notes = append(notes, fmt.Sprintf("quality_definition: preferred_ratio %v is out of range (0-1), clamped to %v", *ratio, r))
		ratio = &r
	}
	overrides := map[string]QualitySizeOverride{}
	for _, o := range qd.Qualities {
		overrides[strings.ToLower(o.Name)] = o
	}
	out := map[string]policy.SizeLimit{}
	for _, q := range file.Qualities {
		delete(overrides, strings.ToLower(q.Quality))
	}
	for name := range overrides {
		return nil, nil, fmt.Errorf("quality_definition: quality %q is not in TRaSH's %s sizes", name, qd.Type)
	}
	for _, o := range qd.Qualities {
		overrides[strings.ToLower(o.Name)] = o
	}
	for _, q := range file.Qualities {
		if _, ok := profile.QualityByName(ts.Flavour, q.Quality); !ok {
			notes = append(notes, fmt.Sprintf("quality_definition: %s is not a quality Theatarr knows; skipped", q.Quality))
			continue
		}
		minV, maxV, pref := q.Min, maxLimit, prefLimit
		if q.Max != nil {
			maxV = *q.Max
		}
		if q.Preferred != nil {
			pref = *q.Preferred
		}
		if o, ok := overrides[strings.ToLower(q.Quality)]; ok {
			var err error
			if minV, err = sizeValue(o.Min, minV, minV); err != nil {
				return nil, nil, err
			}
			if maxV, err = sizeValue(o.Max, maxV, maxLimit); err != nil {
				return nil, nil, err
			}
			if pref, err = sizeValue(o.Preferred, pref, prefLimit); err != nil {
				return nil, nil, err
			}
		}
		maxV, pref = min(maxV, maxLimit), min(pref, prefLimit)
		if ratio != nil {
			pref = math.Round((minV+(min(maxV, prefLimit)-minV)**ratio)*10) / 10
		}
		if minV > pref || pref > maxV {
			return nil, nil, fmt.Errorf("quality_definition: %s: min %v, preferred %v and max %v are out of order", q.Quality, minV, pref, maxV)
		}
		l := policy.SizeLimit{Min: minV}
		if maxV < maxLimit {
			l.Max = maxV
		}
		if pref < prefLimit {
			l.Preferred, l.PreferredZero = pref, pref == 0
		}
		out[q.Quality] = l
	}
	return out, notes, nil
}

// sizeValue reads an override: "" keeps def, "unlimited" is unlimited (min keeps def), else a number.
func sizeValue(v string, def, unlimited float64) (float64, error) {
	switch {
	case v == "":
		return def, nil
	case strings.EqualFold(v, "unlimited"):
		return unlimited, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("quality_definition: invalid size %q (a number or unlimited)", v)
	}
	return f, nil
}

func resetExcepted(qp *QualityProfile, name string) bool {
	for _, e := range qp.ResetExcept {
		if strings.EqualFold(e, name) {
			return true
		}
	}
	for _, p := range qp.ResetExceptPatterns {
		if re, err := regexp.Compile("(?i)" + p); err == nil && re.MatchString(name) {
			return true
		}
	}
	return false
}

// ProfileKey is the policy key of an instance's profile: "radarr_4k_web-web-2160p".
func ProfileKey(instance, profile string) string {
	return instance + "-" + Slug(profile)
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lower-cases a name and joins its words with dashes.
func Slug(s string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func sortFormats(fs []*OutFormat) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
