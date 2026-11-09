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

// dropRemoved removes the removed keys from one policy file and returns it with the paths it dropped.
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
			dropped = append(dropped, removedReleasePush)
			continue
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
						dropped = append(dropped, "indexers["+value.Content[j].Value+"]."+name)
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
