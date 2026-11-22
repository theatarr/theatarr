package notify

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
)

// Message targets (notify.targets.*.format: discord, ntfy or telegram) get Theatarr's own human-readable messages
// instead of JSON events: instant health alerts (a check failing, and leaving error; warnings too for a target that
// asks for them), the daily digest, request ready, problem reports, a file that broke while playing and the test.
// The wording here is shared: discord.go lays it out with markdown, and ntfy.go and telegram.go take the same plain
// text from plainText.

// messageLevel is how a message is marked: the embed colour on Discord, the priority and tag on ntfy.
type messageLevel int

const (
	levelInfo messageLevel = iota
	levelGood
	levelWarn
	levelBad
)

// messageKinds are the events a message target gets.
var messageKinds = map[Kind]bool{Health: true, HealthRestored: true, Digest: true,
	RequestAvailable: true, RequestPartial: true, ProblemReported: true, Test: true,
	PlayingReplaced: true, PlayingUnavailable: true, PlayingRestored: true}

// messageWants reports whether message target t gets ev: its kinds, and health only for checks in error (an alert
// when one fails, and one when it leaves error: no longer failing but still a warning, or OK again). A target with
// warnings set gets the same for warnings: a check starting to warn, and a warning easing or clearing. Notices are
// never sent.
func messageWants(ev Event, t *policy.NotifyTarget) bool {
	if !messageKinds[ev.Kind] {
		return false
	}
	if ev.Kind == Health || ev.Kind == HealthRestored {
		if len(ev.Health) == 0 {
			return false
		}
		is := func(level string) bool {
			return ev.Health[0].Type == level || ev.HealthWas != nil && ev.HealthWas.Type == level
		}
		return is("error") || t.Warnings && is("warning")
	}
	return true
}

// healthRank orders the health levels a message can name, most severe last.
var healthRank = map[string]int{"notice": 1, "warning": 2, "error": 3}

// messageText is the title, lines and level of a health, error or digest message. markdown writes Discord's
// markdown (bold labels, the error sample in a code block); without it the text is plain. The text depends on the
// event alone: every message target that gets the event gets the same message.
func messageText(ev Event, markdown bool) (string, []string, messageLevel) {
	switch ev.Kind {
	case Health:
		h := ev.Health[0]
		switch was := ev.HealthWas; {
		case h.Type == "error":
			return "Health check failing: " + h.Source, []string{h.Message}, levelBad
		case was != nil && was.Type == "error":
			return "Health check no longer failing, now " + h.Type + ": " + h.Source,
				[]string{"Now: " + h.Message, "Was: " + was.Message}, levelWarn
		case was != nil && healthRank[was.Type] > healthRank[h.Type]:
			return "Health " + was.Type + " eased, now " + h.Type + ": " + h.Source,
				[]string{"Now: " + h.Message, "Was: " + was.Message}, levelWarn
		}
		return "Health " + h.Type + ": " + h.Source, []string{h.Message}, levelWarn
	case HealthRestored:
		h := ev.Health[0]
		return "Health check OK again: " + h.Source, []string{"Was: " + h.Message}, levelGood
	case Digest:
		return digestText(ev.Digest, markdown)
	case PlayingReplaced, PlayingUnavailable, PlayingRestored:
		if ev.Playing != nil {
			return playingText(ev.Playing, markdown)
		}
	}
	return "", nil, levelInfo
}

// plainText is the title, lines and level of ev as plain text: the one builder ntfy and Telegram share, for every
// kind a message target gets. where names the destination in the test message ("topic", "chat").
func (n *Notifier) plainText(ctx context.Context, ev Event, where string) (string, []string, messageLevel, error) {
	switch ev.Kind {
	case Health, HealthRestored, Digest, PlayingReplaced, PlayingUnavailable, PlayingRestored:
		if title, lines, level := messageText(ev, false); title != "" {
			return title, lines, level, nil
		}
	case RequestAvailable, RequestPartial:
		name, ready, err := n.requestText(ctx, ev)
		if err != nil {
			return "", nil, levelInfo, err
		}
		line := name + " " + ready
		var who []string
		for _, u := range ev.Request.Users {
			who = append(who, requesterName(u))
		}
		if len(who) > 0 {
			line += ", requested by " + joinAnd(who)
		}
		title := "Request ready"
		if ev.Kind == RequestPartial {
			title = "Request partly ready"
		}
		return title, []string{line}, levelGood, nil
	case ProblemReported:
		title, lines := problemText(ev.Problem)
		return title, lines, levelWarn, nil
	case Test:
		return "Theatarr test message", []string{"This " + where + " gets Theatarr's notifications."}, levelInfo, nil
	}
	return "", nil, levelInfo, fmt.Errorf("notify: no message for %s", ev.Kind)
}

// playingText is the message about a file that broke while someone was playing it: what (the title and episodes),
// by what (the releases), and for whom it was playing. A same-release outcome is worded "readable again".
func playingText(o *outcome.Outcome, markdown bool) (string, []string, messageLevel) {
	esc := func(s string) string {
		if markdown {
			return escapeDiscord(s)
		}
		return s
	}
	subject := esc(o.Title)
	if len(o.Episodes) > 0 {
		subject += " " + esc(strings.Join(o.Episodes, ", "))
	}
	who := "while someone was watching"
	if o.Viewer != "" {
		who = "while " + esc(o.Viewer) + " was watching"
	}
	if o.Player != "" {
		who += " on " + esc(o.Player)
	}
	broke := "The file stopped working " + who + "."
	release := func(label, r string) []string {
		if r == "" {
			return nil
		}
		return []string{label + esc(r)}
	}
	switch o.Kind {
	case outcome.Replaced:
		lines := []string{broke + " Another copy is in its place: play it again."}
		lines = append(lines, release("Was: ", o.OldRelease)...)
		return "Replaced while playing: " + subject, append(lines, release("Now: ", o.NewRelease)...), levelGood
	case outcome.SameRelease:
		again := " The same release reads again"
		if o.Account != "" {
			again += " from " + esc(o.Account)
		}
		lines := []string{broke + again + ": play it again."}
		return "Readable again: " + subject, append(lines, release("Release: ", o.OldRelease)...), levelGood
	case outcome.Unavailable:
		lines := []string{broke + " No other copy is available right now."}
		if o.Outage {
			lines[0] = broke + " The debrid account did not answer and no other copy is available right now."
		}
		return "No other copy: " + subject, append(lines, release("Release: ", o.OldRelease)...), levelWarn
	case outcome.Restored:
		lines := []string{"The copy that stopped working " + who + " works again and is better than its replacement, so it is back in place."}
		return "Earlier copy put back: " + subject, append(lines, release("Release: ", o.OldRelease)...), levelGood
	}
	return "", nil, levelInfo
}

// problemText is a problem report's message: "Problem reported: <Title> S01E02", and "<what> · reported by <name>"
// with the note on a line of its own.
func problemText(p *Problem) (string, []string) {
	subject := p.Name
	if p.Episode != "" {
		subject += " " + p.Episode
	}
	lines := []string{p.Label + " · reported by " + requesterName(p.Reporter)}
	if p.Note != "" {
		lines = append(lines, p.Note)
	}
	return "Problem reported: " + subject, lines
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
		lines = append(lines, "Quiet day: nothing grabbed, imported, failed or blocklisted; no dead links or health problems.")
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
		lines = append(lines, "Theatarr is paused: nothing is downloaded or linked.")
	}
	if d.GrabbingOff || d.Accounts != nil || len(d.Health) > 0 {
		level = levelWarn
	}
	return "Theatarr daily digest · " + d.Day, lines, level
}

// requestText is the subject of a request-ready message, "<Title> (year)", and what happened to it ("is ready in
// the library", or "is partly ready in the library" for a partial series).
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
	ready = "is ready in the library"
	if ev.Kind == RequestPartial {
		ready = "is partly ready in the library"
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
