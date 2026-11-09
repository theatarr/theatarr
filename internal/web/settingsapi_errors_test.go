package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSettingsAPIConfigTypeErrorsFixed: config type errors are fixed field/type messages that
// never repeat a submitted value, and every body error is scrubbed of the request's credential.
func TestSettingsAPIConfigTypeErrorsFixed(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	cases := []struct {
		label, path string
		body        map[string]any
		want        string
	}{
		// A backtick inside the value ended the old quoted-value redaction early.
		{"backtick int", "/indexers", map[string]any{"generation": gen, "key": "typed", "secret": testSecret,
			"config": map[string]any{"kind": "torznab", "url": "http://x", "page_cap": "a`" + testSecret}}, "config.page_cap: must be an integer"},
		{"duration", "/budgets", map[string]any{"generation": gen, "key": "b",
			"config": map[string]any{"requests": 1, "per": testSecret}}, "config.per: must be a duration"},
		{"nested", fmt.Sprintf("/providers/%d", se.id(entityProviders, "rd")), map[string]any{"generation": gen, "orphan_active_downloads": false,
			"secret": testSecret, "config": map[string]any{"pacing": map[string]any{"adds_per_minute": []any{testSecret}}}}, "config.pacing.adds_per_minute: must be an integer"},
		{"object expected", "/indexers", map[string]any{"generation": gen, "key": "typed2", "secret": testSecret,
			"config": map[string]any{"kind": "seadex", "url": "http://x", "seadex": testSecret}}, "config.seadex: must be an object"},
		// A field name is echoed only after the request's credential is removed from it.
		{"unknown field named like the secret", "/indexers", map[string]any{"generation": gen, "key": "typed3", "secret": testSecret,
			"config": map[string]any{"kind": "torznab", "url": "http://x", testSecret: 1}}, "unknown field config."},
	}
	for _, c := range cases {
		rec := se.call(map[bool]string{true: "PUT", false: "POST"}[strings.Contains(c.path, "providers/")], c.path, c.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s, want 400 with %q", c.label, rec.Code, rec.Body, c.want)
		}
		if strings.Contains(rec.Body.String(), testSecret) || strings.Contains(rec.Body.String(), "`") {
			t.Errorf("%s: the error repeats the submitted value: %s", c.label, rec.Body)
		}
	}
	if se.generation() != gen {
		t.Fatal("a refused body saved settings")
	}
	se.noSecretAnywhere()
}

// TestSettingsAPIImportPreviewCredentialChange: a preview flags a record whose resolved credential
// the import would replace, without showing a value or hash.
func TestSettingsAPIImportPreviewCredentialChange(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	// The stored credential differs from what the YAML's THEATARR_DMM_KEY resolves to; the configuration is unchanged.
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", se.id(entityIndexers, "dmm")), map[string]any{"generation": gen, "secret": testSecret}),
		http.StatusOK, "set dmm credential")
	gen++
	out := se.want(se.call("POST", "/import/preview", map[string]any{"generation": gen}), http.StatusOK, "preview")
	var dmm map[string]any
	for _, c := range out["changes"].([]any) {
		m := c.(map[string]any)
		if m["entity"] == entityIndexers && m["key"] == "dmm" {
			dmm = m
		} else if m["credential_changed"] != nil {
			t.Errorf("credential flagged on an unchanged credential: %v", m)
		}
	}
	if dmm == nil || dmm["operation"] != "update" || dmm["credential_changed"] != true {
		t.Fatalf("dmm credential change not previewed: %v", out["changes"])
	}
	body := se.bodies[len(se.bodies)-1]
	if strings.Contains(body, "fixture-THEATARR_DMM_KEY") || strings.Contains(body, testSecret) {
		t.Fatal("preview shows a credential value")
	}
	if se.generation() != gen {
		t.Fatal("preview saved settings")
	}
	se.noSecretAnywhere()
}

// TestSettingsAPICandidateCooldownIdentity: a candidate's cooldown is keyed on the destination
// and account the server derives, not on the caller's key; candidate tests share a rate limit; old entries expire.
func TestSettingsAPICandidateCooldownIdentity(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	limits := &SettingsTestLimits{Cooldown: time.Minute, Now: func() time.Time { return now }}
	se := newSettingsEnv(t, limits, nil)
	cand := func(key, url, secret string) map[string]any {
		return map[string]any{"key": key, "secret": secret, "config": map[string]any{"kind": "torznab", "url": url, "privacy": "public"}}
	}
	se.want(se.call("POST", "/indexers/test", cand("probe1", "http://prowlarr:9696/3/api", testSecret)), http.StatusOK, "probe1")
	// The same destination and account under another key, or another path on that host, is the same identity.
	calls := len(se.tester.seen)
	for _, c := range []map[string]any{cand("probe2", "http://prowlarr:9696/3/api", testSecret), cand("probe3", "http://PROWLARR:9696/4/api", testSecret)} {
		rec := se.call("POST", "/indexers/test", c)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%v: %d %s, want the cooldown", c["key"], rec.Code, rec.Body)
		}
	}
	if len(se.tester.seen) != calls {
		t.Fatal("a cooled-down identity was tested")
	}
	// Another account on that destination is another identity.
	se.want(se.call("POST", "/indexers/test", cand("probe4", "http://prowlarr:9696/3/api", "another-account")), http.StatusOK, "other account")
	// Candidate tests share a rate limit across identities.
	limited := 0
	for i := range 12 {
		rec := se.call("POST", "/indexers/test", cand(fmt.Sprintf("r%d", i), fmt.Sprintf("http://feed%d.example:9696/api", i), testSecret))
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("fourteen candidate tests in a minute were all run")
	}
	// Entries expire: after the cooldown only the newest identity is kept.
	now = now.Add(5 * time.Minute)
	se.want(se.call("POST", "/indexers/test", cand("later", "http://later.example/api", testSecret)), http.StatusOK, "later")
	limits.mu.Lock()
	kept := len(limits.started)
	limits.mu.Unlock()
	if kept != 1 {
		t.Errorf("%d cooldown entries kept after they expired", kept)
	}
	se.noSecretAnywhere()
}

// TestSettingsAPITrailingJSONRefused: anything after the body object is refused before a write.
func TestSettingsAPITrailingJSONRefused(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	path := fmt.Sprintf("/indexers/%d", se.id(entityIndexers, "zilean"))
	for _, suffix := range []string{"}", "]", " {}", " x"} {
		rec := se.call("PUT", path, fmt.Sprintf(`{"generation":%d,"priority":7}%s`, gen, suffix))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("trailing %q: %d %s", suffix, rec.Code, rec.Body)
		}
	}
	if se.generation() != gen {
		t.Fatal("a malformed body saved settings")
	}
	// Trailing white space is fine, and an empty body still means {} where the route allows it.
	se.want(se.call("PUT", path, fmt.Sprintf("{\"generation\":%d,\"priority\":7}\n ", gen)), http.StatusOK, "trailing space")
	se.want(se.call("POST", "/indexers/testall", ""), http.StatusOK, "empty body")
}
