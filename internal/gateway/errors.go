package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

// Sentinel errors. A *Refused matches the one for its Reason with errors.Is.
var (
	ErrUnknownTarget    = errors.New("gateway: unknown target")
	ErrHostNotAllowed   = errors.New("gateway: host not in the allowlist")
	ErrVPNRequired      = errors.New("gateway: egress is not confirmed through the VPN")
	ErrBackoff          = errors.New("gateway: backing off")
	ErrBudget           = errors.New("gateway: budget exhausted")
	ErrResponseTooLarge = errors.New("gateway: response too large")
)

// Reason is why a request was refused. It is also the reason label on theatarr_gateway_refused_total.
type Reason string

const (
	ReasonUnknownTarget Reason = "unknown_target"
	ReasonHost          Reason = "host"
	ReasonVPN           Reason = "vpn"
	ReasonBackoff       Reason = "backoff"
	ReasonBudget        Reason = "budget"
	ReasonTooLarge      Reason = "too_large"
)

var reasonErr = map[Reason]error{
	ReasonUnknownTarget: ErrUnknownTarget,
	ReasonHost:          ErrHostNotAllowed,
	ReasonVPN:           ErrVPNRequired,
	ReasonBackoff:       ErrBackoff,
	ReasonBudget:        ErrBudget,
	ReasonTooLarge:      ErrResponseTooLarge,
}

// Refused is a request the gateway did not send.
type Refused struct {
	// Sent reports that the original request was sent before a redirect was refused.
	Sent   bool
	Reason Reason
	// Target is "class:key", e.g. "indexer:dmm".
	Target string
	// Detail says what failed, e.g. "host evil.example" or "daily cap 5000 reached for budget torrentio".
	Detail string
	// RetryAt, when set, is the earliest time the same request could pass.
	RetryAt time.Time
}

func (r *Refused) Error() string {
	s := fmt.Sprintf("%v: %s: %s", reasonErr[r.Reason], r.Target, r.Detail)
	if !r.RetryAt.IsZero() {
		s += " (retry at " + r.RetryAt.UTC().Format(time.RFC3339) + ")"
	}
	return s
}

// Is makes errors.Is(err, ErrBudget) and friends work.
func (r *Refused) Is(target error) bool { return reasonErr[r.Reason] == target }

var errTooManyRedirects = fmt.Errorf("stopped after %d redirects", maxRedirects)

// transportError is a transport failure reduced to a fixed category. Go's transport errors can quote text the server
// supplied (a malformed redirect Location, for one), which may hold a credential; none of it is kept.
type transportError struct {
	msg     string
	timeout bool
}

func (e *transportError) Error() string   { return e.msg }
func (e *transportError) Timeout() bool   { return e.timeout }
func (e *transportError) Temporary() bool { return e.timeout }

// safeTransportError maps a client error to a safe category. Cancellation and deadline errors are returned as the
// context sentinels so errors.Is keeps working.
func safeTransportError(err error) error {
	var ne net.Error
	var dns *net.DNSError
	var op *net.OpError
	var tlsErr *tls.RecordHeaderError
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.As(err, &ne) && ne.Timeout():
		return &transportError{msg: "timeout", timeout: true}
	case errors.As(err, &dns):
		return &transportError{msg: "host lookup failed"}
	case errors.Is(err, syscall.ECONNREFUSED):
		return &transportError{msg: "connection refused"}
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &transportError{msg: "connection closed"}
	case errors.As(err, &tlsErr), errors.As(err, &certErr):
		return &transportError{msg: "TLS failed"}
	case errors.As(err, &op):
		return &transportError{msg: "connection failed"}
	case errors.Is(err, errTooManyRedirects):
		return &transportError{msg: errTooManyRedirects.Error()}
	case strings.Contains(err.Error(), "Location"):
		return &transportError{msg: "invalid redirect"}
	}
	return &transportError{msg: "transport error"}
}
