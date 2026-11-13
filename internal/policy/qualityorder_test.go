package policy

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestQualityOrderValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*Profile)
		want string
	}{
		{"complete", func(*Profile) {}, ""},
		{"missing quality", func(p *Profile) { p.QualityOrder = p.QualityOrder[:len(p.QualityOrder)-1] }, "missing quality"},
		{"missing allowed", func(p *Profile) { p.QualityOrder = p.QualityOrder[1:] }, "allowed items must match"},
		{"duplicate", func(p *Profile) { p.QualityOrder = append(p.QualityOrder, p.QualityOrder[0]) }, "listed twice"},
		{"unknown", func(p *Profile) { p.QualityOrder = append(p.QualityOrder, QualityItem{Name: "imaginary"}) }, "unknown quality"},
		{"wrong flavour", func(p *Profile) { p.QualityOrder = append(p.QualityOrder, QualityItem{Name: "Bluray-2160p Remux"}) }, "not a movie quality"},
		{"empty group", func(p *Profile) { p.QualityOrder = append(p.QualityOrder, QualityItem{Name: "empty", Group: true}) }, "has no qualities"},
		{"changed group", func(p *Profile) {
			p.QualityOrder[0] = QualityItem{Name: "Remux-2160p", Group: true, Qualities: []string{"Remux-2160p"}}
		}, "allowed items must match"},
		{"zero preferred", func(p *Profile) { p.Sizes = map[string]SizeLimit{"BR-DISK": {PreferredZero: true}} }, ""},
		{"conflicting preferred", func(p *Profile) { p.Sizes = map[string]SizeLimit{"BR-DISK": {Preferred: 5, PreferredZero: true}} }, "preferred_zero requires"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := &Profile{Qualities: []QualityItem{{Name: "Remux-2160p"}}, Cutoff: "Remux-2160p", QualityOrder: []QualityItem{{Name: "Remux-2160p"}}}
			for _, q := range sortedKeys(radarrQualities) {
				if q != "Remux-2160p" {
					pr.QualityOrder = append(pr.QualityOrder, QualityItem{Name: q})
				}
			}
			tc.edit(pr)
			p, e := &Policy{}, &Errors{}
			p.validateProfile(e, "fixture", pr)
			p.checkProfileFits(e, "fixture", pr, KindMovie)
			err := e.err()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestQualityOrderYAML(t *testing.T) {
	t.Parallel()
	var pr Profile
	if err := yaml.Unmarshal([]byte(`qualities: [WEBDL-2160p]
quality_order:
  - group: Disabled UHD
    qualities: [Remux-2160p, Bluray-2160p]
  - WEBDL-2160p
sizes:
  BR-DISK: {preferred: 0, preferred_zero: true}
`), &pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.QualityOrder) != 2 || !pr.QualityOrder[0].Group || !pr.Sizes["BR-DISK"].PreferredZero {
		t.Fatalf("lost explicit order/zero: %+v", pr)
	}
}
