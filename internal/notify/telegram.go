package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// Telegram targets (notify.targets.*.format: telegram) get the messages a Discord target gets, as the plain text ntfy
// gets (plainText), sent with the Bot API's sendMessage to the target's chat_id. The target has no URL: requests go
// to policy.TelegramAPI, registered with the gateway's webhook class like any target. The bot token is the target's
// own secret (bot_token, or bot_token_env), never the Authorization-header secret; it and the chat id are added only
// when a delivery is sent (telegramRequest), so no outbox row holds them, and the gateway shows the request as scheme
// and host and masks the token (policy.SecretEnvs).
//
// Limits, from Telegram's documentation (not tried against Telegram's servers):
//   - sendMessage text is 1 to 4096 characters (https://core.telegram.org/bots/api#sendmessage). The text is cut to
//     fit, counting UTF-16 code units (how the Bot API counts text positions,
//     https://core.telegram.org/bots/api#messageentity), and lines that do not fit become "and N more".
//   - A bot can send at most about 20 messages a minute to one group, and should avoid more than one a second in one
//     chat (https://core.telegram.org/bots/faq#my-bot-is-hitting-limits-how-do-i-avoid-this): one message every 3 s
//     per chat, Send test included.
//   - A refusal for flood control carries parameters.retry_after, the seconds to wait
//     (https://core.telegram.org/bots/api#responseparameters), in the body and without a Retry-After header: the
//     gateway's back-off of the target, the chat's next slot and the delivery all take that one deadline (at most an
//     hour), and a retry the gateway refuses before sending waits for its deadline without counting an attempt.

// telegramTextMax is sendMessage's text limit.
const telegramTextMax = 4096

// telegramPace is the time between two messages to one chat.
const telegramPace = 3 * time.Second

// telegramMaxRetryAfter caps a 429's retry_after, as the gateway caps a Retry-After header.
const telegramMaxRetryAfter = time.Hour

// telegramEventPrefix starts the event name of every Telegram delivery ("telegram.health").
const telegramEventPrefix = "telegram."

// telegramMessage is the stored body of a Telegram delivery: sendMessage without its chat_id.
type telegramMessage struct {
	Text string `json:"text"`
}

// telegramPayload is the Telegram message for ev.
func (n *Notifier) telegramPayload(ctx context.Context, ev Event) (telegramMessage, error) {
	title, lines, _, err := n.plainText(ctx, ev, "chat")
	if err != nil {
		return telegramMessage{}, err
	}
	return fitTelegram(title, lines), nil
}

// fitTelegram is the title and the lines in at most telegramTextMax characters; never empty.
func fitTelegram(title string, lines []string) telegramMessage {
	all := append([]string{title}, lines...)
	text := fitMeasured(all, telegramTextMax, func(s string) int { return len(utf16.Encode([]rune(s))) })
	if strings.TrimSpace(text) == "" {
		text = "Theatarr"
	}
	return telegramMessage{Text: text}
}

// telegramRequest turns a stored Telegram delivery into the sendMessage request for target t: the URL with the bot
// token and the body with the chat id. A delivery queued while the target had another format is dropped. The errors
// name neither token nor chat.
func telegramRequest(ctx context.Context, api string, t *policy.NotifyTarget, token, event string, stored []byte) (*http.Request, error) {
	if !strings.HasPrefix(event, telegramEventPrefix) {
		return nil, fmt.Errorf("queued before the target became a Telegram target: %w", errGone)
	}
	if token == "" {
		return nil, fmt.Errorf("the Telegram target has no bot token: %w", errGone)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(stored, &body); err != nil {
		return nil, fmt.Errorf("not a Telegram message: %w", errGone)
	}
	chat, err := json.Marshal(t.ChatID)
	if err != nil {
		return nil, err
	}
	body["chat_id"] = chat
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(api)
	if err != nil {
		return nil, err
	}
	u.Path = "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(b)))
	if err != nil {
		return nil, errors.New("the Telegram request could not be built")
	}
	return req, nil
}

// errPaced: the chat's next message slot is later; the delivery waits for it without counting an attempt.
type errPaced struct{ until time.Time }

func (e errPaced) Error() string { return "waiting for the chat's next message slot" }

// telegramSlot takes chat's next message slot. With wait (Send test) it sleeps until the slot comes, at most the
// pace (a longer wait, after a 429, is said in the error instead); without, a slot that is not free yet answers
// errPaced, so the shared sender goes on with other deliveries.
func (n *Notifier) telegramSlot(ctx context.Context, chat string, wait bool) error {
	n.tgMu.Lock()
	now := n.now()
	at := n.tgNext[chat]
	if at.After(now) && !wait {
		n.tgMu.Unlock()
		return errPaced{until: at}
	}
	if d := at.Sub(now); d > telegramPace {
		n.tgMu.Unlock()
		return fmt.Errorf("asked by Telegram to wait: try again in %d s", int((d+time.Second-1)/time.Second))
	}
	if at.Before(now) {
		at = now
	}
	if n.tgNext == nil {
		n.tgNext = map[string]time.Time{}
	}
	n.tgNext[chat] = at.Add(telegramPace)
	n.tgMu.Unlock()
	if d := at.Sub(now); d > 0 {
		return n.sleep(ctx, d)
	}
	return nil
}

// telegramWaitUntil moves chat's next slot to at (a 429's retry_after, a gateway back-off).
func (n *Notifier) telegramWaitUntil(chat string, at time.Time) {
	n.tgMu.Lock()
	defer n.tgMu.Unlock()
	if n.tgNext == nil {
		n.tgNext = map[string]time.Time{}
	}
	if at.After(n.tgNext[chat]) {
		n.tgNext[chat] = at
	}
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfterSetter is the gateway's SetRetryAfter: a 429 whose body named its wait backs the target off that long.
type retryAfterSetter interface {
	SetRetryAfter(ctx context.Context, t gateway.Target, d time.Duration)
}

// sendTelegram posts a stored Telegram delivery to target key (token is the resolved bot token). A 429's
// parameters.retry_after is read before the answer is dropped and becomes the one deadline of the gateway's
// back-off, the chat's slot and the delivery (errStatus.after). A request the gateway refuses before sending with a
// time to retry is errPaced (no attempt counted) outside Send test.
func (n *Notifier) sendTelegram(ctx context.Context, key string, t *policy.NotifyTarget, token, event string, body []byte, wait bool) (int, error) {
	req, err := telegramRequest(ctx, n.tgAPI, t, token, event, body)
	if err != nil {
		return 0, err
	}
	if err := n.telegramSlot(ctx, t.ChatID, wait); err != nil {
		return 0, err
	}
	target := gateway.Target{Class: gateway.ClassWebhook, Key: key}
	resp, err := n.post(ctx, target, req)
	if err != nil {
		var ref *gateway.Refused
		if errors.As(err, &ref) && !ref.Sent && !ref.RetryAt.IsZero() {
			n.telegramWaitUntil(t.ChatID, ref.RetryAt)
			if !wait {
				return 0, errPaced{until: ref.RetryAt}
			}
		}
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp.StatusCode, nil
	}
	st := errStatus{code: resp.StatusCode}
	if resp.StatusCode == http.StatusTooManyRequests {
		if d := retryAfterBody(data); d > 0 {
			st.after = d
			n.telegramWaitUntil(t.ChatID, n.now().Add(d))
			if g, ok := n.doer.(retryAfterSetter); ok {
				g.SetRetryAfter(ctx, target, d)
			}
		}
	}
	return resp.StatusCode, st
}

// retryAfterBody is the wait a Bot API answer names in parameters.retry_after, at most telegramMaxRetryAfter; 0 when
// it names none.
func retryAfterBody(data []byte) time.Duration {
	var a struct {
		Parameters struct {
			RetryAfter float64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(data, &a) != nil || !(a.Parameters.RetryAfter > 0) {
		return 0
	}
	if a.Parameters.RetryAfter >= telegramMaxRetryAfter.Seconds() {
		return telegramMaxRetryAfter
	}
	return time.Duration(math.Ceil(a.Parameters.RetryAfter)) * time.Second
}
