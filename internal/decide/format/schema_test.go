package format

import "testing"

func TestSpecificationSchemaFlavourEnums(t *testing.T) {
	all := SpecificationSchemas()
	if len(all) != 12 {
		t.Fatalf("schemas=%d", len(all))
	}
	for _, schema := range all {
		for _, field := range schema.Fields {
			for flavour, options := range field.Options {
				for _, option := range options {
					if schema.Implementation == "LanguageSpecification" {
						got, ok := Flavour(flavour).LanguageID(option.Name)
						if !ok || got != option.Value {
							t.Fatal("language schema differs from matcher")
						}
					}
					if schema.Implementation == "SourceSpecification" {
						got, err := Flavour(flavour).SourceValue(option.Name)
						if err != nil || got != option.Value {
							t.Fatal("source schema differs from matcher")
						}
					}
				}
			}
		}
	}
}
