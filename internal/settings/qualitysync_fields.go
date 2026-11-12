package settings

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// A Recyclarr source manages only the profile values its config sets ("managed fields", named as in policy YAML):
// qualities, cutoff, upgrades, min_score, cutoff_score, min_upgrade_step, "formats/<format>" (a score it assigns, or
// resets with reset_unmatched_scores) and "sizes/<quality>" (from quality_definition). Everything else of an existing
// profile stays as it is. Like the bound TRaSH scores, the source remembers per managed field the value it last
// asked for (FieldState), so a value we changed stays ours when the source changes it.

// RecyclarrImport is a Recyclarr config converted for a sync source (cmd/theatarr's converter).
type RecyclarrImport struct {
	Formats  []CustomFormat
	Profiles []RecyclarrProfile
	// Notes name what the config holds that the sync does not apply; the preview shows them.
	Notes []string
}

// RecyclarrProfile is one profile of the config: Config is complete (taken whole for a profile the settings do not
// hold yet), Fields the managed fields, Reset whether reset_unmatched_scores resets the score of a format (nil when
// the profile does not reset).
type RecyclarrProfile struct {
	Key    string
	Config policy.Profile
	Fields []string
	Reset  func(format string) bool
}

// FieldMemory is one managed field of a source's profile: the source's value when it was last taken into account
// (null: the source removes it).
type FieldMemory struct {
	Upstream json.RawMessage `json:"upstream"`
}

// HeldField is a managed field kept as ours: ours differs from the source's last value (Last) and the source changed
// it (now Upstream). The run keeps ours and remembers the source's new value. Values are JSON; null is "not set".
type HeldField struct {
	Profile  string          `json:"profile"`
	Field    string          `json:"field"`
	Ours     json.RawMessage `json:"ours"`
	Last     json.RawMessage `json:"last"`
	Upstream json.RawMessage `json:"upstream"`
}

// ProfileFieldChange is one profile value the preview's candidate changes (scores are listed as ScoreChange).
type ProfileFieldChange struct {
	Profile string          `json:"profile"`
	Field   string          `json:"field"`
	Before  json.RawMessage `json:"before"`
	After   json.RawMessage `json:"after"`
}

var jsonNull = json.RawMessage("null")

// sizeJSON is a size limit as the settings records spell it (policy YAML names).
type sizeJSON struct {
	Min           float64 `json:"min"`
	Preferred     float64 `json:"preferred"`
	Max           float64 `json:"max"`
	PreferredZero bool    `json:"preferred_zero,omitempty"`
}

// qualityJSON reads a quality item as the settings records spell it: a name, or {group, qualities}.
func qualityJSON(raw json.RawMessage) (policy.QualityItem, error) {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return policy.QualityItem{Name: name}, nil
	}
	var g struct {
		Group     string   `json:"group"`
		Qualities []string `json:"qualities"`
	}
	err := json.Unmarshal(raw, &g)
	return policy.QualityItem{Name: g.Group, Group: true, Qualities: g.Qualities}, err
}

func marshalField(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// profileField reads managed field f of p as JSON (null when a score or size is not set).
func profileField(p *policy.Profile, f string) json.RawMessage {
	if k, ok := strings.CutPrefix(f, "formats/"); ok {
		if v, ok := p.Formats[k]; ok {
			return marshalField(v)
		}
		return jsonNull
	}
	if k, ok := strings.CutPrefix(f, "sizes/"); ok {
		if v, ok := p.Sizes[k]; ok {
			return marshalField(sizeJSON(v))
		}
		return jsonNull
	}
	switch f {
	case "qualities":
		items := make([]any, 0, len(p.Qualities))
		for _, q := range p.Qualities {
			if q.Group {
				items = append(items, map[string]any{"group": q.Name, "qualities": q.Qualities})
			} else {
				items = append(items, q.Name)
			}
		}
		return marshalField(items)
	case "cutoff":
		return marshalField(p.Cutoff)
	case "upgrades":
		return marshalField(p.Upgrades == nil || *p.Upgrades)
	case "min_score":
		return marshalField(p.MinScore)
	case "cutoff_score":
		return marshalField(p.CutoffScore)
	case "min_upgrade_step":
		step := 1
		if p.MinUpgradeStep != nil {
			step = *p.MinUpgradeStep
		}
		return marshalField(step)
	}
	return jsonNull
}

// setProfileField sets managed field f of p from JSON (null removes a score or size). Taking the qualities drops a
// quality_order made for the old ones and moves a cutoff that is no longer allowed to the first allowed item. p's
// maps are replaced, never written in place: they may be shared with a snapshot.
func setProfileField(p *policy.Profile, f string, raw json.RawMessage) error {
	null := sameFieldJSON(raw, jsonNull)
	if k, ok := strings.CutPrefix(f, "formats/"); ok {
		m := map[string]int{}
		for key, v := range p.Formats {
			m[key] = v
		}
		delete(m, k)
		if !null {
			var v int
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			m[k] = v
		}
		p.Formats = m
		return nil
	}
	if k, ok := strings.CutPrefix(f, "sizes/"); ok {
		m := map[string]policy.SizeLimit{}
		for key, v := range p.Sizes {
			m[key] = v
		}
		delete(m, k)
		if !null {
			var v sizeJSON
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			m[k] = policy.SizeLimit(v)
		}
		p.Sizes = m
		return nil
	}
	var err error
	switch f {
	case "qualities":
		var items []json.RawMessage
		if err = json.Unmarshal(raw, &items); err != nil {
			return err
		}
		q := make([]policy.QualityItem, len(items))
		for i, it := range items {
			if q[i], err = qualityJSON(it); err != nil {
				return err
			}
		}
		p.Qualities = q
		if !orderFits(p.QualityOrder, q) {
			p.QualityOrder = nil
		}
		fixCutoff(p, "")
	case "cutoff":
		err = json.Unmarshal(raw, &p.Cutoff)
	case "upgrades":
		var v bool
		if err = json.Unmarshal(raw, &v); err == nil {
			p.Upgrades = &v
		}
	case "min_score":
		err = json.Unmarshal(raw, &p.MinScore)
	case "cutoff_score":
		err = json.Unmarshal(raw, &p.CutoffScore)
	case "min_upgrade_step":
		var v int
		if err = json.Unmarshal(raw, &v); err == nil {
			p.MinUpgradeStep = &v
		}
	}
	return err
}

// orderFits reports whether a quality_order still ranks qualities: its allowed items are qualities, in order and
// with the same group members (what validation asks).
func orderFits(order, qualities []policy.QualityItem) bool {
	names := map[string]bool{}
	for _, q := range qualities {
		names[q.Name] = true
	}
	var allowed []policy.QualityItem
	for _, q := range order {
		if names[q.Name] {
			allowed = append(allowed, q)
		}
	}
	return len(order) > 0 && slices.EqualFunc(allowed, qualities, func(a, b policy.QualityItem) bool {
		return a.Name == b.Name && a.Group == b.Group && slices.Equal(a.Qualities, b.Qualities)
	})
}

// fixCutoff moves a cutoff that is not one of p's allowed items to want when that is one, else to the first.
func fixCutoff(p *policy.Profile, want string) {
	names := map[string]bool{}
	for _, q := range p.Qualities {
		names[q.Name] = true
	}
	if len(p.Qualities) == 0 || names[p.Cutoff] {
		return
	}
	p.Cutoff = p.Qualities[0].Name
	if names[want] {
		p.Cutoff = want
	}
}

func sameFieldJSON(a, b json.RawMessage) bool {
	if len(a) == 0 {
		a = jsonNull
	}
	if len(b) == 0 {
		b = jsonNull
	}
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// followField decides one managed field whose source value is now up, as followScore does for a bound score: it
// follows the source (take) while ours still equals the source's last value, is left alone while the source has not
// changed it, and takes the source's value the first time the source sets it (not known). Ours, when it differs from
// the source's last value and the source changed it, is kept. It returns the memory to keep: always the source's
// current value.
func followField(ours, up json.RawMessage, prev FieldMemory, known bool) (mem FieldMemory, take, kept bool) {
	switch {
	case sameFieldJSON(ours, up):
	case known && sameFieldJSON(prev.Upstream, up):
		return prev, false, false
	case !known || sameFieldJSON(ours, prev.Upstream):
		return FieldMemory{Upstream: up}, true, false
	default:
		return FieldMemory{Upstream: up}, false, true
	}
	return FieldMemory{Upstream: up}, false, false
}

// reconcileProfile applies the managed fields of in to cur. A draft takes every source value and remembers
// nothing. keep reports a score the reset must leave alone (a retired format's). It returns the memory to keep
// and the fields kept as ours.
func reconcileProfile(cur *policy.Profile, in RecyclarrProfile, last map[string]FieldMemory, draft bool, keep func(string) bool) (map[string]FieldMemory, []HeldField, error) {
	up := map[string]json.RawMessage{}
	for _, f := range in.Fields {
		up[f] = profileField(&in.Config, f)
	}
	if in.Reset != nil {
		// A score reset earlier stays remembered while the reset covers it, so restored by hand it is ours.
		resettable := slices.Collect(maps.Keys(cur.Formats))
		for f := range last {
			if k, ok := strings.CutPrefix(f, "formats/"); ok {
				resettable = append(resettable, k)
			}
		}
		for _, k := range resettable {
			if _, managed := up["formats/"+k]; !managed && !keep(k) && in.Reset(k) {
				up["formats/"+k] = jsonNull
			}
		}
	}
	// Each field is compared with the profile as it was before the run: taking one (the qualities) may move another
	// (the cutoff).
	before := *cur
	next := map[string]FieldMemory{}
	var held []HeldField
	fields := sortedStringKeys(up)
	// The qualities go first: taking them may move the cutoff, which the cutoff field then sees.
	sort.SliceStable(fields, func(i, j int) bool { return fields[i] == "qualities" && fields[j] != "qualities" })
	for _, f := range fields {
		ours := profileField(&before, f)
		if draft {
			if err := setProfileField(cur, f, up[f]); err != nil {
				return nil, nil, err
			}
			continue
		}
		prev, known := last[f]
		mem, take, kept := followField(ours, up[f], prev, known)
		switch {
		case kept:
			// Ours stays; the source's new value is remembered, so the next run leaves it alone.
			held = append(held, HeldField{Profile: in.Key, Field: f, Ours: ours, Last: prev.Upstream, Upstream: up[f]})
		case take:
			if err := setProfileField(cur, f, up[f]); err != nil {
				return nil, nil, err
			}
		}
		next[f] = mem
	}
	fixCutoff(cur, in.Config.Cutoff)
	return next, held, nil
}

// newProfileMemory is what a source remembers of a profile it creates: every managed field as it set it.
func newProfileMemory(in RecyclarrProfile) map[string]FieldMemory {
	out := map[string]FieldMemory{}
	for _, f := range in.Fields {
		out[f] = FieldMemory{Upstream: profileField(&in.Config, f)}
	}
	return out
}

// profileFieldNames are the profile values a preview lists one by one; any other change names only the profile.
var profileFieldNames = []string{"qualities", "cutoff", "upgrades", "min_score", "cutoff_score", "min_upgrade_step"}

// fieldChanges lists the managed-field values that differ between before and after, sizes per quality.
func fieldChanges(key string, before, after *policy.Profile) []ProfileFieldChange {
	names := append([]string(nil), profileFieldNames...)
	sizes := map[string]bool{}
	for q := range before.Sizes {
		sizes[q] = true
	}
	for q := range after.Sizes {
		sizes[q] = true
	}
	for _, q := range sortedStringKeys(sizes) {
		names = append(names, "sizes/"+q)
	}
	var out []ProfileFieldChange
	for _, f := range names {
		a, b := profileField(before, f), profileField(after, f)
		if !sameFieldJSON(a, b) {
			out = append(out, ProfileFieldChange{Profile: key, Field: f, Before: a, After: b})
		}
	}
	return out
}

// sameFieldState compares two field states by their upstream values as JSON; an empty profile entry equals none.
func sameFieldState(a, b map[string]map[string]FieldMemory) bool {
	norm := func(s map[string]map[string]FieldMemory) map[string]map[string]any {
		out := map[string]map[string]any{}
		for p, fields := range s {
			if len(fields) == 0 {
				continue
			}
			m := map[string]any{}
			for f, mem := range fields {
				var v any
				_ = json.Unmarshal(mem.Upstream, &v)
				m[f] = v
			}
			out[p] = m
		}
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}
