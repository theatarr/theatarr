package arrview

import (
	"fmt"
	"testing"
)

func TestNotificationFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ view, added string }{{"radarr", "MovieAdded"}, {"sonarr", "SeriesAdd"}} {
		t.Run(tc.view, func(t *testing.T) {
			e := newDevEnv(t)
			base := "/" + tc.view + "/api/v3/notification"
			var schema []map[string]any
			e.call("GET", base+"/schema", nil, 200, &schema)
			flags := []string{"HealthRestored", tc.added, "ManualInteractionRequired", "Rename"}
			for _, flag := range flags {
				if schema[0]["supportsOn"+flag] != true || schema[0]["on"+flag] != false {
					t.Fatalf("schema %s: %v", flag, schema)
				}
			}
			body := map[string]any{"name": "Parity", "implementation": "Webhook", "configContract": "WebhookSettings",
				"fields": []map[string]any{{"name": "url", "value": "https://hook.example/event"}}}
			for _, flag := range flags {
				body["on"+flag] = true
			}
			body["includeHealthWarnings"] = true
			var out map[string]any
			e.call("POST", base, body, 201, &out)
			path := fmt.Sprintf("%s/%.0f", base, out["id"])
			e.call("GET", path, nil, 200, &out)
			for _, flag := range flags {
				if out["on"+flag] != true {
					t.Errorf("saved flag %s: %v", flag, out)
				}
			}
			if out["includeHealthWarnings"] != true {
				t.Fatal(out)
			}
			for _, flag := range flags {
				out["on"+flag] = false
			}
			e.call("PUT", path, out, 202, &out)
			e.call("GET", path, nil, 200, &out)
			for _, flag := range flags {
				if out["on"+flag] != false || out["supportsOn"+flag] != true {
					t.Errorf("disabled flag %s: %v", flag, out)
				}
			}
		})
	}
}
