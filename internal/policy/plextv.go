package policy

import "fmt"

// PlexTVSource is the metadata source key of plex.tv, which Plex sign-in talks to through the gateway (its pins,
// the signed-in account, and the account's resources; docs/design/users.md). Sign-in also needs library.plex with its
// token: it checks the account's resources for our server's machineIdentifier, which the local server's /identity
// gives. Without library.plex the service logs that Plex sign-in is off.
const PlexTVSource = "plex-tv"

func (p *Policy) validatePlexTV(e *Errors) {
	if p.Metadata == nil || p.Metadata.Sources[PlexTVSource] == nil {
		return
	}
	path := fmt.Sprintf("metadata.sources[%s]", PlexTVSource)
	s := p.Metadata.Sources[PlexTVSource]
	if s.APIKeyEnv != "" {
		e.add(path+".api_key_env", "plex.tv needs no key; each sign-in uses the account's own token, never stored")
	}
}
