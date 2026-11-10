package web

import (
	"testing"
)

// Errors a user can reach name a library, not a version (the settings key and API field keep that name).
func TestUnknownLibraryErrors(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	for _, tc := range []struct {
		name, method, path string
		body               any
		want               string
	}{
		{"add", "POST", APIBase + "/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "versions": []string{"nope"}},
			`unknown library "nope"`},
		{"search", "POST", APIBase + "/titles/1/search", map[string]any{"versions": []string{"nope"}},
			`the title has no monitored library "nope"`},
		{"releases", "GET", APIBase + "/titles/1/releases?version=nope", nil, `unknown library "nope" for this title`},
	} {
		var got apiError
		e.call(tc.method, tc.path, tc.body, 400, &got)
		if got.Error != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got.Error, tc.want)
		}
	}
}
