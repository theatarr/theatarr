package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// Provider is a read-only debrid account. It has no method that adds, selects, deletes, restarts or unrestricts
// anything, and the clients can only send GET requests (see getter). Writes are a separate type (internal/provider/adder).
type Provider interface {
	// Key is the policy key (providers[Key]); Kind is realdebrid, alldebrid or torbox.
	Key() string
	Kind() string
	// Account reads the account: plan, expiry, limits. No e-mail or username is kept.
	Account(ctx context.Context) (*Account, error)
	// Torrents lists every torrent on the account, paging as the provider requires. It fails rather than return a
	// partial list, so a nil error means the list is complete.
	Torrents(ctx context.Context) ([]Torrent, error)
	// Torrent reads one torrent, with its files when the provider gives them.
	Torrent(ctx context.Context, id string) (*Torrent, error)
	// Cache says what instant-availability check the provider offers.
	Cache() CacheSupport
	// CheckCached asks the provider which hashes it has cached. It returns ErrCacheUnsupported where the provider
	// has no such endpoint. Which hashes may be asked about is the caller's rule (internal/indexer/cachedon.go).
	CheckCached(ctx context.Context, q CacheQuery) (map[string]bool, error)
}

// Account is what a provider says about the account. Zero values mean "not reported".
type Account struct {
	Provider string    `json:"provider"`
	Plan     string    `json:"plan"`
	Premium  bool      `json:"premium"`
	Expires  time.Time `json:"expires,omitzero"`
	Points   int       `json:"points,omitempty"`
	// ActiveTorrents and MaxActiveTorrents are -1 when the provider doesn't report them.
	ActiveTorrents    int       `json:"active_torrents"`
	MaxActiveTorrents int       `json:"max_active_torrents"`
	CooldownUntil     time.Time `json:"cooldown_until,omitzero"`
	// Notes are provider-specific limits worth showing (AllDebrid's limited hoster quotas).
	Notes []string `json:"notes,omitempty"`
}

// State is a torrent status normalised across providers.
type State string

const (
	StateQueued      State = "queued"
	StateDownloading State = "downloading"
	StateDownloaded  State = "downloaded"
	StateError       State = "error"
	StateUnknown     State = "unknown"
)

// Torrent is one torrent on a provider account. It holds no download links: Links is only their number.
type Torrent struct {
	ID   string `json:"id"`
	Hash string `json:"hash"` // lower-case hex, as the provider gave it (lower-cased)
	Name string `json:"name"`
	// Bytes is the size the provider reports (RD: of the selected files).
	Bytes int64 `json:"bytes"`
	// Status is the provider's raw status; State is the normalised one.
	Status   string    `json:"status"`
	State    State     `json:"state"`
	Progress float64   `json:"progress"` // 0-100
	Added    time.Time `json:"added,omitzero"`
	// Links is the number of download links the provider reports, -1 when it doesn't say.
	Links int `json:"links"`
	// Files is nil when the call didn't return files (list calls on RD and AD).
	Files []File `json:"files,omitempty"`
	// OriginalName is the name the torrent itself declared, the name a mount such as zurg gives its folder; the
	// provider's Name may differ (Real-Debrid's can be a selected file's lower-cased name). Filled by Torrent only,
	// "" from list calls and when the provider gives none.
	OriginalName string `json:"original_name,omitempty"`
}

// File is one file of a torrent. Path is relative to the torrent.
type File struct {
	ID       string `json:"id,omitempty"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Selected bool   `json:"selected"`
}

// OriginalName is a torrent's own name read from its files, as zurg names a TorBox or AllDebrid folder: the folder
// every file path shares, else the single file's own name, else the provider's name. "" without files.
func OriginalName(files []File, name string) string {
	if len(files) == 0 {
		return ""
	}
	if dir, _, ok := strings.Cut(files[0].Path, "/"); ok && dir != "" {
		shared := true
		for _, f := range files[1:] {
			if d, _, ok := strings.Cut(f.Path, "/"); !ok || d != dir {
				shared = false
				break
			}
		}
		if shared {
			return dir
		}
	}
	if len(files) == 1 {
		return path.Base(files[0].Path)
	}
	return name
}

// CacheSupport describes a provider's instant-availability check.
type CacheSupport struct {
	Supported bool `json:"supported"`
	// Endpoint is the read-only endpoint used, when supported.
	Endpoint string `json:"endpoint,omitempty"`
	// Note says what the provider offers instead, when unsupported.
	Note string `json:"note"`
}

// CacheQuery is a cache check for hashes.
type CacheQuery struct {
	Hashes []string
}

// Errors.
var (
	// ErrCacheUnsupported: the provider has no instant-availability endpoint (RD since November 2024, AD).
	ErrCacheUnsupported = errors.New("provider: no cache check at this provider")
	// ErrAuth: the provider refused the API key.
	ErrAuth = errors.New("provider: authentication refused")
	// ErrNotFound: no such torrent.
	ErrNotFound = errors.New("provider: not found")
	// ErrIncomplete: a list could not be read completely (it kept shifting while paged).
	ErrIncomplete = errors.New("provider: incomplete list")
	// ErrNoKey: the policy's api_key_env is unset or empty.
	ErrNoKey = errors.New("provider: API key not set")
	// ErrUnavailable: the provider answered that it cannot serve the request now (AllDebrid MAINTENANCE or GENERIC,
	// TorBox DATABASE_ERROR, SERVER_ERROR or UNKNOWN_ERROR), whatever the HTTP status: AllDebrid and TorBox send these
	// in an error envelope on HTTP 200. Like a 5xx it says nothing about the torrent asked for.
	ErrUnavailable = errors.New("provider: unavailable")
	// ErrLocationBlocked: AllDebrid refused a login from a new location (AUTH_BLOCKED) until the account owner
	// approves it from the e-mail AllDebrid sent. It is also an ErrAuth.
	ErrLocationBlocked = &kindError{msg: "AllDebrid blocked this location; approve it from the email AllDebrid sent",
		parent: ErrAuth}
)

// kindError is a sentinel that also matches a broader one.
type kindError struct {
	msg    string
	parent error
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.parent }

// APIError is an error answer from a provider. It carries the provider's code and a short, sanitised message (see
// cleanMessage), never the request URL or the response body. Code and Message are sanitised once, when the answer
// is decoded (scrub); Error only formats them, since decoding the sanitised text again could turn an entity-encoded
// credential the redaction did not see back into the credential.
type APIError struct {
	Provider string
	Status   int
	Code     string
	Message  string
	kind     error // ErrAuth, ErrNotFound, ErrUnavailable or nil
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("%s: HTTP %d", e.Provider, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if k, ok := e.kind.(*kindError); ok {
		return s + ": " + k.msg
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *APIError) Unwrap() error { return e.kind }

// Doer sends a request for a target. *gateway.Gateway is the one implementation outside tests.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// New returns the client for providers[key] of the policy. apiKey is the value of its api_key_env.
func New(key string, pv *policy.Provider, apiKey string, d Doer) (Provider, error) {
	if pv == nil {
		return nil, fmt.Errorf("provider %q: not in the policy", key)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("provider %q: %w (%s)", key, ErrNoKey, pv.APIKeyEnv)
	}
	base, err := url.Parse(strings.TrimRight(pv.URL, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("provider %q: bad url", key)
	}
	g := &getter{provider: key, base: base, key: apiKey, doer: d, redact: NewRedactor(apiKey),
		target: gateway.Target{Class: gateway.ClassProvider, Key: key}}
	switch pv.Kind {
	case KindRealDebrid:
		return &realDebrid{g: g}, nil
	case KindAllDebrid:
		return newAllDebrid(g), nil
	case KindTorBox:
		return &torBox{g: g}, nil
	}
	return nil, fmt.Errorf("provider %q: unknown kind %q", key, pv.Kind)
}

// Provider kinds, as in the policy.
const (
	KindRealDebrid = "realdebrid"
	KindAllDebrid  = "alldebrid"
	KindTorBox     = "torbox"
)

// getter is the only way the clients reach a provider, and it can only send GET. It holds the key and puts it in the
// Authorization header (all three providers accept a bearer token), never in the URL.
type getter struct {
	provider string
	base     *url.URL
	key      string
	redact   *Redactor
	doer     Doer
	target   gateway.Target
}

// maxErrorBody bounds what is read from an error answer to find its code.
const maxErrorBody = 64 << 10

// get sends GET base+path?query through the gateway (target class provider) and returns the response. The caller
// closes the body. A non-2xx answer is decoded into an *APIError by decodeErr and the body closed.
func (g *getter) get(ctx context.Context, path string, query url.Values,
	decodeErr func(status int, body []byte) *APIError) (*http.Response, error) {
	u := *g.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: bad request", g.provider)
	}
	req.Header.Set("Authorization", "Bearer "+g.key)
	req.Header.Set("Accept", "application/json")
	resp, err := g.doer.Do(ctx, g.target, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", g.provider, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
	e := decodeErr(resp.StatusCode, body)
	if e == nil {
		e = &APIError{Status: resp.StatusCode}
	}
	e.Provider, e.Status = g.provider, resp.StatusCode
	scrub(e, g.redact)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if e.kind == nil {
			e.kind = ErrAuth
		}
	case resp.StatusCode == http.StatusNotFound:
		if e.kind == nil {
			e.kind = ErrNotFound
		}
	}
	return nil, e
}

// scrub sanitizes a provider answer with this client's generation-local credential matchers.
func scrub(e *APIError, redact *Redactor) {
	e.Code, e.Message = redact.Sanitize(e.Code, e.Message)
}

// getJSON is get plus decoding a 2xx body into out. A 204 leaves out untouched and returns no error.
func (g *getter) getJSON(ctx context.Context, path string, query url.Values,
	decodeErr func(int, []byte) *APIError, out any) (*http.Response, error) {
	resp, err := g.get(ctx, path, query, decodeErr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return resp, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return resp, nil
		}
		return nil, fmt.Errorf("%s: decoding %s: %w", g.provider, path, err)
	}
	return resp, nil
}

// linkCount decodes a JSON array of links and keeps only how many there were, so a link never reaches a struct
// that could be logged or stored.
type linkCount int

func (c *linkCount) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		*c = 0
		return nil
	}
	*c = linkCount(len(raw))
	return nil
}

// normHash lower-cases a hash and reports whether it is a v1 info hash (40 hex digits).
func normHash(h string) (string, bool) {
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) != 40 {
		return h, false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return h, false
		}
	}
	return h, true
}

// cleanHashes validates a cache query's hashes: v1 hashes only, lower-cased, deduped.
func cleanHashes(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for i, h := range in {
		n, ok := normHash(h)
		if !ok {
			return nil, fmt.Errorf("provider: hash %d is not a v1 info hash", i)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// flexTime decodes the date forms the providers use: RFC 3339 (with or without fraction), the same without a zone
// (taken as UTC), "YYYY-MM-DD hh:mm:ss", and null or "" (zero). An unreadable date is zero rather than an error, so
// one odd row never fails a whole list.
type flexTime struct{ time.Time }

var flexLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"}

func (t *flexTime) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil || s == "" {
		t.Time = time.Time{}
		return nil
	}
	for _, l := range flexLayouts {
		if v, err := time.Parse(l, s); err == nil {
			t.Time = v.UTC()
			return nil
		}
	}
	t.Time = time.Time{}
	return nil
}

// trimSlash drops a leading slash from a provider file path (RD paths start with one).
func trimSlash(p string) string { return strings.TrimLeft(p, "/") }
