package arrview

import (
	"strconv"
	"strings"
	"testing"
)

// A webhook's credentials are not only its password: the URL's user information, token-like query values and path
// segments, and credential headers are masked in every response, and a client that GETs the notification and PUTs it
// back unchanged keeps them. Masks sent to another host are not filled in.
func TestNotificationSecretsMasked(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	secrets := fields(`urlpass99 AbCdEf0123456789XyZ qwerty123secret HDRSECRET123 apikey-xyz-0001 pwsecret1
		7f3a9c2e5b1d4f6a8c0e`)
	url := "https://hookuser:urlpass99@relay.example/hooks/AbCdEf0123456789XyZ?token=qwerty123secret&room=general" +
		"&id=7f3a9c2e5b1d4f6a8c0e"
	body := map[string]any{"name": "Relay", "implementation": "Webhook", "configContract": "WebhookSettings",
		"fields": []map[string]any{{"name": "url", "value": url}, {"name": "method", "value": 1},
			{"name": "username", "value": "hookuser"}, {"name": "password", "value": "pwsecret1"},
			{"name": "headers", "value": []map[string]any{{"key": "Authorization", "value": "Bearer HDRSECRET123"},
				{"key": "X-Api-Key", "value": "apikey-xyz-0001"}, {"key": "Content-Language", "value": "en"}}}},
		"onGrab": true}
	leaks := func(what string, v any) {
		t.Helper()
		b := string(mustJSON(t, v))
		for _, s := range secrets {
			if strings.Contains(b, s) {
				t.Errorf("%s returns %q: %s", what, s, b)
			}
		}
		for _, s := range fields(`hookuser relay.example /hooks/ room=general Content-Language "en"`) {
			if !strings.Contains(b, s) {
				t.Errorf("%s lost the non-secret %q: %s", what, s, b)
			}
		}
	}
	var n map[string]any
	e.call("POST", "/radarr/api/v3/notification", body, 201, &n)
	leaks("POST", n)
	id := strconv.Itoa(int(n["id"].(float64)))
	var list []map[string]any
	e.call("GET", "/radarr/api/v3/notification", nil, 200, &list)
	leaks("GET list", list)
	var one map[string]any
	e.call("GET", "/radarr/api/v3/notification/"+id, nil, 200, &one)
	leaks("GET detail", one)

	stored := func() string {
		t.Helper()
		var raw string
		if err := e.st.Reader().QueryRow(`SELECT resource FROM arrview_notification WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// GET then PUT the whole object back: every secret is kept, and the PUT answer is masked too.
	one["onDownload"] = true
	var put map[string]any
	e.call("PUT", "/radarr/api/v3/notification/"+id, one, 202, &put)
	leaks("PUT", put)
	raw := stored()
	for _, s := range secrets {
		if !strings.Contains(raw, s) {
			t.Errorf("stored secret %q lost on PUT: %s", s, raw)
		}
	}
	if !strings.Contains(raw, strings.ReplaceAll(url, "&", `\u0026`)) {
		t.Errorf("stored url changed: %s", raw)
	}
	// The same masked object pointed at another host: the masks are not filled in.
	for _, f := range put["fields"].([]any) {
		m := f.(map[string]any)
		if m["name"] == "url" {
			m["value"] = strings.Replace(m["value"].(string), "relay.example", "elsewhere.example", 1)
		}
	}
	e.call("PUT", "/radarr/api/v3/notification/"+id, put, 202, nil)
	raw = stored()
	for _, s := range secrets {
		if strings.Contains(raw, s) {
			t.Errorf("secret %q kept for another host: %s", s, raw)
		}
	}
}

func TestMaskURLRoundTrip(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, masked string }{
		{"https://notify.ruddarr.com/push/abc", "https://notify.ruddarr.com/push/abc"},
		{"https://Tok3nOnlyUserName12345@ntfy.example/topic", "https://********@ntfy.example/topic"},
		{"https://discord.com/api/webhooks/123456789012345678/aB3dEfGhIjKlMnOpQrStUvWxYz0123456789",
			"https://discord.com/api/webhooks/123456789012345678/********"},
		{"https://h.example/x?apikey=abc&apikey=def&b=1", "https://h.example/x?apikey=********&apikey=********&b=1"},
		{"not a url", "not a url"},
	} {
		m := maskURL(c.in)
		if m != c.masked {
			t.Errorf("maskURL(%q) = %q, want %q", c.in, m, c.masked)
		}
		if back := unmaskURL(m, c.in); back != c.in {
			t.Errorf("unmaskURL(%q) = %q, want %q", m, back, c.in)
		}
		// A client that escapes the asterisks gets the same answer.
		if esc := strings.ReplaceAll(m, "*", "%2A"); unmaskURL(esc, c.in) != c.in && m != c.in {
			t.Errorf("escaped mask %q not restored", esc)
		}
	}
}
