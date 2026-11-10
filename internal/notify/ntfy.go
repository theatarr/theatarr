package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// ntfy targets (notify.targets.*.format: ntfy) get the messages a Discord target gets, as plain text, published with
// ntfy's JSON form: a POST to the server address with topic, title, message, priority and tags in the body. The
// target URL is the server address followed by the topic; the topic is split off and added to the body only when a
// delivery is sent (ntfyRequest), so the outbox never stores it. An access token is the target's Authorization
// secret, sent as "Bearer <token>".
//
// This follows ntfy's published API as remembered; it has not been run against an ntfy server (docs/design/devices.md
// lists what is assumed).

// ntfy limits, kept under what the server is assumed to accept (a message of 4096 bytes; a longer one would be
// stored as an attachment). Lines that do not fit become "and N more".
const (
	ntfyMessageMax = 3800 // bytes
	ntfyTitleMax   = 250  // bytes
)

// ntfy priorities (1 lowest to 5 highest; 3 is the default).
const (
	ntfyDefault = 3
	ntfyHigh    = 4
)

// ntfyMessage is the stored body of an ntfy delivery: the JSON publish without its topic.
type ntfyMessage struct {
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
}

// ntfyPayload is the ntfy message for ev: the Discord message's text without markdown or mentions. A failing health
// check and an error are high priority; the tag is a warning sign for a problem and a check mark for good news.
func (n *Notifier) ntfyPayload(ctx context.Context, ev Event) (ntfyMessage, error) {
	switch ev.Kind {
	case Health, HealthRestored, Error, Digest:
		title, lines, level := messageText(ev, false)
		return fitNtfy(title, lines, level), nil
	case RequestAvailable, RequestPartial:
		name, ready, err := n.requestText(ctx, ev)
		if err != nil {
			return ntfyMessage{}, err
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
		return fitNtfy(title, []string{line}, levelGood), nil
	case Test:
		return fitNtfy("Theatarr test message", []string{"This topic gets Theatarr's notifications."}, levelInfo), nil
	}
	return ntfyMessage{}, fmt.Errorf("notify: no ntfy message for %s", ev.Kind)
}

// fitNtfy builds a message within the limits. The message is never empty (ntfy would replace an empty one with its
// own placeholder): without lines it repeats the title.
func fitNtfy(title string, lines []string, level messageLevel) ntfyMessage {
	m := ntfyMessage{Title: fitBytes([]string{title}, ntfyTitleMax), Message: fitBytes(lines, ntfyMessageMax),
		Priority: ntfyDefault}
	if strings.TrimSpace(m.Message) == "" {
		m.Message = m.Title
	}
	switch level {
	case levelBad:
		m.Priority, m.Tags = ntfyHigh, []string{"warning"}
	case levelWarn:
		m.Tags = []string{"warning"}
	case levelGood:
		m.Tags = []string{"white_check_mark"}
	}
	return m
}

// fitBytes is fitLines with the limit in bytes: ntfy counts bytes, and a title outside ASCII takes several each.
func fitBytes(lines []string, limit int) string {
	chars := limit
	s := fitLines(lines, chars)
	for len(s) > limit && chars > 1 {
		chars = min(chars-1, chars*limit/len(s))
		s = fitLines(lines, chars)
	}
	return s
}

// ntfyEventPrefix starts the event name of every ntfy delivery ("ntfy.health").
const ntfyEventPrefix = "ntfy."

// ntfyRequest turns a stored ntfy delivery into the request for the target URL in force: the server address to POST
// to and the body with the URL's topic added. A delivery that was queued while the target had another format (its
// event is not ntfy.*) is not an ntfy message and is dropped. The errors name neither the URL nor the topic (the URL
// is a write-only credential).
func ntfyRequest(targetURL, event string, stored []byte) (string, []byte, error) {
	if !strings.HasPrefix(event, ntfyEventPrefix) {
		return "", nil, fmt.Errorf("queued before the target became an ntfy target: %w", errGone)
	}
	server, topic, ok := policy.NtfyEndpoint(targetURL)
	if !ok {
		return "", nil, fmt.Errorf("the ntfy target's URL has no topic: %w", errGone)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(stored, &body); err != nil {
		return "", nil, fmt.Errorf("not an ntfy message: %w", errGone)
	}
	t, err := json.Marshal(topic)
	if err != nil {
		return "", nil, err
	}
	body["topic"] = t
	out, err := json.Marshal(body)
	return server, out, err
}

// ntfyAuthorization is the Authorization header for an ntfy target's secret: a value that already names a scheme
// ("Bearer tk_...", "Basic ...") is sent as it is, a bare access token as "Bearer <token>".
func ntfyAuthorization(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || strings.Contains(secret, " ") {
		return secret
	}
	return "Bearer " + secret
}
