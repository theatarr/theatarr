package preflight

import (
	"context"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"strings"
	"testing"
)

func TestPreflightNextStepsNameCurrentPages(t *testing.T) {
	o, _ := good(t)
	o.Credentials = settings.Credentials{Lookup: func(string) (string, bool) { return "", false }}
	o.APIKeys = 0
	o.Policy.Library.Versions = nil
	r := Run(context.Background(), o)
	for key, page := range map[string]string{
		"providers/rd (realdebrid)": "Settings › Download clients",
		"indexers/feed (torznab)":   "Settings › Indexers",
		"library/roots movies":      "Settings › Libraries",
		"views/api keys":            "Users & access",
	} {
		if got := items(r)[key].Next; !strings.Contains(got, page) {
			t.Errorf("%s next = %q; want %s", key, got, page)
		}
	}
	o, _ = good(t)
	o.Sources = func(_ context.Context, entity string) []Source {
		key := "feed"
		if entity == "providers" {
			key = "rd"
		}
		return []Source{{Key: key, Status: "failed", Category: "auth"}}
	}
	r = Run(context.Background(), o)
	for key, page := range map[string]string{"providers/rd (realdebrid)": "Settings › Download clients", "indexers/feed (torznab)": "Settings › Indexers"} {
		if got := items(r)[key].Next; !strings.Contains(got, page) {
			t.Errorf("%s next = %q; want %s", key, got, page)
		}
	}
	// A fresh install points at the forms for TMDb and Plex, not at YAML.
	r = Run(context.Background(), Options{Policy: &policy.Policy{}, APIKeys: 0})
	for key, page := range map[string]string{"metadata/tmdb": "Settings › Metadata & artwork", "plex/plex": "Settings › Plex & playback"} {
		if got := items(r)[key].Next; !strings.Contains(got, page) || strings.Contains(got, "library.plex") {
			t.Errorf("%s next = %q; want %s", key, got, page)
		}
	}
}
