package main

import "testing"

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
