// Package outcome holds the result of a repair of a file that broke while someone was playing it. The library and
// the now-playing tracker produce it; notifications and the web UI read it. It has no dependencies, so each of
// them can import it.
package outcome

import "time"

// Kinds of outcome. One outcome is reported per incident, about the file that was being watched.
const (
	// Replaced: another release was imported in place of the broken file.
	Replaced = "replaced"
	// SameRelease: the same release became readable again (for example from another account); the link is unchanged.
	SameRelease = "same_release"
	// Unavailable: the search for a replacement ended with nothing imported.
	Unavailable = "unavailable"
	// Restored: the copy that broke works again, is better than its replacement, and was put back.
	Restored = "restored"
)

// Outcome is what happened to the watched file of one incident.
type Outcome struct {
	Incident   int64
	Kind       string
	TitleID    int64
	VersionKey string
	// Title is the display title, "Show (2020)" or "Film (2019)".
	Title string
	// Episodes are labels such as "S02E07"; empty for a film.
	Episodes   []string
	OldRelease string
	// NewRelease is set only for Replaced.
	NewRelease string
	// Account is the provider verified to serve the file now; empty when unknown.
	Account string
	// Viewer is the Plex user name, Player the device, of the playback that hit the break.
	Viewer string
	Player string
	At     time.Time
}
