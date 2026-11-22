package uipages

import "strings"

// What a connection failure was about, for Fix.
const (
	FixProvider = "provider" // a debrid provider; Name is its name (ProviderName)
	FixIndexer  = "indexer"  // an indexer; Name is its key
	FixPlex     = "plex"     // the Plex server; At is its address
	FixMount    = "mount"    // the debrid mount; At is its path
	FixRclone   = "rclone"   // the mount's rclone remote control; At is its URL
	FixMetadata = "metadata" // a metadata service; Name is TMDb or TVDB
)

// The failure classes Fix knows: the source tests' categories (web.SettingsTestOutcome), plus two for a mount.
const (
	FailAuth         = "auth"         // the source refused the credential
	FailCredential   = "credential"   // no credential is set
	FailConnectivity = "connectivity" // the source could not be reached
	FailTimeout      = "timeout"      // no answer in time
	FailHTTP         = "http"         // the source answered with an error status
	FailEmpty        = "empty"        // a mount lists nothing
	FailUnreadable   = "unreadable"   // a mount does not read or answer
)

// Fix is the one sentence for a connection failure of class on a source of kind (FixProvider, ...): what failed and
// the setting to change or the action to take. It is "" for a class it has no sentence for (a gateway refusal,
// say), where the caller keeps its own words.
func Fix(kind, name, at, class string) string {
	switch kind {
	case FixProvider:
		switch class {
		case FailAuth:
			return name + " refused the key: paste a new API token under " + Providers
		case FailCredential:
			return "No API token is set for " + name + ": paste one under " + Providers
		case FailConnectivity:
			return name + " cannot be reached: check this server's network and VPN"
		case FailTimeout, FailHTTP:
			return name + " is not answering properly: it may be down; try again later"
		}
	case FixIndexer:
		switch class {
		case FailAuth:
			return name + " refused the API key: check it under " + Indexers
		case FailCredential:
			return "No API key is set for " + name + ": add it under " + Indexers
		case FailConnectivity, FailTimeout:
			return name + " cannot be reached: check its URL under " + Indexers + ", and that the site is up"
		case FailHTTP:
			return name + " answered with an error: check its URL under " + Indexers
		}
	case FixPlex:
		switch class {
		case FailAuth:
			return "Plex refused the token: paste a new one under " + Plex
		case FailConnectivity, FailTimeout, FailHTTP:
			if at == "" {
				return "Plex does not answer: check the address under " + Plex
			}
			return "Plex does not answer at " + at + ": check the address under " + Plex
		}
	case FixMount:
		switch class {
		case FailEmpty:
			return "The mount at " + at + " lists nothing: is rclone running?"
		case FailUnreadable, FailTimeout:
			return "The mount at " + at + " does not answer: is rclone running?"
		}
	case FixRclone:
		switch class {
		case FailAuth:
			return "rclone refused the username or password: enter the ones rclone runs with (--rc-user, --rc-pass)"
		case FailConnectivity, FailTimeout:
			return "rclone does not answer at " + at + ": is rclone running with --rc, and is the URL right?"
		case FailHTTP:
			return "rclone answered with an error at " + at + ": check the URL"
		}
	case FixMetadata:
		switch class {
		case FailAuth:
			return name + " refused the key: check it under " + Metadata
		case FailCredential:
			return "No " + name + " key is set: add one under " + Metadata
		case FailConnectivity:
			// A count of failures with no known cause: the neutral advice.
			return name + " could not be read: check this server's network, or try again later"
		}
	}
	return ""
}

// HTTPClass is the failure class of an HTTP status a source answered with: auth for 401 and 403, http otherwise.
func HTTPClass(status int) string {
	if status == 401 || status == 403 {
		return FailAuth
	}
	return FailHTTP
}

// ProviderName names a debrid provider for people, by its policy kind or key.
func ProviderName(kindOrKey string) string {
	switch strings.ToLower(kindOrKey) {
	case "realdebrid", "rd":
		return "Real-Debrid"
	case "alldebrid", "ad":
		return "AllDebrid"
	case "torbox", "tb":
		return "TorBox"
	}
	return kindOrKey
}
