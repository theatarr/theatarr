package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/policy"
)

// RcloneTestGateway builds a temporary gateway over the current runtime, sharing
// its budgets. The candidate adds only the rc endpoint; nothing is saved.
type RcloneTestGateway func(context.Context, *policy.Policy, []string) (nowplaying.Doer, error)

func (w *Web) apiRcloneTest(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		URL               string `json:"url"`
		Username          string `json:"username"`
		Password          string `json:"password"`
		UseStored         bool   `json:"use_stored"`
		UseStoredUsername bool   `json:"use_stored_username"`
		UseStoredPassword bool   `json:"use_stored_password"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	reply := func(message string) { writeJSON(rw, http.StatusOK, map[string]any{"ok": false, "error": message}) }
	u, err := url.Parse(body.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		reply("Enter an HTTP or HTTPS URL without credentials, query or fragment.")
		return
	}
	if w.rcloneTestGateway == nil {
		reply("Connection tests are unavailable.")
		return
	}
	p := w.pol
	if w.settings != nil {
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		p = snap.Policy
	}
	var stored *policy.RcloneRC
	if p.Library != nil {
		stored = p.Library.Mount.RcloneRC
	}
	useUsername := (body.UseStored || body.UseStoredUsername) && body.Username == ""
	usePassword := (body.UseStored || body.UseStoredPassword) && body.Password == ""
	if (useUsername || usePassword) && stored != nil {
		// A saved credential is only sent to the endpoint it belongs to.
		if strings.TrimRight(stored.URL, "/") != strings.TrimRight(body.URL, "/") {
			reply("Enter credentials to test a different URL.")
			return
		}
		if usePassword {
			body.Password = p.Getenv(stored.PasswordEnv)
		}
		if useUsername {
			body.Username = p.Getenv(stored.UsernameEnv)
		}
	}
	candidate := *p
	library := policy.Library{}
	if p.Library != nil {
		library = *p.Library
	}
	rc := policy.RcloneRC{URL: body.URL}
	if stored != nil {
		rc.Budget = stored.Budget
	}
	library.Mount.RcloneRC = &rc
	candidate.Library = &library
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	gw, err := w.rcloneTestGateway(ctx, &candidate, []string{body.Username, body.Password})
	if err != nil {
		reply("Could not prepare the connection test.")
		return
	}
	client := nowplaying.RC{URL: body.URL, User: body.Username, Password: body.Password, GW: gw}
	version, err := client.Version(ctx)
	if err != nil {
		if ctx.Err() != nil {
			reply("No answer within 5 seconds.")
		} else {
			reply("Connection failed. Check the URL and credentials.")
		}
		return
	}
	// The upstream version is untrusted, and may echo authentication values.
	if len(version) > 120 || strings.ContainsAny(version, "\r\n") {
		version = ""
	}
	for _, secret := range []string{body.Username, body.Password} {
		if secret != "" && strings.Contains(version, secret) {
			version = ""
		}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "version": version})
}
