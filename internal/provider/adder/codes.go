package adder

import (
	"fmt"
	"time"
)

// Code is what a refused or failed add means for the router. The first four are decypharr's error codes
// (internal/customerror/http.go @ 917cfa2); the rest separate what decypharr folds into plain errors.
type Code string

const (
	// CodeNotCached: the provider does not hold the content and was asked to add only if it did (TorBox
	// DOWNLOAD_NOT_CACHED). A content verdict for this provider: try the next one.
	CodeNotCached Code = "torrent_not_cached"
	// CodeBlocked: the provider refuses this release (RD 451 on a name its rule refuses, too big, not allowed;
	// TorBox too large). A content verdict for this provider: try the next one.
	CodeBlocked Code = "torrent_blocked"
	// CodeTooManyActive: the account's active download slots are full (RD 509 / 21, AD MAGNET_TOO_MANY_ACTIVE,
	// TorBox ACTIVE_LIMIT). Back the provider off and try the next.
	CodeTooManyActive Code = "too_many_active_downloads"
	// CodeTrafficExceeded: an account allowance is spent (RD 23 / 36, AD MAGNET_TOO_MANY, TorBox MONTHLY_LIMIT and
	// COOLDOWN_LIMIT). Back off to the provider's maximum and try the next.
	CodeTrafficExceeded Code = "traffic_exceeded"
	// CodeRateLimited: too many requests (HTTP 429, RD 5 / 34). Back off and try the next.
	CodeRateLimited Code = "rate_limited"
	// CodeAccountHold: Real-Debrid answered 451 infringing_file on a name its rule does not refuse. RD gives the same
	// answer during short blocks of every add on the account (seconds to minutes, after bursts), and to single
	// releases it always refuses. The router blocks the hash on the provider and holds the account only when 451s
	// come on different hashes (router note451).
	CodeAccountHold Code = "account_hold"
	// CodeAuth: the key or the account cannot add (bad token, not premium, location blocked). Back off to the maximum.
	CodeAuth Code = "auth"
	// CodeInvalid: the provider found the magnet invalid. A verdict for this provider.
	CodeInvalid Code = "invalid"
	// CodeUnavailable: the request was not sent (gateway back-off, budget, VPN) or failed in transit or with a 5xx.
	// Back off (or wait for RetryAt) and try the next.
	CodeUnavailable Code = "unavailable"
	// CodeUnknown: an answer this package does not recognise. Treated like CodeUnavailable.
	CodeUnknown Code = "unknown"
)

// Content reports whether the code is a verdict on the release at this provider (counts towards a miss on every
// provider), rather than a condition of the provider or the account.
func (c Code) Content() bool { return c == CodeNotCached || c == CodeBlocked || c == CodeInvalid }

// Error is a refused or failed add or selection. Message is sanitised (provider.Redactor.Sanitize); the request URL,
// form and key are never in it.
type Error struct {
	Provider string
	Code     Code
	// Status is the HTTP status, 0 when nothing was received.
	Status int
	// ProviderCode is the provider's own code (RD error_code, AD and TorBox error strings).
	ProviderCode string
	Message      string
	// RetryAt is when the provider said, or the gateway knows, the same request could pass.
	RetryAt time.Time
	// Err is the underlying error (transport, gateway refusal), when any.
	Err error
	// Refused: the answer was the provider's own error envelope with an error code (AllDebrid's status error,
	// TorBox's success false), so the add was not taken whatever the HTTP status and code; a code we do not know is a
	// definite refusal, not an answer that may hide an accepted add. This alone is no verdict on the release:
	// a per-release miss also requires a recognised content Code.
	Refused bool
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s", e.Provider, e.Code)
	if e.Status != 0 {
		s += fmt.Sprintf(" (HTTP %d", e.Status)
		if e.ProviderCode != "" {
			s += " " + e.ProviderCode
		}
		s += ")"
	} else if e.ProviderCode != "" {
		s += " (" + e.ProviderCode + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// newError keeps add classification local and shares value-only sanitation with the read clients.
func (p *poster) newError(code Code, status int, pcode, msg string) *Error {
	pcode, msg = p.redact.Sanitize(pcode, msg)
	return &Error{Provider: p.provider, Code: code, Status: status, ProviderCode: pcode, Message: msg}
}
