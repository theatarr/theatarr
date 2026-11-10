package recyclarr

import "testing"

// The recyclarr import reads the configured TRaSH checkout only: the snapshot bundled for the profile presets is
// not a fallback here, so without a checkout the conversion fails as it always has.
func TestConvertNeedsTheCheckout(t *testing.T) {
	t.Parallel()
	cfg := &Config{Instances: []*Instance{{Name: "radarr", Flavour: "radarr"}}}
	if _, err := Convert(cfg, Options{}); err == nil {
		t.Fatal("converted without a TRaSH checkout")
	}
}
