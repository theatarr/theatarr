package policy

import (
	"bytes"

	"go.yaml.in/yaml/v3"
)

// Keys removed when privacy became a label only: indexers.K.private_delivery and
// the release_push section (indexers.K.arr_names stays, as a plain alias list). Older policy files and settings still hold them, so
// they are dropped before the strict decode, and Policy.Ignored names each one dropped.
const (
	removedReleasePush = "release_push"
)

var removedIndexerKeys = map[string]bool{"private_delivery": true}

// IgnoredNote is the note a dropped key gets in Policy.Ignored.
const IgnoredNote = "ignored: removed setting (privacy is a label only)"

// Keys removed when every automatic search moved to one schedule (migration 00127 does the same to stored settings):
// router.wanted.enabled, interval and retry_new. enabled: false turned both backlog switches off, so it becomes
// missing: false and cutoff_unmet: off before the keys go.
var removedWantedKeys = map[string]bool{"enabled": true, "interval": true, "retry_new": true}

// wantedIgnoredNote is the note a dropped router.wanted key gets in Policy.Ignored.
const wantedIgnoredNote = "ignored: removed setting (one search schedule)"

// dropRemovedWanted applies that to a router mapping and returns the notes of the keys it dropped.
func dropRemovedWanted(router *yaml.Node) []string {
	var w *yaml.Node
	for i := 0; i+1 < len(router.Content); i += 2 {
		if router.Content[i].Value == "wanted" && router.Content[i+1].Kind == yaml.MappingNode {
			w = router.Content[i+1]
		}
	}
	if w == nil {
		return nil
	}
	var dropped []string
	off := false
	fields := []*yaml.Node{}
	for i := 0; i+1 < len(w.Content); i += 2 {
		name, value := w.Content[i].Value, w.Content[i+1]
		if !removedWantedKeys[name] {
			fields = append(fields, w.Content[i], value)
			continue
		}
		var on bool
		if name == "enabled" && value.Kind == yaml.ScalarNode && value.ShortTag() == "!!bool" && value.Decode(&on) == nil && !on {
			off = true
		}
		dropped = append(dropped, "router.wanted."+name+" "+wantedIgnoredNote)
	}
	if off {
		set := map[string]*yaml.Node{
			"missing":      {Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"},
			"cutoff_unmet": {Kind: yaml.ScalarNode, Tag: "!!str", Value: "off"},
		}
		for i := 0; i+1 < len(fields); i += 2 {
			if v, ok := set[fields[i].Value]; ok {
				fields[i+1] = v
				delete(set, fields[i].Value)
			}
		}
		for _, k := range []string{"missing", "cutoff_unmet"} {
			if v, ok := set[k]; ok {
				fields = append(fields, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, v)
			}
		}
	}
	w.Content = fields
	return dropped
}

// dropRemoved removes the removed keys from one policy file and returns it with the paths it dropped, each with its
// note.
func dropRemoved(b []byte) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&doc); err != nil {
		return b, nil, nil // empty, or broken: the strict decode reports it
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return b, nil, nil
	}
	root := doc.Content[0]
	var dropped []string
	kept := []*yaml.Node{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Value == removedReleasePush {
			dropped = append(dropped, removedReleasePush+" "+IgnoredNote)
			continue
		}
		if key.Value == "router" && value.Kind == yaml.MappingNode {
			dropped = append(dropped, dropRemovedWanted(value)...)
		}
		if key.Value == "indexers" && value.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(value.Content); j += 2 {
				ix := value.Content[j+1]
				if ix.Kind != yaml.MappingNode {
					continue
				}
				fields := []*yaml.Node{}
				for k := 0; k+1 < len(ix.Content); k += 2 {
					if name := ix.Content[k].Value; removedIndexerKeys[name] {
						dropped = append(dropped, "indexers["+value.Content[j].Value+"]."+name+" "+IgnoredNote)
						continue
					}
					fields = append(fields, ix.Content[k], ix.Content[k+1])
				}
				ix.Content = fields
			}
		}
		kept = append(kept, key, value)
	}
	if len(dropped) == 0 {
		return b, nil, nil
	}
	root.Content = kept
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, err
	}
	return out, dropped, nil
}
