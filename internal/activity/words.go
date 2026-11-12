package activity

import (
	"fmt"
	"math"
	"strings"
)

// Steps are what a download walks through, in order, as the activity shows them.
var Steps = []string{"queued", "adding", "downloading", "ready", "linked"}

// Step is the download's place in Steps (an index), and whether it stopped there (failed or rejected).
func (d *Download) Step() (step int, stopped bool) {
	switch d.State {
	case "queued":
		return 0, false
	case "adding", "added":
		return 1, false
	case "downloading":
		return 2, false
	case "ready", "importing":
		if d.ImportState == "rejected" || d.ImportState == "failed" {
			return 3, true
		}
		return 3, false
	case "imported":
		return 4, false
	case "failed":
		// Where it stopped: never placed, placed but not finished, or finished and refused by the library.
		switch {
		case !d.Ready.IsZero():
			return 3, true
		case !d.Added.IsZero() || d.Provider != "" || d.Client != "":
			return 2, true
		}
		return 1, true
	}
	return 0, false
}

// Stages are the acquisition stages the library views show for a download in hand, in order: queued, starting (the
// provider add), downloading, complete (downloaded, not linked yet), waiting_mount (complete, its files not on the
// mount yet), linking, and blocked (complete, but the library refused or failed it). The library's own words for an
// available file are not a stage: a copy with nothing in hand has no acquisition.
var Stages = []string{"queued", "starting", "downloading", "complete", "waiting_mount", "linking", "blocked"}

// Stage is the download's acquisition stage (Stages) for the library views, "" once it is done (imported or
// failed): the same reading of state and library import as Step and Words.
func (d *Download) Stage() string {
	switch d.State {
	case "queued":
		return "queued"
	case "adding", "added":
		return "starting"
	case "downloading":
		return "downloading"
	case "ready", "importing":
		switch d.ImportState {
		case "waiting_mount":
			return "waiting_mount"
		case "rejected", "failed":
			return "blocked"
		case "partial":
			return "linking"
		}
		if d.State == "importing" {
			return "linking"
		}
		return "complete"
	}
	return ""
}

// ProviderName names a provider for people: by its policy kind (realdebrid, alldebrid, torbox) or key.
func ProviderName(kindOrKey string) string {
	switch strings.ToLower(kindOrKey) {
	case "realdebrid", "rd":
		return "Real-Debrid"
	case "alldebrid", "ad":
		return "AllDebrid"
	case "torbox", "tb":
		return "TorBox"
	case "":
		return ""
	}
	return kindOrKey
}

// unreadablePrefix is library.UnreadablePrefix: an import backed off because the mount would not read its files
// while it answered otherwise.
const unreadablePrefix = "file unreadable on the mount: "

// mismatchPrefix is router.ImportMismatchPrefix: an import waiting while the mount and zurg or decypharr disagree
// about its files.
const mismatchPrefix = "mount mismatch: "

// Words is the download's state in plain words. With provider (a name, e.g. "Real-Debrid"), the words name it
// (admins); without, they name none (users never see providers, docs/design/ui.md §8.4).
func (d *Download) Words(provider string) string {
	switch d.State {
	case "queued":
		if d.Error != "" && strings.HasPrefix(d.Error, "waiting") {
			return "Waiting for a free slot"
		}
		return "Queued"
	case "adding":
		if provider == "" {
			return "Starting the download"
		}
		if strings.HasPrefix(d.Error, "add not settled") {
			// The router could not tell yet whether the provider took the add; nothing is added again until it can.
			return "Adding to " + provider + ": not confirmed yet"
		}
		return "Adding to " + provider
	case "added":
		if provider == "" {
			return "Waiting for the download to start"
		}
		return "Added to " + provider + ", waiting for it to start"
	case "downloading":
		pct := int(math.Round(max(0, min(d.Progress, 100)))) // as the SPA rounds its bar label
		if provider == "" {
			return fmt.Sprintf("Downloading (%d%%)", pct)
		}
		return fmt.Sprintf("Downloading on %s (%d%%)", provider, pct)
	case "ready":
		switch d.ImportState {
		case "waiting_mount":
			if strings.HasPrefix(d.ImportErr, unreadablePrefix) {
				return "File unreadable on the mount, retrying"
			}
			if strings.HasPrefix(d.ImportErr, mismatchPrefix) {
				return "Waiting for the mount to show this download's files"
			}
			return "Waiting for the mount"
		case "partial":
			return "Partly imported (retrying)"
		case "rejected":
			if d.ImportErr != "" {
				return "Import blocked: " + d.ImportErr
			}
			return "Import blocked"
		case "failed":
			return "Linking failed, retrying"
		case "dry_run":
			return "Download complete (the library is in dry-run mode)"
		}
		return "Download complete, waiting to be linked"
	case "importing":
		if d.ImportState == "waiting_mount" && strings.HasPrefix(d.ImportErr, mismatchPrefix) {
			return "Waiting for the mount to show this download's files" // after a partial import
		}
		if d.ImportState == "partial" {
			return "Partly imported (retrying)"
		}
		return "Linking into the library"
	case "imported":
		return "Linked into the library"
	case "failed":
		if r := d.NotStarted(); r != "" {
			return notStartedWords[r]
		}
		if strings.HasPrefix(d.Error, "stalled") {
			return "Stalled, so it was dropped"
		}
		if strings.Contains(d.Error, "removed from the queue") {
			return "Removed from the queue"
		}
		return "Download failed"
	}
	return d.State
}

// NotStartedSQL selects the downloads NotStarted reads as not started (with state = 'failed'): the same two prefixes.
// GLOB, not LIKE: it is case-sensitive, as strings.CutPrefix is.
const NotStartedSQL = "(COALESCE(error, '') GLOB 'route ended: router: *' OR COALESCE(error, '') GLOB 'route stopped: router: *')"

// The router's gate errors (router.ErrShadow, router.ErrNoGrab; every other gate refusal is router.ErrDisabled). The
// texts are mirrored here because activity does not import router; a test in internal/web pins them.
const (
	gateWatchOnly = "router: the library is Watch only; nothing is written for it"
	gateNoGrab    = "router: the version's grab switch is off; nothing is written for it"
)

// NotStarted says why a download ended before anything was sent to a provider because downloads were off when its
// turn came: "off" (Pause, the environment lock, new downloads off), "watch_only" (its library is Watch only) or
// "grab_off" (the library's downloads switch is off). "" for any other error text. The router writes these texts
// ("route ended: " / "route stopped: " + the gate's error) only when its gate refused before a provider add.
func NotStarted(errText string) string {
	rest, ok := strings.CutPrefix(errText, "route ended: ")
	if !ok {
		rest, ok = strings.CutPrefix(errText, "route stopped: ")
	}
	if !ok || !strings.HasPrefix(rest, "router: ") {
		return ""
	}
	switch rest {
	case gateWatchOnly:
		return "watch_only"
	case gateNoGrab:
		return "grab_off"
	}
	return "off"
}

// NotStarted is NotStarted of a failed download's error; "" for any other state.
func (d *Download) NotStarted() string {
	if d.State != "failed" {
		return ""
	}
	return NotStarted(d.Error)
}

var notStartedWords = map[string]string{
	"off":        "Downloads were off when it was due to start",
	"watch_only": "Its library is Watch only, so it was not started",
	"grab_off":   "Downloads are off for its library, so it was not started",
}

var notStartedMessages = map[string]string{
	"off":        "Nothing was added to a provider. Use Retry once downloads are on again.",
	"watch_only": "Nothing was added to a provider. Use Retry once the library is Live.",
	"grab_off":   "Nothing was added to a provider. Use Retry once downloads are on for the library.",
}

var notStartedHistory = map[string]string{
	"off":        "Not started: downloads were off",
	"watch_only": "Not started: its library is Watch only",
	"grab_off":   "Not started: downloads are off for its library",
}

// NotStartedMessage is the admin's message for a not-started download (by NotStarted's reason), "" for none.
func NotStartedMessage(reason string) string { return notStartedMessages[reason] }

// NotStartedHistory is the History line for a failure whose error text NotStarted reads as not started, "" otherwise.
func NotStartedHistory(errText string) string { return notStartedHistory[NotStarted(errText)] }
