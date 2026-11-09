package policy

import (
	"net/url"
	"strings"
	"testing"
)

func TestNotifyValidation(t *testing.T) {
	t.Parallel()
	r := -1
	p := &Policy{Versions: map[string]*VersionPolicy{"movie-web": {}}, Views: map[string]*View{"radarr": {}},
		Notify: &Notify{Retries: &r, AllowHosts: []string{"notify.ruddarr.com", "bad/host"}, Budget: "nope",
			Targets: map[string]*NotifyTarget{
				"automation": {URL: "http://192.168.1.50:8080/hook", Views: []string{"radarr", "v/movie-web"}, Native: true},
				"installed":  {URL: "http://x/", Native: true},
				"silent":     {URL: "http://x/"},
				"creds":      {URL: "http://u:p@x/", Views: []string{"sonarr"}, Events: []string{"boom"}, Native: true},
			}}}
	e := &Errors{}
	p.validateNotify(e)
	got := e.Error()
	for _, want := range []string{"notify.retries", "bad/host", "unknown budget", "targets[installed]: the key installed",
		"targets[silent]: sends nothing", "no credentials", `unknown view "sonarr"`, `unknown event "boom"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "targets[automation]") {
		t.Errorf("automation refused: %s", got)
	}
	hosts := map[string]bool{}
	for _, h := range p.OutboundHosts() {
		hosts[h.Host] = true
	}
	if !hosts["notify.ruddarr.com"] || !hosts["192.168.1.50:8080"] {
		t.Errorf("outbound hosts %v", hosts)
	}
	u, _ := url.Parse("https://notify.ruddarr.com/push/x")
	if !p.NotifyHostAllowed(u) {
		t.Error("allowed host refused")
	}
	u, _ = url.Parse("https://evil.example/")
	if p.NotifyHostAllowed(u) {
		t.Error("other host allowed")
	}
	if s := (&Policy{}).NotifySettings(); s.Enabled || s.Retries != DefaultNotifyRetries {
		t.Errorf("defaults %+v", s)
	}
}

func TestNotifyParityEvents(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"health_restored", "series_add", "movie_added", "manual_interaction_required", "rename"} {
		t.Run(event, func(t *testing.T) {
			p := &Policy{Notify: &Notify{Targets: map[string]*NotifyTarget{
				"hook": {URL: "https://hook.example/event", Native: true, Events: []string{event}},
			}}}
			err := &Errors{}
			p.validateNotify(err)
			if got := err.Error(); got != "" {
				t.Fatal(got)
			}
		})
	}
}
