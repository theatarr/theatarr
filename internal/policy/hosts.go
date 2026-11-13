package policy

import (
	"fmt"
	"net/url"
	"sort"
)

// OutboundHost is one host the policy makes the service talk to, and what uses it.
type OutboundHost struct {
	Host  string   // host[:port] as configured
	Users []string // e.g. "indexers[dmm]", "providers[rd]"
}

// OutboundHosts lists every host the policy configures, sorted. The service logs it at startup, so nothing talks to
// an endpoint nobody configured.
func (p *Policy) OutboundHosts() []OutboundHost {
	users := map[string][]string{}
	add := func(user, raw string) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return
		}
		users[u.Host] = append(users[u.Host], user)
	}
	for _, k := range sortedKeys(p.Indexers) {
		add(fmt.Sprintf("indexers[%s]", k), p.Indexers[k].URL)
		for _, h := range p.Indexers[k].Hosts {
			add(fmt.Sprintf("indexers[%s].hosts", k), "https://"+h)
		}
	}
	if p.Cardigann != nil && p.Cardigann.FlareSolverrURL != "" {
		add("cardigann.flaresolverr_url", p.Cardigann.FlareSolverrURL)
	}
	if p.Cardigann != nil && p.Cardigann.Prowlarr != nil {
		add("cardigann.prowlarr.url", p.Cardigann.Prowlarr.URL)
	}
	for _, k := range sortedKeys(p.Providers) {
		add(fmt.Sprintf("providers[%s]", k), p.Providers[k].URL)
	}
	for _, k := range sortedKeys(p.Clients) {
		add(fmt.Sprintf("clients[%s]", k), p.Clients[k].URL)
	}
	if p.Metadata != nil {
		for _, k := range sortedKeys(p.Metadata.Sources) {
			add(fmt.Sprintf("metadata.sources[%s]", k), p.Metadata.Sources[k].URL)
		}
	}
	if p.Egress != nil {
		add("egress.check_url", p.Egress.CheckURL)
	}
	if p.Library != nil && p.Library.Plex != nil {
		add("library.plex.url", p.Library.Plex.URL)
	}
	if p.Library != nil && ZurgLogIsURL(p.Library.Mount.ZurgLog) {
		add("library.mount.zurg_log", p.Library.Mount.ZurgLog)
	}
	if p.Library != nil && p.Library.Mount.ZurgAPI != "" {
		add("library.mount.zurg_api", p.Library.Mount.ZurgAPI)
	}
	if p.Library != nil && p.Library.Mount.RcloneRC != nil {
		add("library.mount.rclone_rc", p.Library.Mount.RcloneRC.URL)
	}
	if p.Library != nil && p.Library.Mount.Decypharr != nil {
		add("library.mount.decypharr.url", p.Library.Mount.Decypharr.URL)
	}
	if p.Notify != nil {
		for _, k := range sortedKeys(p.Notify.Targets) {
			if t := p.Notify.Targets[k]; t != nil {
				add(fmt.Sprintf("notify.targets[%s]", k), t.URL)
			}
		}
		for _, h := range p.Notify.AllowHosts {
			add("notify.allow_hosts", "http://"+h)
		}
	}
	out := make([]OutboundHost, 0, len(users))
	for h, us := range users {
		out = append(out, OutboundHost{Host: h, Users: us})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// SecretEnvs lists every environment variable name the policy refers to for secrets, with its user, sorted by
// name. The service can check they are set without ever printing their values.
func (p *Policy) SecretEnvs() map[string][]string {
	m := map[string][]string{}
	add := func(user, env string) {
		if env != "" {
			m[env] = append(m[env], user)
		}
	}
	for _, k := range sortedKeys(p.Indexers) {
		add(fmt.Sprintf("indexers[%s].api_key_env", k), p.Indexers[k].APIKeyEnv)
	}
	for _, k := range sortedKeys(p.Providers) {
		add(fmt.Sprintf("providers[%s].api_key_env", k), p.Providers[k].APIKeyEnv)
	}
	for _, k := range sortedKeys(p.Clients) {
		c := p.Clients[k]
		add(fmt.Sprintf("clients[%s].username_env", k), c.UsernameEnv)
		add(fmt.Sprintf("clients[%s].password_env", k), c.PasswordEnv)
	}
	for _, k := range sortedKeys(p.Views) {
		add(fmt.Sprintf("views[%s].api_key_env", k), p.Views[k].APIKeyEnv)
	}
	if p.Metadata != nil {
		for _, k := range sortedKeys(p.Metadata.Sources) {
			add(fmt.Sprintf("metadata.sources[%s].api_key_env", k), p.Metadata.Sources[k].APIKeyEnv)
		}
	}
	if p.Cardigann != nil && p.Cardigann.Prowlarr != nil {
		add("cardigann.prowlarr.api_key_env", p.Cardigann.Prowlarr.APIKeyEnv)
	}
	if p.Library != nil && p.Library.Plex != nil {
		add("library.plex.token_env", p.Library.Plex.TokenEnv)
	}
	if p.Library != nil && p.Library.Mount.RcloneRC != nil {
		add("library.mount.rclone_rc.username_env", p.Library.Mount.RcloneRC.UsernameEnv)
		add("library.mount.rclone_rc.password_env", p.Library.Mount.RcloneRC.PasswordEnv)
	}
	if p.Library != nil && p.Library.Mount.Decypharr != nil {
		d := p.Library.Mount.Decypharr
		add("library.mount.decypharr.api_token_env", d.APITokenEnv)
		add("library.mount.decypharr.username_env", d.UsernameEnv)
		add("library.mount.decypharr.password_env", d.PasswordEnv)
	}
	if p.Notify != nil {
		for _, k := range sortedKeys(p.Notify.Targets) {
			if t := p.Notify.Targets[k]; t != nil {
				add(fmt.Sprintf("notify.targets[%s].auth_header_env", k), t.AuthHeaderEnv)
			}
		}
	}
	return m
}
