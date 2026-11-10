package notify

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// sentTitles are the message titles recorded for a policy target, oldest first, and the raw payloads.
func (e *nenv) sentTitles(target string) (titles, raw []string) {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT event, payload FROM notify_delivery WHERE target = ? ORDER BY id`, "policy:"+target)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var event, payload string
		if err := rows.Scan(&event, &payload); err != nil {
			e.t.Fatal(err)
		}
		var m struct {
			Title  string `json:"title"`
			Embeds []struct {
				Title string `json:"title"`
			} `json:"embeds"`
		}
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			e.t.Fatal(err)
		}
		if len(m.Embeds) > 0 {
			m.Title = m.Embeds[0].Title
		}
		titles, raw = append(titles, m.Title), append(raw, payload)
	}
	return titles, raw
}

// Health warnings go only to a message target that asks for them; a target without the option gets exactly the
// error messages it got before, and a notice goes to nobody.
func TestHealthWarningsOptIn(t *testing.T) {
	t.Parallel()
	e := newDiscordEnv(t, true, func(p *policy.Policy) {
		p.Notify.Targets["loud"] = &policy.NotifyTarget{URL: discordHook, Format: policy.NotifyFormatDiscord, Warnings: true}
		p.Notify.Targets["pager"] = &policy.NotifyTarget{URL: ntfyURL, Format: policy.NotifyFormatNtfy, Warnings: true}
		p.Notify.Targets["phone"] = &policy.NotifyTarget{URL: ntfyURL, Format: policy.NotifyFormatNtfy}
	})
	ctx := context.Background()
	disk := func(level, msg string) HealthItem {
		return HealthItem{Source: "DiskSpaceCheck", Type: level, Message: msg}
	}
	version := func(level string) HealthItem {
		return HealthItem{Source: "VersionCheck", Type: level, Message: "version " + level}
	}
	for _, h := range [][]HealthItem{nil,
		{disk("warning", "disk 91% full")},                    // a fresh warning: no previous item
		{disk("warning", "disk 91% full"), version("notice")}, // a notice
		{disk("error", "disk full"), version("notice")},       // warning to error
		{disk("warning", "disk 91% full"), version("notice")}, // error to warning
		{disk("notice", "disk 81% full"), version("warning")}, // warning to notice, notice to warning
		nil} { // notice to ok, warning to ok
		e.hlth = h
		if err := e.n.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	errorsOnly := []string{"Health check failing: DiskSpaceCheck", "Health check no longer failing, now warning: DiskSpaceCheck"}
	withWarnings := []string{"Health warning: DiskSpaceCheck", errorsOnly[0], errorsOnly[1],
		"Health warning eased, now notice: DiskSpaceCheck", "Health warning: VersionCheck", "Health check OK again: VersionCheck"}
	quiet, quietRaw := e.sentTitles("chan")
	loud, loudRaw := e.sentTitles("loud")
	if !slices.Equal(quiet, errorsOnly) {
		t.Errorf("target without warnings: %q", quiet)
	}
	if !slices.Equal(loud, withWarnings) {
		t.Fatalf("target with warnings: %q", loud)
	}
	// The error messages are the same with the option on and off.
	if quietRaw[0] != loudRaw[1] || quietRaw[1] != loudRaw[2] {
		t.Errorf("error messages differ: %s / %s", quietRaw, loudRaw[1:3])
	}
	if got, _ := e.sentTitles("phone"); !slices.Equal(got, errorsOnly) {
		t.Errorf("ntfy without warnings: %q", got)
	}
	if got, _ := e.sentTitles("pager"); !slices.Equal(got, withWarnings) {
		t.Errorf("ntfy with warnings: %q", got)
	}
	var first discordMessage
	if err := json.Unmarshal([]byte(loudRaw[0]), &first); err != nil {
		t.Fatal(err)
	}
	if g := first.Embeds[0]; g.Description != "disk 91% full" || g.Color != discordAmber {
		t.Errorf("fresh warning %+v", g)
	}
	var pager []ntfyMessage
	_, raw := e.sentTitles("pager")
	for _, r := range raw {
		var m ntfyMessage
		if err := json.Unmarshal([]byte(r), &m); err != nil {
			t.Fatal(err)
		}
		pager = append(pager, m)
	}
	if m := pager[0]; m.Message != "disk 91% full" || m.Priority != ntfyDefault || !slices.Equal(m.Tags, []string{"warning"}) {
		t.Errorf("ntfy fresh warning %+v", m)
	}
	if m := pager[3]; m.Message != "Now: disk 81% full\nWas: disk 91% full" {
		t.Errorf("ntfy eased warning %+v", m)
	}
	if m := pager[5]; m.Message != "Was: version warning" || !slices.Equal(m.Tags, []string{"white_check_mark"}) {
		t.Errorf("ntfy warning cleared %+v", m)
	}
}

// messageWants and messageText for each health change; the text never needs a previous item.
func TestHealthMessageWantsAndText(t *testing.T) {
	t.Parallel()
	item := func(level string) *HealthItem {
		if level == "" {
			return nil
		}
		return &HealthItem{Source: "MountCheck", Type: level, Message: level + " text"}
	}
	off, on := &policy.NotifyTarget{Format: policy.NotifyFormatDiscord}, &policy.NotifyTarget{Format: policy.NotifyFormatNtfy, Warnings: true}
	for _, c := range []struct {
		kind            Kind
		now, was        string
		wantOff, wantOn bool
		title           string
		level           messageLevel
	}{
		{Health, "error", "", true, true, "Health check failing: MountCheck", levelBad},
		{Health, "error", "warning", true, true, "Health check failing: MountCheck", levelBad},
		{Health, "error", "notice", true, true, "Health check failing: MountCheck", levelBad},
		{Health, "warning", "error", true, true, "Health check no longer failing, now warning: MountCheck", levelWarn},
		{Health, "notice", "error", true, true, "Health check no longer failing, now notice: MountCheck", levelWarn},
		{HealthRestored, "error", "", true, true, "Health check OK again: MountCheck", levelGood},
		{Health, "warning", "", false, true, "Health warning: MountCheck", levelWarn},
		{Health, "warning", "notice", false, true, "Health warning: MountCheck", levelWarn},
		{Health, "notice", "warning", false, true, "Health warning eased, now notice: MountCheck", levelWarn},
		{HealthRestored, "warning", "", false, true, "Health check OK again: MountCheck", levelGood},
		{Health, "notice", "", false, false, "Health notice: MountCheck", levelWarn},
		{HealthRestored, "notice", "", false, false, "Health check OK again: MountCheck", levelGood},
	} {
		ev := Event{Kind: c.kind, Health: []HealthItem{*item(c.now)}, HealthWas: item(c.was)}
		if got := messageWants(ev, off); got != c.wantOff {
			t.Errorf("%s %s from %q, warnings off: %v", c.kind, c.now, c.was, got)
		}
		if got := messageWants(ev, on); got != c.wantOn {
			t.Errorf("%s %s from %q, warnings on: %v", c.kind, c.now, c.was, got)
		}
		title, lines, level := messageText(ev, false)
		if title != c.title || level != c.level || len(lines) == 0 {
			t.Errorf("%s %s from %q: %q %v %v", c.kind, c.now, c.was, title, lines, level)
		}
	}
}
