package main

import (
	"strings"
	"testing"
)

func TestCommandsExcludeRetiredTooling(t *testing.T) {
	for _, name := range []string{"parity", "parity-candidates", "rls-parse", "radarr-parse", "profiles", "formats"} {
		if _, ok := commands[name]; ok {
			t.Errorf("retired command %q is still registered", name)
		}
	}
	for _, name := range []string{"sonarr-parse", "decide", "policy", "shadow"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("supported diagnostic command %q is missing", name)
		}
	}
}

func TestUsageGroupsEveryCommand(t *testing.T) {
	grouped := map[string]bool{}
	for _, g := range usageGroups {
		for _, n := range g.commands {
			if grouped[n] {
				t.Errorf("command %q is in two usage groups", n)
			}
			grouped[n] = true
			if _, ok := commands[n]; !ok {
				t.Errorf("usage group %q lists unknown command %q", g.title, n)
			}
		}
	}
	var out strings.Builder
	writeUsage(&out)
	text := out.String()
	for n, c := range commands {
		listed := strings.Contains(text, "\n  "+n+" ")
		switch {
		case c.hidden && listed:
			t.Errorf("hidden command %q is in the usage", n)
		case !c.hidden && !grouped[n]:
			t.Errorf("command %q has no usage group", n)
		case !c.hidden && !listed:
			t.Errorf("command %q is missing from the usage", n)
		}
	}
	if strings.Contains(text, "\nother:") {
		t.Errorf("usage has an other group:\n%s", text)
	}
}
