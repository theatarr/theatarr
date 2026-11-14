package web

import (
	"maps"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestDistinctLabels(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		in   []copyName
		want map[string]string
	}{
		{"no collision", []copyName{{"tv-2160p", "4K"}, {"tv-web", "1080p WEB"}},
			map[string]string{"tv-2160p": "4K", "tv-web": "1080p WEB"}},
		{"collision", []copyName{{"tv-2160p", "4K"}, {"tv-2160p-theatarr", "4K"}, {"tv-web", "WEB"}},
			map[string]string{"tv-2160p": "4K", "tv-2160p-theatarr": "4K (2)", "tv-web": "WEB"}},
		{"two collide", []copyName{{"anime-tv", "Anime"}, {"anime-tv-theatarr", "Anime"}},
			map[string]string{"anime-tv": "Anime", "anime-tv-theatarr": "Anime (2)"}},
		{"three", []copyName{{"a", "4K"}, {"b", "4K"}, {"c", "4K"}},
			map[string]string{"a": "4K", "b": "4K (2)", "c": "4K (3)"}},
		{"once per key", []copyName{{"a", "4K"}, {"a", "4K"}, {"b", ""}, {"", "x"}},
			map[string]string{"a": "4K", "b": "b"}},
	} {
		if got := distinctLabels(c.in); !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// twinPolicy adds tv-2160p-theatarr, a second "4K" series version next to tv-2160p.
func twinPolicy(p *policy.Policy) {
	v := *p.Versions["tv-2160p"]
	p.Versions["tv-2160p-theatarr"] = &v
}

// TestSameNamedCopiesServerWords: server words and labels that name a copy tell same-named copies apart by number
// (libraryNames), never by key.
func TestSameNamedCopiesServerWords(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, twinPolicy)
	now := addNow.UnixMilli()
	// The Show's second 4K copy.
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(11, 10, 'tv-2160p-theatarr', 1, 'wanted', ?, ?)`, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(11, 101, 1, 'wanted', ?), (11, 102, 1, 'wanted', ?)`, now, now)
	if got := e.web.versionLabel("tv-2160p-theatarr"); got != "4K (2)" {
		t.Fatalf("the twin's label: %q", got)
	}
	want := map[string]string{"tv-2160p": "4K", "tv-2160p-theatarr": "4K (2)"}

	f := false
	var pre BulkMonitorResult
	e.call("POST", APIBase+"/titles/monitor", BulkMonitorRequest{TitleIDs: []int64{10}, Monitored: &f, DryRun: true}, 200, &pre)
	got := map[string]string{}
	for _, it := range pre.Items {
		got[it.Version] = it.Label
	}
	if !maps.Equal(got, want) {
		t.Errorf("bulk items: %v", got)
	}
	// Targeting one copy still names it among the selected titles' copies.
	var one BulkMonitorResult
	e.call("POST", APIBase+"/titles/monitor", BulkMonitorRequest{TitleIDs: []int64{10}, Versions: []string{"tv-2160p-theatarr"},
		Monitored: &f, DryRun: true}, 200, &one)
	if len(one.Items) != 1 || one.Items[0].Label != "4K (2)" {
		t.Errorf("bulk one copy: %+v", one.Items)
	}

	var scope RemovalScope
	e.call("GET", APIBase+"/titles/10/removal?versions=tv-2160p-theatarr", nil, 200, &scope)
	if len(scope.Copies) != 1 || scope.Copies[0].Label != "4K (2)" {
		t.Errorf("removal scope: %+v", scope.Copies)
	}

	var ep EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/102", nil, 200, &ep)
	got = map[string]string{}
	for _, v := range ep.Versions {
		got[v.Key] = v.Label
	}
	if !maps.Equal(got, want) {
		t.Errorf("episode overview: %v", got)
	}

	// Request lists and cross-title notes: the same names.
	if got := e.web.keyLabels([]string{"tv-2160p", "tv-2160p-theatarr", "movie-remux"}); !maps.Equal(got,
		map[string]string{"tv-2160p": "4K", "tv-2160p-theatarr": "4K (2)", "movie-remux": "4K Remux"}) {
		t.Errorf("key labels: %v", got)
	}
	choices := map[string]string{}
	for _, c := range e.web.versionChoices() {
		choices[c.Key] = c.Label
	}
	if choices["tv-2160p"] != "4K" || choices["tv-2160p-theatarr"] != "4K (2)" {
		t.Errorf("version choices: %v", choices)
	}
}
