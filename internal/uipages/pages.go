// Package uipages names the destinations used by Go readiness and recovery messages.
// Keep these aligned with web/src/components/shell/nav.ts and web/src/lib/settings-map.ts.
package uipages

const (
	Settings    = "Settings"
	Libraries   = "Settings › Libraries"
	Providers   = "Settings › Download clients"
	Indexers    = "Settings › Indexers"
	Profiles    = "Settings › Profiles"
	QualitySync = "Settings › Quality sync"
	Apps        = "Settings › Apps"
	Metadata    = "Settings › Metadata & artwork"
	Plex        = "Settings › Plex & playback"
	Users       = "Users & access"
	Setup       = "Setup"
	Limits      = "Advanced › Request limits"
	Migration   = "Advanced › Sonarr/Radarr migration"
	Import      = "Advanced › YAML settings › Import & export"
	Tasks       = "System › Tasks"
	Torrents    = "Torrents"
	Switches    = "Setup › Review activation"

	// FileManagement is the Libraries tab with the mount, trash and broken-link settings.
	FileManagement = Libraries + " › File management"
)
