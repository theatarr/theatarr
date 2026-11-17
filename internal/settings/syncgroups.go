package settings

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/policy"
)

// The sync groups (library.sync_groups) live in the library section and name libraries, which are version records:
// a save that changes the libraries keeps the groups in step (PruneSyncGroups), and the library editor sets one
// library's group (SetSyncGroup). docs/design/ui.md "Keep in sync".

const syncGroupsField = "sync_groups"

// syncGroups reads the stored groups and the library section's index in records (-1: no section).
func syncGroups(records *Records) (groups [][]string, index int, err error) {
	index = slices.IndexFunc(records.Sections, func(s Section) bool { return s.Key == "library" })
	if index < 0 || nullSection(records.Sections[index].Config) {
		return nil, index, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(records.Sections[index].Config, &fields); err != nil {
		return nil, index, fmt.Errorf("settings: library section: %w", err)
	}
	if raw, ok := fields[syncGroupsField]; ok && !nullSection(raw) {
		if err := json.Unmarshal(raw, &groups); err != nil {
			return nil, index, fmt.Errorf("settings: library.sync_groups: %w", err)
		}
	}
	return groups, index, nil
}

// setSyncGroups stores groups in the library section, leaving its other fields as they are; none leaves the field
// out. A database without a library section gets one.
func setSyncGroups(records *Records, index int, groups [][]string) error {
	fields := map[string]json.RawMessage{}
	if index >= 0 && !nullSection(records.Sections[index].Config) {
		if err := json.Unmarshal(records.Sections[index].Config, &fields); err != nil {
			return fmt.Errorf("settings: library section: %w", err)
		}
	}
	if len(groups) == 0 {
		delete(fields, syncGroupsField)
	} else {
		raw, err := json.Marshal(groups)
		if err != nil {
			return err
		}
		fields[syncGroupsField] = raw
	}
	config, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if index < 0 {
		records.Sections = append(records.Sections, Section{Record: Record{Key: "library"}, Config: config})
		return nil
	}
	sections := slices.Clone(records.Sections)
	sections[index].Config = config
	records.Sections = sections
	return nil
}

// syncRule is what every library of a group shares: its kind and its anime rule.
type syncRule struct {
	kind  policy.Kind
	anime string
}

func ruleOf(vp *policy.VersionPolicy) syncRule {
	r := syncRule{kind: vp.Kind, anime: "both"}
	if vp.Anime != nil {
		r.anime = fmt.Sprint(*vp.Anime)
	}
	return r
}

// PruneSyncGroups keeps library.sync_groups in step with records' libraries, for a save that deletes a library,
// changes one's kind or anime setting, or imports the libraries: a library no longer in the records leaves its
// group, one whose kind or anime rule no longer matches the rest of its group leaves it (the rule most of the group
// shares stays; on a tie, the first library's), and a group left with one library is dropped.
func PruneSyncGroups(records *Records) error {
	groups, index, err := syncGroups(records)
	if err != nil || len(groups) == 0 {
		return err
	}
	versions := map[string]*policy.VersionPolicy{}
	for i := range records.Versions {
		versions[records.Versions[i].Key] = &records.Versions[i].Config
	}
	var out [][]string
	changed := false
	for _, g := range groups {
		var known []string
		count := map[syncRule]int{}
		for _, k := range g {
			if vp := versions[k]; vp != nil {
				known = append(known, k)
				count[ruleOf(vp)]++
			}
		}
		var keep []string
		if len(known) > 0 {
			best := ruleOf(versions[known[0]])
			for _, k := range known {
				if r := ruleOf(versions[k]); count[r] > count[best] {
					best = r
				}
			}
			for _, k := range known {
				if ruleOf(versions[k]) == best {
					keep = append(keep, k)
				}
			}
		}
		if len(keep) != len(g) || len(keep) < 2 {
			changed = true
		}
		if len(keep) >= 2 {
			out = append(out, keep)
		}
	}
	if !changed {
		return nil
	}
	return setSyncGroups(records, index, out)
}

// SetSyncGroup makes key's sync group exactly key and with (none: key leaves its group). Libraries of with leave the
// groups they were in, and a group left with one library is dropped. Each library of with must exist in records, and
// be of key's kind and anime rule; the check of the whole is policy validation's.
func SetSyncGroup(records *Records, key string, with []string) error {
	groups, index, err := syncGroups(records)
	if err != nil {
		return err
	}
	versions := map[string]*policy.VersionPolicy{}
	for i := range records.Versions {
		versions[records.Versions[i].Key] = &records.Versions[i].Config
	}
	own := versions[key]
	if own == nil {
		return fmt.Errorf("settings: no library %q", key)
	}
	members := []string{key}
	for _, k := range with {
		vp := versions[k]
		switch {
		case k == key || slices.Contains(members, k):
			continue
		case vp == nil:
			return &policy.Errors{List: []policy.FieldError{{Path: "sync_with", Msg: fmt.Sprintf("unknown library %q", k)}}}
		case ruleOf(vp) != ruleOf(own):
			return &policy.Errors{List: []policy.FieldError{{Path: "sync_with",
				Msg: fmt.Sprintf("%q is not of this library's kind and anime setting", k)}}}
		}
		members = append(members, k)
	}
	var out [][]string
	for _, g := range groups {
		rest := slices.DeleteFunc(slices.Clone(g), func(k string) bool { return slices.Contains(members, k) })
		if len(rest) >= 2 {
			out = append(out, rest)
		}
	}
	if len(members) >= 2 {
		out = append(out, members)
	}
	return setSyncGroups(records, index, out)
}
