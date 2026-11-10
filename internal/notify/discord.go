package notify

import (
	"context"
	"fmt"
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

// discordColor is the embed colour of a message level.
var discordColor = map[messageLevel]int{levelInfo: discordBlue, levelGood: discordGreen, levelWarn: discordAmber,
	levelBad: discordRed}

// discordPayload is the Discord message for ev.
func (n *Notifier) discordPayload(ctx context.Context, ev Event) (discordMessage, error) {
	at := ev.At.UTC().Format(time.RFC3339)
	switch ev.Kind {
	case Health, HealthRestored, Error, Digest:
		title, lines, level := messageText(ev, true)
		return fitDiscord("", []embedLines{{title: title, lines: lines, color: discordColor[level]}}, nil, at), nil
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
	name, ready, err := n.requestText(ctx, ev)
	if err != nil {
		return discordMessage{}, err
	}
	var who, mentions []string
	for _, u := range ev.Request.Users {
		label := requesterName(u)
		if u.DiscordID != "" {
			label += " (<@" + u.DiscordID + ">)"
			mentions = append(mentions, u.DiscordID)
		}
		who = append(who, label)
	}
	content := "**" + escapeDiscord(name) + "** " + ready
	if len(who) > 0 {
		content += ", requested by " + joinAnd(who)
	}
	return fitDiscord(content, nil, mentions, ev.At.UTC().Format(time.RFC3339)), nil
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
