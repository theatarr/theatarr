package notify

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Message targets (notify.targets.*.format: discord or ntfy) get Theatarr's own human-readable messages instead of
// JSON events: instant health alerts (a check failing, and leaving error), error alerts, the daily digest, request
// ready and the test. The wording here is shared; discord.go and ntfy.go lay it out for their service.

// messageLevel is how a message is marked: the embed colour on Discord, the priority and tag on ntfy.
type messageLevel int

const (
	levelInfo messageLevel = iota
	levelGood
	levelWarn
	levelBad
)

// messageKinds are the events a message target gets.
var messageKinds = map[Kind]bool{Health: true, HealthRestored: true, Error: true, Digest: true,
	RequestAvailable: true, RequestPartial: true, Test: true}

// messageWants reports whether a message target gets ev: its kinds, and health only for checks in error (an alert
// when one fails, and one when it leaves error: no longer failing but still a warning, or OK again). A warning
// clearing after that is not announced.
func messageWants(ev Event) bool {
	if !messageKinds[ev.Kind] {
		return false
	}
	if ev.Kind == Health || ev.Kind == HealthRestored {
		return len(ev.Health) > 0 && (ev.Health[0].Type == "error" || ev.HealthWas != nil && ev.HealthWas.Type == "error")
	}
	return true
}

// messageText is the title, lines and level of a health, error or digest message. markdown writes Discord's
// markdown (bold labels, the error sample in a code block); without it the text is plain.
func messageText(ev Event, markdown bool) (string, []string, messageLevel) {
	switch ev.Kind {
	case Health:
		h := ev.Health[0]
		if h.Type != "error" {
			return "Health check no longer failing, now " + h.Type + ": " + h.Source,
				[]string{"Now: " + h.Message, "Was: " + ev.HealthWas.Message}, levelWarn
		}
		return "Health check failing: " + h.Source, []string{h.Message}, levelBad
	case HealthRestored:
		h := ev.Health[0]
		return "Health check OK again: " + h.Source, []string{"Was: " + h.Message}, levelGood
	case Error:
		e := ev.Error
		title := "New error: " + e.Component
		if e.Reopened {
			title = "Error back after it was resolved: " + e.Component
		}
		sample := e.Sample
		if markdown {
			sample = "```" + strings.ReplaceAll(e.Sample, "```", "'''") + "```"
		}
		return title, []string{sample,
			fmt.Sprintf("Source %s · seen %d times · Admin › System › Errors #%d", e.Source, e.Count, e.ID)}, levelBad
	case Digest:
		return digestText(ev.Digest, markdown)
	}
	return "", nil, levelInfo
}

// digestText lays out the daily digest.
func digestText(d *DigestSummary, markdown bool) (string, []string, messageLevel) {
	label := func(s string) string {
		if markdown {
			return "**" + s + "**"
		}
		return s
	}
	var lines []string
	level := levelInfo
	if d.Quiet() {
		lines = append(lines, "Quiet day: nothing grabbed, imported, failed or blocklisted; no dead links, open errors or health problems.")
	} else {
		line := fmt.Sprintf("%s %d grabbed · %d imported · %d failed · %d blocklisted · %d dead links found",
			label("Last 24 h:"), d.Grabs, d.Imports, d.Failed, d.Blocklisted, d.DeadLinks)
		if d.DeadLinks > 0 {
			line += fmt.Sprintf(", %d still dead", d.StillDead)
		}
		lines = append(lines, line)
		if len(d.Grabbed) > 0 {
			lines = append(lines, label("Grabbed:")+" "+strings.Join(d.Grabbed, ", ")+more(d.Grabs, len(d.Grabbed)))
		}
		if len(d.Imported) > 0 {
			lines = append(lines, label("Imported:")+" "+strings.Join(d.Imported, ", ")+more(d.Imports, len(d.Imported)))
		}
		if d.OpenErrors > 0 {
			lines = append(lines, fmt.Sprintf("%s %d (Admin › System › Errors)", label("Open error groups:"), d.OpenErrors))
			for _, l := range d.Errors {
				lines = append(lines, "• "+l)
			}
		}
		if d.Accounts != nil {
			lines = append(lines, label("Debrid accounts:")+" "+d.Accounts.Message)
		}
		if len(d.Health) > 0 {
			lines = append(lines, fmt.Sprintf("%s %d", label("Health checks not OK:"), len(d.Health)))
			for _, h := range d.Health {
				lines = append(lines, "• "+h.Type+": "+h.Message)
			}
		}
	}
	if d.GrabbingOff {
		lines = append(lines, "Grabbing is off (router.enabled is false): Theatarr adds nothing to the debrid providers.")
	}
	if d.GrabbingOff || d.Accounts != nil || len(d.Health) > 0 || d.OpenErrors > 0 {
		level = levelWarn
	}
	return "Theatarr daily digest · " + d.Day, lines, level
}

// requestText is the subject of a request-ready message, "<Title> (year)", and what happened to it ("is ready in
// Plex", or "is partly ready in Plex" for a partial series).
func (n *Notifier) requestText(ctx context.Context, ev Event) (name, ready string, err error) {
	r := ev.Request
	name = r.Name
	year := 0
	if r.TitleID > 0 {
		var tn string
		err := n.st.Reader().QueryRowContext(ctx, `SELECT name, COALESCE(year, 0) FROM title WHERE id = ?`, r.TitleID).
			Scan(&tn, &year)
		if err != nil && err != sql.ErrNoRows {
			return "", "", err
		}
		if tn != "" {
			name = tn
		}
	}
	if year > 0 && !strings.HasSuffix(name, "("+strconv.Itoa(year)+")") {
		name += " (" + strconv.Itoa(year) + ")"
	}
	ready = "is ready in Plex"
	if ev.Kind == RequestPartial {
		ready = "is partly ready in Plex"
	}
	return name, ready, nil
}

// requesterName is the name a message shows for a requester.
func requesterName(u RequestUser) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

func more(total, shown int) string {
	if total > shown {
		return fmt.Sprintf(" and %d more", total-shown)
	}
	return ""
}

func joinAnd(xs []string) string {
	if len(xs) <= 1 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
