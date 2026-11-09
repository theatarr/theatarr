package format

// Import of the arr API's GET /api/v3/customformat and /api/v3/qualityprofile bodies (Radarr.Api.V3 and
// Sonarr.Api.V3 CustomFormatResource, CustomFormatSpecificationSchema, QualityProfileResource).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// SpecJSON is one specification in arr API or TRaSH shape. Fields is a list of {name, value} in the API and an
// object in TRaSH JSON; both are read.
type SpecJSON struct {
	Name           string          `json:"name"`
	Implementation string          `json:"implementation"`
	Negate         bool            `json:"negate"`
	Required       bool            `json:"required"`
	Fields         json.RawMessage `json:"fields"`
}

// FormatJSON is a custom format in arr API shape (TRaSH adds trash_id and trash_scores).
type FormatJSON struct {
	ID                              int            `json:"id"`
	Name                            string         `json:"name"`
	IncludeCustomFormatWhenRenaming bool           `json:"includeCustomFormatWhenRenaming"`
	Specifications                  []SpecJSON     `json:"specifications"`
	TrashID                         string         `json:"trash_id,omitempty"`
	TrashScores                     map[string]int `json:"trash_scores,omitempty"`
}

// ParseArrFormats reads a GET /api/v3/customformat body in flavour fl.
func ParseArrFormats(fl Flavour, body []byte) ([]*Format, error) {
	var raw []FormatJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("customformat: %w", err)
	}
	out := make([]*Format, 0, len(raw))
	for i := range raw {
		f, err := raw[i].Build(fl)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// Build turns one format's JSON into a Format in flavour fl. An implementation the flavour does not have (Edition
// in Sonarr) is an error, as the arr would refuse it.
func (j *FormatJSON) Build(fl Flavour) (*Format, error) {
	if !fl.Valid() {
		return nil, fmt.Errorf("format %q: unknown flavour %q", j.Name, fl)
	}
	f := &Format{
		ID: j.ID, Name: j.Name, Flavour: fl, IncludeWhenRenaming: j.IncludeCustomFormatWhenRenaming,
		TrashID: j.TrashID, TrashScores: j.TrashScores,
	}
	for _, s := range j.Specifications {
		spec, err := s.Build(fl)
		if err != nil {
			return nil, fmt.Errorf("format %q: %w", j.Name, err)
		}
		f.Specs = append(f.Specs, spec)
	}
	return f, nil
}

var radarrOnly = map[string]bool{
	"EditionSpecification": true, "QualityModifierSpecification": true, "YearSpecification": true,
}

// Build turns one specification's JSON into a Spec in flavour fl.
func (s *SpecJSON) Build(fl Flavour) (*Spec, error) {
	fields, err := readFields(s.Fields)
	if err != nil {
		return nil, fmt.Errorf("condition %q: %w", s.Name, err)
	}
	if fl == Sonarr && radarrOnly[s.Implementation] || fl == Radarr && s.Implementation == "ReleaseTypeSpecification" {
		return nil, fmt.Errorf("condition %q: %s is not a %s specification", s.Name, s.Implementation, fl)
	}
	var c Condition
	switch s.Implementation {
	case "ReleaseTitleSpecification":
		c, err = NewReleaseTitle(fields.str("value"))
	case "EditionSpecification":
		c, err = NewEdition(fields.str("value"))
	case "ReleaseGroupSpecification":
		c, err = NewReleaseGroup(fields.str("value"))
	case "LanguageSpecification":
		var v int
		if v, err = fields.int("value"); err == nil {
			var ex bool
			ex, err = fields.bool("exceptLanguage")
			c = &Language{Value: v, ExceptLanguage: ex}
		}
	case "IndexerFlagSpecification":
		var v int
		v, err = fields.int("value")
		c = &IndexerFlag{Value: v}
	case "QualityModifierSpecification":
		var v int
		v, err = fields.int("value")
		c = &QualityModifier{Value: v}
	case "ResolutionSpecification":
		var v int
		v, err = fields.int("value")
		c = &Resolution{Value: v}
	case "SourceSpecification":
		var v int
		v, err = fields.int("value")
		c = &Source{Value: v}
	case "ReleaseTypeSpecification":
		var v int
		v, err = fields.int("value")
		c = &ReleaseType{Value: v}
	case "SizeSpecification":
		var lo, hi float64
		if lo, err = fields.float("min"); err == nil {
			hi, err = fields.float("max")
		}
		c = &Size{Min: lo, Max: hi}
	case "YearSpecification":
		var lo, hi int
		if lo, err = fields.int("min"); err == nil {
			hi, err = fields.int("max")
		}
		c = &Year{Min: lo, Max: hi}
	case "SeaDexSpecification": // ours, both flavours (seadex.go)
		c, err = NewSeaDex(fields.str("value"))
	default:
		return nil, fmt.Errorf("condition %q: unknown implementation %q", s.Name, s.Implementation)
	}
	if err != nil {
		return nil, fmt.Errorf("condition %q: %w", s.Name, err)
	}
	return &Spec{Name: s.Name, Negate: s.Negate, Required: s.Required, Condition: c}, nil
}

// fieldSet holds a specification's field values by name. A missing field reads as the zero value, as the arr's
// SchemaBuilder leaves the property at its default.
type fieldSet map[string]json.RawMessage

func readFields(raw json.RawMessage) (fieldSet, error) {
	fs := fieldSet{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return fs, nil
	}
	if raw[0] == '{' {
		if err := json.Unmarshal(raw, (*map[string]json.RawMessage)(&fs)); err != nil {
			return nil, fmt.Errorf("fields: %w", err)
		}
		return fs, nil
	}
	var list []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("fields: %w", err)
	}
	for _, f := range list {
		fs[f.Name] = f.Value
	}
	return fs, nil
}

func (fs fieldSet) present(name string) (json.RawMessage, bool) {
	v, ok := fs[name]
	if !ok || len(v) == 0 || string(v) == "null" {
		return nil, false
	}
	return v, true
}

func (fs fieldSet) str(name string) string {
	v, ok := fs.present(name)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}

func (fs fieldSet) float(name string) (float64, error) {
	v, ok := fs.present(name)
	if !ok {
		return 0, nil
	}
	var n float64
	if err := json.Unmarshal(v, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("field %s: not a number: %s", name, v)
}

func (fs fieldSet) int(name string) (int, error) {
	n, err := fs.float(name)
	if err != nil {
		return 0, err
	}
	if n != float64(int(n)) {
		return 0, fmt.Errorf("field %s: not an integer: %v", name, n)
	}
	return int(n), nil
}

func (fs fieldSet) bool(name string) (bool, error) {
	v, ok := fs.present(name)
	if !ok {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false, fmt.Errorf("field %s: not a bool: %s", name, v)
	}
	return b, nil
}

// ProfileJSON is the format part of a GET /api/v3/qualityprofile item.
type ProfileJSON struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	FormatItems []struct {
		Format int    `json:"format"`
		Name   string `json:"name"`
		Score  int    `json:"score"`
	} `json:"formatItems"`
	MinFormatScore        int `json:"minFormatScore"`
	CutoffFormatScore     int `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int `json:"minUpgradeFormatScore"`
}

// ParseArrProfiles reads a GET /api/v3/qualityprofile body. Qualities, cutoff and upgrade rules are not read here.
func ParseArrProfiles(fl Flavour, body []byte) ([]*Profile, error) {
	var raw []ProfileJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("qualityprofile: %w", err)
	}
	out := make([]*Profile, 0, len(raw))
	for _, r := range raw {
		p := &Profile{
			ID: r.ID, Name: r.Name, Flavour: fl, MinFormatScore: r.MinFormatScore,
			CutoffFormatScore: r.CutoffFormatScore, MinUpgradeFormatScore: r.MinUpgradeFormatScore,
		}
		for _, it := range r.FormatItems {
			p.FormatItems = append(p.FormatItems, FormatItem{Format: it.Format, Name: it.Name, Score: it.Score})
		}
		out = append(out, p)
	}
	return out, nil
}
