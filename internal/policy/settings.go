package policy

import (
	"bytes"
	"errors"
	"io"

	"go.yaml.in/yaml/v3"
)

func unmanagedYAML(b []byte, quality bool) ([]byte, error) {
	var doc yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return b, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one YAML document; use one document per file")
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return b, nil
	}
	root := doc.Content[0]
	filtered := []*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "indexers", "providers", "budgets":
			continue
		case "profiles", "formats", "format_sets":
			if quality {
				continue
			}
		case "versions":
			if quality && value.Kind == yaml.MappingNode {
				for j := 1; j < len(value.Content); j += 2 {
					v := value.Content[j]
					if v.Kind != yaml.MappingNode {
						continue
					}
					fields := []*yaml.Node{}
					for k := 0; k < len(v.Content); k += 2 {
						if v.Content[k].Value != "naming" {
							fields = append(fields, v.Content[k], v.Content[k+1])
						}
					}
					v.Content = fields
				}
			}
		}
		filtered = append(filtered, key, value)
	}
	root.Content = filtered
	return yaml.Marshal(&doc)
}
