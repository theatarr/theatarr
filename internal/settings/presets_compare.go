package settings

// What the Quality profiles and Custom formats pages show: each profile against the TRaSH preset it started from
// (stored, or the closest one when none is stored), each score's state (follows, pinned, held), and each format's definition
// origin with the profiles that score it. Everything is read from one policy, so the YAML rollback shows it too.

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// Score states and format definition origins.
const (
	StateFollows  = "follows"  // equal to TRaSH's last score; follows TRaSH's changes
	StatePinned   = "pinned"   // ours: differs from TRaSH's last score, or kept; held only when TRaSH changes it
	StateHeld     = "held"     // waits in the held-score review
	StateDiffers  = "differs"  // bound, never asked: differs from TRaSH, asked once at the next sync run
	StateOurs     = "ours"     // our own format: TRaSH has nothing like it
	StateUnbound  = "unbound"  // the profile is not bound to a TRaSH score set: TRaSH's score is shown only
	StateUnlinked = "unlinked" // a frozen copy of a TRaSH format without its id: no sync touches it
	StateNoScore  = "no_score" // TRaSH sets no score for it in the profile's score set: ours, no sync changes it

	DefinitionTrash  = "trash"  // carries a TRaSH id: definition updates come from TRaSH sync
	DefinitionFrozen = "frozen" // a copy from the arrs with a TRaSH name and no id ("Link formats to TRaSH")
	DefinitionOurs   = "ours"   // ours alone
)

// summaryLines caps OriginDiff.Summary; the structured lists keep every edit.
const summaryLines = 8

const groupOurs = "Our formats"
const groupProfile = "Tiers and repacks (from TRaSH profiles)"
const groupOther = "Other TRaSH formats"

// OverviewInput is what QualityOverviewOf reads. Profiles and Formats carry record ids and stored origins (DB mode);
// Held is the held scores of pending score reviews; Trash holds the local TRaSH tree per flavour, if any.
type OverviewInput struct {
	Policy   *policy.Policy
	Profiles []QualityProfile
	Formats  []CustomFormat
	Sources  []QualitySyncSource
	Held     []HeldScore
	Trash    map[string]*format.Trash
}

type QualityOverview struct {
	Profiles []ProfileSummary `json:"profiles"`
	Formats  []FormatSummary  `json:"formats"`
	// Trash lists the flavours a local TRaSH tree was read for (presets, comparisons and groups need it).
	Trash []string `json:"trash"`
}

type VersionUse struct {
	Key    string `json:"key"`
	Mode   string `json:"mode"`
	Kind   string `json:"kind"`
	Anime  *bool  `json:"anime"`
	Active bool   `json:"active"`
}

type ProfileSummary struct {
	ID        int64          `json:"id"`
	Key       string         `json:"key"`
	Flavour   string         `json:"flavour"`
	Qualities []string       `json:"qualities"`
	Cutoff    string         `json:"cutoff"`
	FormatSet string         `json:"format_set"`
	Formats   int            `json:"formats"`
	FromArr   string         `json:"from_arr,omitempty"`
	UsedBy    []VersionUse   `json:"used_by"`
	Held      int            `json:"held"`
	Origin    *ProfileOrigin `json:"origin"`
	// Guessed marks an origin that is not stored: the closest preset (with modifiers) Theatarr found.
	Guessed bool        `json:"guessed"`
	Diff    *OriginDiff `json:"diff"`
	// Binding is the sync source and TRaSH score set the profile's scores follow, empty when unbound.
	Binding  string     `json:"binding"`
	ScoreSet string     `json:"score_set"`
	Scores   []ScoreRow `json:"scores"`
}

// ScoreRow is one score of a profile with its score state, grouped as TRaSH groups the format.
type ScoreRow struct {
	Format     string `json:"format"`
	TrashID    string `json:"trash_id,omitempty"`
	Score      int    `json:"score"`
	Trash      *int   `json:"trash"`
	State      string `json:"state"`
	Definition string `json:"definition"`
	Group      string `json:"group"`
}

type FormatUse struct {
	Profile string `json:"profile"`
	Score   int    `json:"score"`
	State   string `json:"state"`
}

// FormatSummary is one format definition: a TRaSH format (by id, or by name for a frozen copy) or one of ours, with
// every place it is defined and every profile that scores it.
type FormatSummary struct {
	Name       string   `json:"name"`
	Names      []string `json:"names"`
	Flavour    string   `json:"flavour"`
	TrashID    string   `json:"trash_id,omitempty"`
	Definition string   `json:"definition"`
	Group      string   `json:"group"`
	Scopes     []string `json:"scopes"`
	// ID and Key name the standalone format record when there is one (the Custom formats editor edits it).
	ID       int64       `json:"id,omitempty"`
	Key      string      `json:"key,omitempty"`
	ScoredBy []FormatUse `json:"scored_by"`
}

type ValueChange struct {
	Ours  string `json:"ours"`
	Trash string `json:"trash"`
}

type FieldChange struct {
	Field string `json:"field"`
	Ours  string `json:"ours"`
	Trash string `json:"trash"`
}

type ScoreDiff struct {
	Format  string `json:"format"`
	TrashID string `json:"trash_id,omitempty"`
	Ours    *int   `json:"ours"`
	Trash   *int   `json:"trash"`
}

// OriginDiff is a profile against its preset (with the origin's modifiers): our edits.
type OriginDiff struct {
	Preset           string        `json:"preset"`
	QualitiesAdded   []string      `json:"qualities_added"`
	QualitiesRemoved []string      `json:"qualities_removed"`
	Cutoff           *ValueChange  `json:"cutoff"`
	Settings         []FieldChange `json:"settings"`
	FormatsAdded     []ScoreDiff   `json:"formats_added"`
	FormatsMissing   []ScoreDiff   `json:"formats_missing"`
	ScoresChanged    []ScoreDiff   `json:"scores_changed"`
	// Summary is the edits in short lines, the most telling first ("+ Remux-1080p, Bluray-1080p", "DD+ 2500 (TRaSH
	// 1750)"); Same is true when there is none.
	Summary []string `json:"summary"`
	Same    bool     `json:"same"`
}

// VersionsUsing lists the versions that use profile key, by key.
func VersionsUsing(p *policy.Policy, key string) []VersionUse {
	out := []VersionUse{}
	if p == nil {
		return out
	}
	for _, vk := range sortedPolicyKeys(p.Versions) {
		v := p.Versions[vk]
		if v.Profile == key {
			out = append(out, VersionUse{Key: vk, Mode: string(v.Mode), Kind: string(v.Kind), Anime: v.Anime, Active: v.Mode == policy.ModeActive})
		}
	}
	return out
}

// profileFlavour is the profile's arr flavour: its format set's, else the kind of a version using it.
func profileFlavour(p *policy.Policy, key string, pr *policy.Profile) string {
	if pr.FormatSet != "" && p.FormatSets[pr.FormatSet] != nil {
		return p.FormatSets[pr.FormatSet].Flavour
	}
	for _, vk := range sortedPolicyKeys(p.Versions) {
		v := p.Versions[vk]
		if v.Profile == key {
			if v.Kind == policy.KindSeries {
				return string(format.Sonarr)
			}
			return string(format.Radarr)
		}
	}
	return ""
}

// identity is a format's TRaSH id: its own, or, for a copy without one, the id of the TRaSH format of its name
// (exact, or without Notifiarr's " - Notifiarr"). definition says which.
func identity(t *format.Trash, name string, ref *policy.FormatRef) (id, definition string) {
	if ref != nil {
		if ref.TrashID != "" {
			return ref.TrashID, DefinitionTrash
		}
		var j format.FormatJSON
		if len(ref.Specification) > 0 && json.Unmarshal(ref.Specification, &j) == nil && j.TrashID != "" {
			return j.TrashID, DefinitionTrash
		}
	}
	if t != nil {
		for _, n := range []string{name, strings.TrimSuffix(name, " - Notifiarr")} {
			if f := trashFormatNamed(t, n); f != nil {
				return f.TrashID, DefinitionFrozen
			}
		}
	}
	return "", DefinitionOurs
}

// TrashGroups maps each TRaSH format id of t to the group the pages show it in.
func TrashGroups(t *format.Trash) map[string]string { return trashGroups(t) }

// trashGroups names each TRaSH format's group: its first cf-group by name, else "tiers and repacks" for a format a
// TRaSH profile lists itself.
func trashGroups(t *format.Trash) map[string]string {
	out := map[string]string{}
	if t == nil {
		return out
	}
	groups := make([]*format.TrashGroup, 0, len(t.Groups))
	for _, g := range t.Groups {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	for _, g := range groups {
		for _, cf := range g.CustomFormats {
			if _, ok := out[cf.TrashID]; !ok {
				out[cf.TrashID] = g.Name
			}
		}
	}
	for _, tp := range t.Profiles {
		for _, id := range tp.FormatItems {
			if _, ok := out[id]; !ok {
				out[id] = groupProfile
			}
		}
	}
	return out
}

// QualityOverviewOf builds both pages' data from one policy.
func QualityOverviewOf(in OverviewInput) QualityOverview {
	p := in.Policy
	out := QualityOverview{Profiles: []ProfileSummary{}, Formats: []FormatSummary{}, Trash: []string{}}
	for fl, t := range in.Trash {
		if t != nil {
			out.Trash = append(out.Trash, fl)
		}
	}
	sort.Strings(out.Trash)
	if p == nil {
		return out
	}
	groups := map[string]map[string]string{}
	for fl, t := range in.Trash {
		groups[fl] = trashGroups(t)
	}
	records := map[string]QualityProfile{}
	for _, r := range in.Profiles {
		records[r.Key] = r
	}
	held := map[string]bool{}
	for _, h := range in.Held {
		held[h.Profile+"\x00"+h.Format] = true
	}
	formats := map[string]*FormatSummary{}
	order := []string{}
	formatKey := func(flavour, id, name string) string {
		if id != "" {
			return flavour + "/" + id
		}
		return flavour + "/name:" + name
	}
	addFormat := func(flavour, name, scope string, ref *policy.FormatRef, recordID int64) *FormatSummary {
		t := in.Trash[flavour]
		id, def := identity(t, name, ref)
		k := formatKey(flavour, id, name)
		f := formats[k]
		if f == nil {
			f = &FormatSummary{Name: name, Names: []string{}, Flavour: flavour, TrashID: id, Definition: def, Scopes: []string{}, ScoredBy: []FormatUse{}}
			if def == DefinitionOurs {
				f.Group = groupOurs
			} else if g := groups[flavour][id]; g != "" {
				f.Group = g
			} else {
				f.Group = groupOther
			}
			if def != DefinitionOurs && t != nil {
				if tf, ok := t.Format(id); ok {
					f.Name = tf.Name
				}
			}
			formats[k] = f
			order = append(order, k)
		}
		// A linked copy anywhere makes the row a TRaSH definition.
		if def == DefinitionTrash {
			f.Definition = DefinitionTrash
		}
		if !slices.Contains(f.Names, name) {
			f.Names = append(f.Names, name)
		}
		if !slices.Contains(f.Scopes, scope) {
			f.Scopes = append(f.Scopes, scope)
		}
		if recordID != 0 {
			f.ID, f.Key = recordID, name
		}
		return f
	}
	globalIDs := map[string]int64{}
	for _, r := range in.Formats {
		globalIDs[r.Key] = r.ID
	}
	for _, key := range sortedPolicyKeys(p.Formats) {
		ref := p.Formats[key]
		fl := ref.Flavour
		if fl == "" {
			fl = string(format.Radarr)
		}
		addFormat(fl, key, "global", ref, globalIDs[key])
	}
	for _, setKey := range sortedPolicyKeys(p.FormatSets) {
		set := p.FormatSets[setKey]
		for _, name := range sortedPolicyKeys(set.Formats) {
			ref := set.Formats[name]
			if ref != nil && ref.Ref != "" {
				continue
			}
			addFormat(set.Flavour, name, setKey, ref, 0)
		}
	}
	for _, key := range sortedPolicyKeys(p.Profiles) {
		pr := p.Profiles[key]
		fl := profileFlavour(p, key, pr)
		t := in.Trash[fl]
		s := ProfileSummary{Key: key, Flavour: fl, Qualities: allowedNames(pr.Qualities), Cutoff: pr.Cutoff, FormatSet: pr.FormatSet,
			Formats: len(pr.Formats), FromArr: pr.FromArr, UsedBy: []VersionUse{}, Scores: []ScoreRow{}}
		if r, ok := records[key]; ok {
			s.ID = r.ID
			if r.Origin != nil {
				origin := *r.Origin
				s.Origin = &origin
			}
		}
		s.UsedBy = VersionsUsing(p, key)
		for _, h := range in.Held {
			if h.Profile == key {
				s.Held++
			}
		}
		s.Binding, s.ScoreSet = ProfileBinding(in.Sources, key)
		var state ProfileScoreState
		for _, src := range in.Sources {
			if src.Key == s.Binding {
				state = src.Config.ScoreState[key]
			}
		}
		if state.Set != s.ScoreSet {
			state = ProfileScoreState{}
		}
		scoreSet := s.ScoreSet
		if scoreSet == "" && s.Origin != nil {
			scoreSet = s.Origin.ScoreSet
		}
		ids := map[string]string{}
		for _, name := range sortedStringKeys(pr.Formats) {
			display, ref := p.ProfileFormat(pr, name)
			if display == "" {
				display = name
			}
			id, def := identity(t, display, ref)
			ids[name] = id
			row := ScoreRow{Format: name, TrashID: id, Score: pr.Formats[name], Definition: def}
			if def == DefinitionOurs {
				row.Group = groupOurs
			} else if g := groups[fl][id]; g != "" {
				row.Group = g
			} else {
				row.Group = groupOther
			}
			if t != nil && id != "" {
				if f, ok := t.Format(id); ok {
					if score, ok := format.TrashScore(f, scoreSet); ok {
						row.Trash = &score
					}
				}
			}
			row.State = scoreState(row, s.Binding != "", state.Scores, held[key+"\x00"+name])
			s.Scores = append(s.Scores, row)
			fs := addFormat(fl, display, scopeOf(pr, ref), ref, 0)
			fs.ScoredBy = append(fs.ScoredBy, FormatUse{Profile: key, Score: row.Score, State: row.State})
		}
		if t != nil && pr.FromArr == "" {
			if s.Origin == nil {
				if guess := guessOrigin(t, p, pr, in.Formats); guess != nil {
					s.Origin, s.Guessed = guess, true
				}
			}
			if s.Origin != nil && s.Origin.Flavour == fl {
				s.Diff = originDiff(t, p, pr, *s.Origin, ids, in.Formats)
			}
		}
		out.Profiles = append(out.Profiles, s)
	}
	for _, k := range order {
		f := formats[k]
		sort.Slice(f.ScoredBy, func(i, j int) bool { return f.ScoredBy[i].Profile < f.ScoredBy[j].Profile })
		out.Formats = append(out.Formats, *f)
	}
	sort.SliceStable(out.Formats, func(i, j int) bool {
		a, b := out.Formats[i], out.Formats[j]
		if (a.Group == groupOurs) != (b.Group == groupOurs) {
			return b.Group == groupOurs
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Flavour < b.Flavour
	})
	return out
}

func scopeOf(pr *policy.Profile, ref *policy.FormatRef) string {
	if pr.FormatSet == "" || (ref != nil && ref.Ref != "") {
		return "global"
	}
	return pr.FormatSet
}

// scoreState is a score's state: held while it waits in a review; for a bound profile, follows while it equals
// TRaSH's last score, pinned once it differs or was kept, and "differs" until the first run asks about it.
func scoreState(row ScoreRow, bound bool, remembered map[string]TrashScoreState, isHeld bool) string {
	switch {
	case row.Definition == DefinitionOurs:
		return StateOurs
	case isHeld:
		return StateHeld
	case row.Definition == DefinitionFrozen:
		return StateUnlinked
	case !bound:
		return StateUnbound
	case row.Trash == nil:
		return StateNoScore
	}
	if st, ok := remembered[row.Format]; ok {
		if !st.Pinned && st.Trash == row.Score {
			return StateFollows
		}
		return StatePinned
	}
	if row.Trash != nil && *row.Trash == row.Score {
		return StateFollows
	}
	return StateDiffers
}

// originDiff compares profile pr with what origin builds today. ids maps each of pr's score keys to its TRaSH
// identity (empty for ours).
func originDiff(t *format.Trash, p *policy.Policy, pr *policy.Profile, origin ProfileOrigin, ids map[string]string, globals []CustomFormat) *OriginDiff {
	build, err := BuildPreset(t, origin.Preset, origin.Modifiers, globals)
	if err != nil {
		return &OriginDiff{Preset: origin.Name, Summary: []string{"The preset is no longer in this TRaSH tree, or its modifiers no longer apply."},
			QualitiesAdded: []string{}, QualitiesRemoved: []string{}, Settings: []FieldChange{}, FormatsAdded: []ScoreDiff{}, FormatsMissing: []ScoreDiff{}, ScoresChanged: []ScoreDiff{}}
	}
	base := build.Profile
	d := &OriginDiff{Preset: presetLabel(origin), QualitiesAdded: []string{}, QualitiesRemoved: []string{}, Settings: []FieldChange{},
		FormatsAdded: []ScoreDiff{}, FormatsMissing: []ScoreDiff{}, ScoresChanged: []ScoreDiff{}, Summary: []string{}}
	ours, theirs := members(pr.Qualities), members(base.Qualities)
	for _, q := range pr.Qualities {
		if !subset(members([]policy.QualityItem{q}), theirs) {
			d.QualitiesAdded = append(d.QualitiesAdded, q.Name)
		}
	}
	for _, q := range base.Qualities {
		if !subset(members([]policy.QualityItem{q}), ours) {
			d.QualitiesRemoved = append(d.QualitiesRemoved, q.Name)
		}
	}
	if !sameCutoff(pr, &base) {
		d.Cutoff = &ValueChange{Ours: pr.Cutoff, Trash: base.Cutoff}
	}
	field := func(name, a, b string) {
		if a != b {
			d.Settings = append(d.Settings, FieldChange{Field: name, Ours: a, Trash: b})
		}
	}
	field("min_score", fmt.Sprint(pr.MinScore), fmt.Sprint(base.MinScore))
	field("cutoff_score", fmt.Sprint(pr.CutoffScore), fmt.Sprint(base.CutoffScore))
	field("min_upgrade_step", fmt.Sprint(intOr(pr.MinUpgradeStep, 1)), fmt.Sprint(intOr(base.MinUpgradeStep, 1)))
	field("upgrades", fmt.Sprint(pr.Upgrades == nil || *pr.Upgrades), fmt.Sprint(base.Upgrades == nil || *base.Upgrades))
	if pr.Language != "" || base.Language != "original" {
		field("language", orDefault(pr.Language, "any"), orDefault(base.Language, "any"))
	}
	// Scores by TRaSH identity. A format of ours has none and is always an addition.
	trashScore := map[string]int{}
	trashName := map[string]string{}
	for name, score := range base.Formats {
		trashScore[build.TrashIDs[name]] = score
		trashName[build.TrashIDs[name]] = name
	}
	seen := map[string]bool{}
	for _, key := range sortedStringKeys(pr.Formats) {
		score := pr.Formats[key]
		id := ids[key]
		if id != "" {
			seen[id] = true
		}
		if ts, ok := trashScore[id]; ok && id != "" {
			if ts != score {
				d.ScoresChanged = append(d.ScoresChanged, ScoreDiff{Format: key, TrashID: id, Ours: ptr(score), Trash: ptr(ts)})
			}
			continue
		}
		d.FormatsAdded = append(d.FormatsAdded, ScoreDiff{Format: key, TrashID: id, Ours: ptr(score)})
	}
	for _, name := range sortedStringKeys(base.Formats) {
		id := build.TrashIDs[name]
		if !seen[id] {
			d.FormatsMissing = append(d.FormatsMissing, ScoreDiff{Format: name, TrashID: id, Trash: ptr(base.Formats[name])})
		}
	}
	if len(d.QualitiesAdded) > 0 {
		d.Summary = append(d.Summary, "+ "+strings.Join(d.QualitiesAdded, ", "))
	}
	if len(d.QualitiesRemoved) > 0 {
		d.Summary = append(d.Summary, "− "+strings.Join(d.QualitiesRemoved, ", "))
	}
	if d.Cutoff != nil {
		d.Summary = append(d.Summary, fmt.Sprintf("cutoff %s (TRaSH %s)", d.Cutoff.Ours, d.Cutoff.Trash))
	}
	for _, c := range d.Settings {
		d.Summary = append(d.Summary, fmt.Sprintf("%s %s (TRaSH %s)", strings.ReplaceAll(c.Field, "_", " "), c.Ours, c.Trash))
	}
	// The biggest score edits first: they say most about the profile.
	changed := slices.Clone(d.ScoresChanged)
	sort.SliceStable(changed, func(i, j int) bool {
		return abs(*changed[i].Ours-*changed[i].Trash) > abs(*changed[j].Ours-*changed[j].Trash)
	})
	for _, c := range changed {
		d.Summary = append(d.Summary, fmt.Sprintf("%s %s (TRaSH %d)", c.Format, signed(*c.Ours), *c.Trash))
	}
	added := []ScoreDiff{}
	for _, a := range d.FormatsAdded {
		if *a.Ours != 0 {
			added = append(added, a)
		}
	}
	sort.SliceStable(added, func(i, j int) bool { return abs(*added[i].Ours) > abs(*added[j].Ours) })
	for _, a := range added {
		label := "not in the preset"
		if a.TrashID == "" {
			label = "ours"
		}
		d.Summary = append(d.Summary, fmt.Sprintf("%s %s (%s)", a.Format, signed(*a.Ours), label))
	}
	missing := 0
	for _, m := range d.FormatsMissing {
		if *m.Trash != 0 {
			missing++
		}
	}
	if missing > 0 {
		d.Summary = append(d.Summary, fmt.Sprintf("%d of the preset's scored formats not scored", missing))
	}
	d.Same = len(d.Summary) == 0
	if len(d.Summary) > summaryLines {
		more := len(d.Summary) - summaryLines + 1
		d.Summary = append(d.Summary[:summaryLines-1:summaryLines-1], fmt.Sprintf("and %d more edits", more))
	}
	return d
}

func presetLabel(o ProfileOrigin) string {
	out := o.Name
	for _, m := range o.Modifiers {
		out += " + " + ModifierLabel(m)
	}
	return out
}

func sameCutoff(a, b *policy.Profile) bool {
	if a.Cutoff == b.Cutoff {
		return true
	}
	find := func(pr *policy.Profile) []string {
		for _, q := range pr.Qualities {
			if q.Name == pr.Cutoff {
				return members([]policy.QualityItem{q})
			}
		}
		return []string{pr.Cutoff}
	}
	x, y := find(a), find(b)
	return subset(x, y) && subset(y, x)
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func intOr(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func ptr(v int) *int { return &v }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func signed(v int) string {
	if v > 0 {
		return fmt.Sprintf("+%d", v)
	}
	return fmt.Sprint(v)
}

// guessOrigin finds the preset, with modifiers, closest to a profile that has no stored origin: mostly by the TRaSH
// formats both score, then by the qualities they allow and their best quality. Nil when nothing comes close.
func guessOrigin(t *format.Trash, p *policy.Policy, pr *policy.Profile, globals []CustomFormat) *ProfileOrigin {
	ours := map[string]bool{}
	for key := range pr.Formats {
		display, ref := p.ProfileFormat(pr, key)
		if display == "" {
			display = key
		}
		if id, _ := identity(t, display, ref); id != "" {
			ours[id] = true
		}
	}
	quals := members(pr.Qualities)
	var best *ProfileOrigin
	bestScore := 0.0
	ids := make([]string, 0, len(t.Profiles))
	for id := range t.Profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		tp := t.Profiles[id]
		mods := []string{}
		for _, m := range presetModifiers {
			if applies(t, tp, m) {
				mods = append(mods, m)
			}
		}
		for mask := 0; mask < 1<<len(mods); mask++ {
			chosen := []string{}
			for i, m := range mods {
				if mask&(1<<i) != 0 {
					chosen = append(chosen, m)
				}
			}
			build, err := BuildPreset(t, id, chosen, globals)
			if err != nil {
				continue
			}
			theirs := map[string]bool{}
			for _, tid := range build.TrashIDs {
				theirs[tid] = true
			}
			score := 2*jaccard(ours, theirs) + jaccard(setOf(quals), setOf(members(build.Profile.Qualities)))
			if len(pr.Qualities) > 0 && len(build.Profile.Qualities) > 0 && subset(members(pr.Qualities[:1]), members(build.Profile.Qualities[:1])) {
				score += 0.5
			}
			score -= 0.01 * float64(len(chosen))
			if best == nil || score > bestScore+1e-9 {
				origin := build.Origin
				best, bestScore = &origin, score
			}
		}
	}
	if bestScore < 1 {
		return nil
	}
	return best
}

func setOf(list []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range list {
		out[v] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a)+len(b) == 0 {
		return 0
	}
	both := 0
	for k := range a {
		if b[k] {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}
