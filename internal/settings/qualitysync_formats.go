package settings

import (
	"encoding/json"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

func syncExistingFormats(records *Records, trash *format.Trash, flavour string, preview *QualityPreview) map[string]bool {
	existing := map[string]bool{}
	update := func(key string, ref *policy.FormatRef, raw json.RawMessage) json.RawMessage {
		id := customTrashID(CustomFormat{Config: *ref, Specification: raw})
		if id == "" || (ref.Flavour != "" && ref.Flavour != flavour) {
			return raw
		}
		existing[id] = true
		upstream, ok := trash.Format(id)
		var old format.FormatJSON
		_ = json.Unmarshal(raw, &old)
		if !ok || (old.Name != "" && trashBaseName(old.Name) != upstream.Name) {
			reason := "removed upstream"
			if ok {
				reason = "renamed upstream to " + upstream.Name
			}
			preview.Retired = append(preview.Retired, FormatChange{Key: key, TrashID: id, Reason: reason})
			return raw
		}
		incoming := keepLocalName(trash.Raw(id), old.Name, upstream.Name)
		if !sameFormatSpec(raw, incoming) {
			preview.Changed = append(preview.Changed, FormatChange{Key: key, TrashID: id})
		}
		return incoming
	}
	for i := range records.Formats {
		r := &records.Formats[i]
		r.Specification = update(r.Key, &r.Config, r.Specification)
	}
	for i := range records.FormatSets {
		set := &records.FormatSets[i]
		if set.Config.Flavour != flavour {
			continue
		}
		if set.Specifications == nil {
			set.Specifications = map[string]json.RawMessage{}
		}
		for key, ref := range set.Config.Formats {
			if ref != nil && ref.Ref == "" {
				set.Specifications[key] = update(set.Key+"/"+key, ref, set.Specifications[key])
			}
		}
	}
	return existing
}

// notifiarrSuffix marks the formats Notifiarr's TRaSH sync made in an arr: "<TRaSH name> - Notifiarr".
const notifiarrSuffix = " - Notifiarr"

// trashBaseName is the TRaSH name an arr format name stands for: the name itself, or without Notifiarr's suffix.
func trashBaseName(name string) string {
	return strings.TrimSuffix(name, notifiarrSuffix)
}

// keepLocalName returns TRaSH's JSON of a linked format under the format's own name, when that differs from
// TRaSH's only by Notifiarr's suffix: the arr's display name stays, only the definition follows TRaSH.
func keepLocalName(raw []byte, local, upstream string) json.RawMessage {
	if local == "" || local == upstream {
		return append(json.RawMessage(nil), raw...)
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil {
		return append(json.RawMessage(nil), raw...)
	}
	f["name"], _ = json.Marshal(local)
	out, err := json.Marshal(f)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return out
}

func scopedTrashIDs(records Records, setKey string) map[string]string {
	global := map[string]CustomFormat{}
	for _, r := range records.Formats {
		global[r.Key] = r
	}
	out := map[string]string{}
	if setKey == "" {
		for key, r := range global {
			out[key] = customTrashID(r)
		}
		return out
	}
	for _, set := range records.FormatSets {
		if set.Key == setKey {
			for key, ref := range set.Config.Formats {
				if ref == nil {
					continue
				}
				if ref.Ref != "" {
					out[key] = customTrashID(global[ref.Ref])
				} else {
					out[key] = customTrashID(CustomFormat{Config: *ref, Specification: set.Specifications[key]})
				}
			}
		}
	}
	return out
}
