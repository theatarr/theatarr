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
		if d.ImportState == "partial" {
			return "Partly imported (retrying)"
		}
		return "Linking into the library"
	case "imported":
		return "Linked into the library"
	case "failed":
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
