package notify

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Discord targets (notify.targets.*.format: discord) get Discord webhook messages instead of the theatarr.* JSON: instant
// health alerts (a check failing, and leaving error) and error alerts, the daily digest, request ready and the test. Messages respect Discord's limits (content
// 2000 characters, 10 embeds, 6000 characters of embed text, a description 4096): lines that do not fit become
// "and N more". allowed_mentions names only the requesters a request-ready message mentions, so a message can never
// ping anyone else (no @everyone, roles or other users).

// Discord limits.
const (
	discordContentMax     = 2000
	discordEmbedsMax      = 10
	discordEmbedTotalMax  = 6000
	discordDescriptionMax = 4096
	discordTitleMax       = 256
)

// Embed colours.
const (
	discordRed   = 0xd9534f
	discordGreen = 0x3fa45b
	discordBlue  = 0x4a7bd0
	discordAmber = 0xe0a030
)

type discordMessage struct {
	Content         string          `json:"content,omitempty"`
	Embeds          []discordEmbed  `json:"embeds,omitempty"`
	AllowedMentions discordMentions `json:"allowed_mentions"`
}

type discordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

type discordMentions struct {
	// Parse is always empty: no @everyone, @here or role mentions from text.
	Parse []string `json:"parse"`
	Users []string `json:"users,omitempty"`
}

// embedLines is an embed before fitting: its description is lines.
type embedLines struct {
	title string
	lines []string
	color int
}

// discordKinds are the events a Discord target gets.
var discordKinds = map[Kind]bool{Health: true, HealthRestored: true, Error: true, Digest: true,
	RequestAvailable: true, RequestPartial: true, Test: true}

// discordWants reports whether a Discord target gets ev: its kinds, and health only for checks in error (an alert
// when one fails, and one when it leaves error: no longer failing but still a warning, or OK again). A warning
// clearing after that is not announced.
func discordWants(ev Event) bool {
	if !discordKinds[ev.Kind] {
		return false
	}
	if ev.Kind == Health || ev.Kind == HealthRestored {
		return len(ev.Health) > 0 && (ev.Health[0].Type == "error" || ev.HealthWas != nil && ev.HealthWas.Type == "error")
	}
	return true
}

// discordPayload is the Discord message for ev.
func (n *Notifier) discordPayload(ctx context.Context, ev Event) (discordMessage, error) {
	at := ev.At.UTC().Format(time.RFC3339)
	switch ev.Kind {
	case Health:
		h := ev.Health[0]
		if h.Type != "error" {
			return fitDiscord("", []embedLines{{title: "Health check no longer failing, now " + h.Type + ": " + h.Source,
				lines: []string{"Now: " + h.Message, "Was: " + ev.HealthWas.Message}, color: discordAmber}}, nil, at), nil
		}
		return fitDiscord("", []embedLines{{title: "Health check failing: " + h.Source, lines: []string{h.Message},
			color: discordRed}}, nil, at), nil
	case HealthRestored:
		h := ev.Health[0]
		return fitDiscord("", []embedLines{{title: "Health check OK again: " + h.Source, lines: []string{"Was: " + h.Message},
			color: discordGreen}}, nil, at), nil
	case Error:
		e := ev.Error
		title := "New error: " + e.Component
		if e.Reopened {
			title = "Error back after it was resolved: " + e.Component
		}
		lines := []string{"```" + strings.ReplaceAll(e.Sample, "```", "'''") + "```",
			fmt.Sprintf("Source %s · seen %d times · Admin › System › Errors #%d", e.Source, e.Count, e.ID)}
		return fitDiscord("", []embedLines{{title: title, lines: lines, color: discordRed}}, nil, at), nil
	case Digest:
		return fitDiscord("", []embedLines{digestEmbed(ev.Digest)}, nil, at), nil
	case RequestAvailable, RequestPartial:
		return n.discordRequest(ctx, ev)
	case Test:
		return fitDiscord("Theatarr test message: this channel gets Theatarr's notifications.", nil, nil, at), nil
	}
	return discordMessage{}, fmt.Errorf("notify: no Discord message for %s", ev.Kind)
}

// discordRequest is "<Title> (year) is ready in Plex, requested by <name>", mentioning the requesters who have a
// Discord user id.
func (n *Notifier) discordRequest(ctx context.Context, ev Event) (discordMessage, error) {
	r := ev.Request
	name, year := r.Name, 0
	if r.TitleID > 0 {
		var tn string
		err := n.st.Reader().QueryRowContext(ctx, `SELECT name, COALESCE(year, 0) FROM title WHERE id = ?`, r.TitleID).
			Scan(&tn, &year)
		if err != nil && err != sql.ErrNoRows {
			return discordMessage{}, err
		}
		if tn != "" {
			name = tn
		}
	}
	if year > 0 && !strings.HasSuffix(name, "("+strconv.Itoa(year)+")") {
		name += " (" + strconv.Itoa(year) + ")"
	}
	var who, mentions []string
	for _, u := range r.Users {
		label := u.DisplayName
		if label == "" {
			label = u.Username
		}
		if u.DiscordID != "" {
			label += " (<@" + u.DiscordID + ">)"
			mentions = append(mentions, u.DiscordID)
		}
		who = append(who, label)
	}
	ready := "is ready in Plex"
	if ev.Kind == RequestPartial {
		ready = "is partly ready in Plex"
	}
	content := "**" + escapeDiscord(name) + "** " + ready
	if len(who) > 0 {
		content += ", requested by " + joinAnd(who)
	}
	return fitDiscord(content, nil, mentions, ev.At.UTC().Format(time.RFC3339)), nil
}

// digestEmbed lays out the daily digest.
func digestEmbed(d *DigestSummary) embedLines {
	e := embedLines{title: "Theatarr daily digest · " + d.Day, color: discordBlue}
	if d.Quiet() {
		e.lines = append(e.lines, "Quiet day: nothing grabbed, imported, failed or blocklisted; no dead links, open errors or health problems.")
	} else {
		line := fmt.Sprintf("**Last 24 h:** %d grabbed · %d imported · %d failed · %d blocklisted · %d dead links found",
			d.Grabs, d.Imports, d.Failed, d.Blocklisted, d.DeadLinks)
		if d.DeadLinks > 0 {
			line += fmt.Sprintf(", %d still dead", d.StillDead)
		}
		e.lines = append(e.lines, line)
		if len(d.Grabbed) > 0 {
			e.lines = append(e.lines, "**Grabbed:** "+strings.Join(d.Grabbed, ", ")+more(d.Grabs, len(d.Grabbed)))
		}
		if len(d.Imported) > 0 {
			e.lines = append(e.lines, "**Imported:** "+strings.Join(d.Imported, ", ")+more(d.Imports, len(d.Imported)))
		}
		if d.OpenErrors > 0 {
			e.lines = append(e.lines, fmt.Sprintf("**Open error groups:** %d (Admin › System › Errors)", d.OpenErrors))
			for _, l := range d.Errors {
				e.lines = append(e.lines, "• "+l)
			}
		}
		if d.Accounts != nil {
			e.lines = append(e.lines, "**Debrid accounts:** "+d.Accounts.Message)
		}
		if len(d.Health) > 0 {
			e.lines = append(e.lines, fmt.Sprintf("**Health checks not OK:** %d", len(d.Health)))
			for _, h := range d.Health {
				e.lines = append(e.lines, "• "+h.Type+": "+h.Message)
			}
		}
	}
	if d.GrabbingOff {
		e.lines = append(e.lines, "Grabbing is off (router.enabled is false): Theatarr adds nothing to the debrid providers.")
		e.color = discordAmber
	}
	if d.Accounts != nil || len(d.Health) > 0 || d.OpenErrors > 0 {
		e.color = discordAmber
	}
	return e
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

// escapeDiscord keeps a title's markdown characters literal.
func escapeDiscord(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`, ">", `\>`).Replace(s)
}

// fitDiscord builds a message within Discord's limits: content up to 2000 characters, at most 10 embeds, a title up
// to 256 and a description up to 4096 characters, and 6000 characters of embed text in all. Lines that do not fit are
// dropped from the end and counted ("and N more").
func fitDiscord(content string, embeds []embedLines, mentions []string, at string) discordMessage {
	m := discordMessage{Content: clip(content, discordContentMax), AllowedMentions: discordMentions{Parse: []string{},
		Users: mentions}}
	if len(embeds) > discordEmbedsMax {
		dropped := len(embeds) - discordEmbedsMax + 1
		embeds = append(embeds[:discordEmbedsMax-1:discordEmbedsMax-1], embedLines{
			lines: []string{fmt.Sprintf("and %d more", dropped)}})
	}
	budget := discordEmbedTotalMax
	for _, e := range embeds {
		title := clip(e.title, min(discordTitleMax, budget))
		budget -= utf8.RuneCountInString(title)
		desc := fitLines(e.lines, min(discordDescriptionMax, budget))
		budget -= utf8.RuneCountInString(desc)
		if title == "" && desc == "" {
			break
		}
		m.Embeds = append(m.Embeds, discordEmbed{Title: title, Description: desc, Color: e.color, Timestamp: at})
	}
	return m
}

// fitLines joins lines with newlines in at most limit characters; the lines that do not fit become "and N more".
func fitLines(lines []string, limit int) string {
	if limit <= 0 {
		return ""
	}
	full := strings.Join(lines, "\n")
	if utf8.RuneCountInString(full) <= limit {
		return full
	}
	if len(lines) == 1 {
		return clip(lines[0], limit)
	}
	for keep := len(lines) - 1; keep > 0; keep-- {
		s := strings.Join(lines[:keep], "\n") + fmt.Sprintf("\nand %d more", len(lines)-keep)
		if utf8.RuneCountInString(s) <= limit {
			return s
		}
	}
	// Not even the first line fits whole: cut it.
	tail := fmt.Sprintf("and %d more", len(lines)-1)
	if first := clip(lines[0], limit-utf8.RuneCountInString(tail)-1); first != "" {
		return first + "\n" + tail
	}
	return clip(tail, limit)
}

// clip cuts s to at most n characters, ending in "…" when cut.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
