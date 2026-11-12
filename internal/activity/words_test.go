package activity

import (
	"slices"
	"testing"
	"time"
)

func TestStepsAndWords(t *testing.T) {
	added := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		d       Download
		step    string
		stopped bool
		user    string
		admin   string
	}{
		{Download{State: "queued"}, "queued", false, "Queued", "Queued"},
		{Download{State: "queued", Error: "waiting for a provider until 12:05"}, "queued", false,
			"Waiting for a free slot", "Waiting for a free slot"},
		{Download{State: "adding"}, "adding", false, "Starting the download", "Adding to Real-Debrid"},
		{Download{State: "added"}, "adding", false, "Waiting for the download to start",
			"Added to Real-Debrid, waiting for it to start"},
		{Download{State: "downloading", Progress: 41.6}, "downloading", false, "Downloading (42%)",
			"Downloading on Real-Debrid (42%)"},
		{Download{State: "ready"}, "ready", false, "Download complete, waiting to be linked", "Download complete, waiting to be linked"},
		{Download{State: "ready", ImportState: "waiting_mount"}, "ready", false, "Waiting for the mount",
			"Waiting for the mount"},
		{Download{State: "ready", ImportState: "waiting_mount", ImportErr: "file unreadable on the mount: read x: EIO"}, "ready",
			false, "File unreadable on the mount, retrying", "File unreadable on the mount, retrying"},
		{Download{State: "ready", ImportState: "waiting_mount", ImportErr: "mount mismatch: zurg does not confirm the files are this download's (x)"},
			"ready", false, "Waiting for the mount to show this download's files", "Waiting for the mount to show this download's files"},
		{Download{State: "ready", ImportState: "rejected"}, "ready", true, "Import blocked",
			"Import blocked"},
		{Download{State: "importing"}, "ready", false, "Linking into the library", "Linking into the library"},
		{Download{State: "importing", ImportState: "waiting_mount", ImportErr: "mount mismatch: zurg does not confirm (x)"},
			"ready", false, "Waiting for the mount to show this download's files", "Waiting for the mount to show this download's files"},
		{Download{State: "importing", ImportState: "partial"}, "ready", false, "Partly imported (retrying)",
			"Partly imported (retrying)"},
		{Download{State: "imported"}, "linked", false, "Linked into the library", "Linked into the library"},
		{Download{State: "failed", Error: "miss on every provider"}, "adding", true, "Download failed", "Download failed"},
		{Download{State: "failed", Error: "stalled: not ready", Added: added, Provider: "rd"}, "downloading", true,
			"Stalled, so it was dropped", "Stalled, so it was dropped"},
		{Download{State: "failed", Ready: added, Provider: "rd"}, "ready", true, "Download failed", "Download failed"},
	}
	for _, c := range cases {
		step, stopped := c.d.Step()
		if Steps[step] != c.step || stopped != c.stopped {
			t.Errorf("%s/%s: step %s stopped %v, want %s %v", c.d.State, c.d.ImportState, Steps[step], stopped, c.step,
				c.stopped)
		}
		if got := c.d.Words(""); got != c.user {
			t.Errorf("%s user words %q, want %q", c.d.State, got, c.user)
		}
		if got := c.d.Words("Real-Debrid"); got != c.admin {
			t.Errorf("%s admin words %q, want %q", c.d.State, got, c.admin)
		}
	}
	if ProviderName("alldebrid") != "AllDebrid" || ProviderName("tb") != "TorBox" || ProviderName("x") != "x" {
		t.Error("provider names")
	}
}

func TestStage(t *testing.T) {
	cases := []struct {
		d    Download
		want string
	}{
		{Download{State: "queued"}, "queued"},
		{Download{State: "adding"}, "starting"},
		{Download{State: "added"}, "starting"},
		{Download{State: "downloading", Progress: 37}, "downloading"},
		{Download{State: "ready"}, "complete"},
		{Download{State: "ready", ImportState: "dry_run"}, "complete"},
		{Download{State: "ready", ImportState: "waiting_mount"}, "waiting_mount"},
		{Download{State: "importing", ImportState: "waiting_mount"}, "waiting_mount"},
		{Download{State: "importing"}, "linking"},
		{Download{State: "ready", ImportState: "partial"}, "linking"},
		{Download{State: "ready", ImportState: "rejected"}, "blocked"},
		{Download{State: "ready", ImportState: "failed"}, "blocked"},
		{Download{State: "imported"}, ""},
		{Download{State: "failed"}, ""},
	}
	for _, c := range cases {
		if got := c.d.Stage(); got != c.want {
			t.Errorf("%s/%s: stage %q, want %q", c.d.State, c.d.ImportState, got, c.want)
		}
	}
	for _, d := range []Download{{State: "queued"}, {State: "adding"}, {State: "downloading"}, {State: "ready"},
		{State: "importing"}} {
		if !slices.Contains(Stages, d.Stage()) {
			t.Errorf("%s: stage %q is not in Stages", d.State, d.Stage())
		}
	}
}

func TestNotStartedWords(t *testing.T) {
	for _, prefix := range []string{"route ended: ", "route stopped: "} {
		for text, want := range map[string][2]string{
			"router: router.enabled is false; nothing is written to any provider": {"off", "Downloads were off when it was due to start"},
			"router: the library is Watch only; nothing is written for it":        {"watch_only", "Its library is Watch only, so it was not started"},
			"router: the version's grab switch is off; nothing is written for it": {"grab_off", "Downloads are off for its library, so it was not started"},
		} {
			d := Download{State: "failed", Error: prefix + text}
			if got := d.NotStarted(); got != want[0] {
				t.Errorf("%s%s: reason %q, want %q", prefix, text, got, want[0])
			}
			if got := d.Words(""); got != want[1] {
				t.Errorf("%s%s: words %q", prefix, text, got)
			}
			if got := (&Download{State: "queued", Error: prefix + text}).NotStarted(); got != "" {
				t.Errorf("queued row read as not started: %q", got)
			}
		}
	}
	for text, words := range map[string]string{"download failed: x": "Download failed",
		"stalled: not ready": "Stalled, so it was dropped",
		"route ended: its release was not placed by the route that queued it": "Download failed"} {
		d := Download{State: "failed", Error: text}
		if d.NotStarted() != "" || d.Words("") != words {
			t.Errorf("%q: reason %q, words %q", text, d.NotStarted(), d.Words(""))
		}
	}
}
