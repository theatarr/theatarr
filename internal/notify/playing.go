package notify

import (
	"context"
	"time"

	"github.com/theatarr/theatarr/internal/outcome"
)

// A file that broke while someone was playing it ends in one outcome (package outcome): replaced or readable again,
// nothing available, or the earlier copy put back. Each goes at once (not only in the digest) to the message targets
// (Discord, ntfy, Telegram; wording in message.go playingText) and as theatarr.playing_* to native targets, under the
// policy event name playing, with the targets' library filters. Arr webhooks get nothing, and there are no per-user
// messages.

// playingKinds maps an outcome's kind to its event kind; a same-release outcome is a replaced message worded
// "readable again".
var playingKinds = map[string]Kind{outcome.Replaced: PlayingReplaced, outcome.SameRelease: PlayingReplaced,
	outcome.Unavailable: PlayingUnavailable, outcome.Restored: PlayingRestored}

// PlayingOutcome records the messages about o. Like Emit it never sends and never fails the caller; an outcome of an
// unknown kind is logged and dropped.
func (n *Notifier) PlayingOutcome(ctx context.Context, o outcome.Outcome) {
	k, ok := playingKinds[o.Kind]
	if !ok {
		n.log.Warn("playing outcome of unknown kind not sent", "kind", o.Kind, "incident", o.Incident)
		return
	}
	n.Emit(ctx, Event{Kind: k, At: o.At, TitleID: o.TitleID, Version: o.VersionKey, Playing: &o})
}

// playingJSON is an Outcome under "playing" in a native theatarr.playing_* event, its fields in snake_case.
type playingJSON struct {
	Incident   int64    `json:"incident"`
	Kind       string   `json:"kind"`
	TitleID    int64    `json:"title_id"`
	VersionKey string   `json:"version_key"`
	Title      string   `json:"title"`
	Episodes   []string `json:"episodes"`
	OldRelease string   `json:"old_release"`
	NewRelease string   `json:"new_release"`
	Account    string   `json:"account"`
	Viewer     string   `json:"viewer"`
	Player     string   `json:"player"`
	At         string   `json:"at"`
}

func playingPayload(o *outcome.Outcome) playingJSON {
	at := ""
	if !o.At.IsZero() {
		at = o.At.UTC().Format(time.RFC3339)
	}
	return playingJSON{Incident: o.Incident, Kind: o.Kind, TitleID: o.TitleID, VersionKey: o.VersionKey,
		Title: o.Title, Episodes: append([]string{}, o.Episodes...), OldRelease: o.OldRelease, NewRelease: o.NewRelease,
		Account: o.Account, Viewer: o.Viewer, Player: o.Player, At: at}
}
