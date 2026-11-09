package format

// SpecificationSchema exposes the field types and arr enum values used by SpecJSON.Build.
type SpecificationSchema struct {
	Implementation string               `json:"implementation"`
	Flavours       []string             `json:"flavours"`
	Fields         []SpecificationField `json:"fields"`
}
type SpecificationField struct {
	Name    string                           `json:"name"`
	Type    string                           `json:"type"`
	Options map[string][]SpecificationOption `json:"options,omitempty"`
}
type SpecificationOption struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func SpecificationSchemas() []SpecificationSchema {
	out := []SpecificationSchema{}
	for _, name := range []string{"ReleaseTitle", "Edition", "ReleaseGroup", "Language", "IndexerFlag", "QualityModifier", "Resolution", "Source", "ReleaseType", "Size", "Year", "SeaDex"} {
		fields := []SpecificationField{{Name: "value", Type: "integer"}}
		flavours := []string{"radarr", "sonarr"}
		switch name {
		case "ReleaseTitle", "Edition", "ReleaseGroup", "SeaDex":
			fields = []SpecificationField{{Name: "value", Type: "string"}}
		case "Language":
			fields = append(fields, SpecificationField{Name: "exceptLanguage", Type: "boolean"})
		case "Size":
			fields = []SpecificationField{{Name: "min", Type: "number"}, {Name: "max", Type: "number"}}
		case "Year":
			fields = []SpecificationField{{Name: "min", Type: "integer"}, {Name: "max", Type: "integer"}}
		}
		switch name {
		case "Edition", "QualityModifier", "Year":
			flavours = []string{"radarr"}
		case "ReleaseType":
			flavours = []string{"sonarr"}
		}
		for _, flavour := range flavours {
			f := Flavour(flavour)
			var table enumTable
			switch name {
			case "Language":
				table = f.languages()
			case "IndexerFlag":
				table = f.indexerFlags()
			case "QualityModifier":
				table = radarrModifiers
			case "Resolution":
				table = resolutions
			case "Source":
				table = f.sources()
			case "ReleaseType":
				table = sonarrReleaseTypes
			}
			if len(table.names) > 0 {
				if fields[0].Options == nil {
					fields[0].Options = map[string][]SpecificationOption{}
				}
				for i, name := range table.names {
					fields[0].Options[flavour] = append(fields[0].Options[flavour], SpecificationOption{Name: name, Value: table.values[i]})
				}
			}
		}
		out = append(out, SpecificationSchema{Implementation: name + "Specification", Flavours: flavours, Fields: fields})
	}
	return out
}
