// Package adder is the write capability for debrid providers (docs/design/router.md): add a magnet and, on
// Real-Debrid, select its files. It is a separate package from internal/provider, whose Provider stays read-only by
// construction (GET only, tested), so code holding a Provider cannot add anything.
//
// # What it can do, and what it cannot
//
//   - [Adder.AddMagnet] adds a [Magnet]: magnet:?xt=urn:btih:<hash>, plus the policy's public trackers when any. A
//     Magnet can only be made by [NewMagnet], which refuses a release with no v1 info hash, and by [Restore]. Only
//     the bare hash is sent: a .torrent file or an indexer's own magnet (which can carry a passkey in its trackers)
//     never is; there is no method that takes one. Which indexers' releases reach a provider is the user's choice
//     (the version's indexer list), checked by the decision engine, not here.
//   - [Adder.SelectFiles] selects files of a Real-Debrid torrent. AllDebrid and TorBox take every file.
//   - [Restore] makes the magnet that puts a torrent from a backup back on an account (internal/torrents): a hash
//     with no release (restore.go).
//   - The one delete in Theatarr is [Deleter.DeleteOwnUncachedAdd] (delete.go), on Real-Debrid and AllDebrid only and not
//     part of Adder: it removes an add this adder made moments ago in the same route attempt, when the router proves
//     the entry is ours and it turned out uncached (router.cached_only, and the one second selection a cached-only
//     movie gets on Real-Debrid), or in the same restore of the torrent manager, whose Real-Debrid add of a backup's
//     known selection came back uncached and is added once more with every playable file. Debrid torrents are shared by every library,
//     so nothing else here can remove one, except the torrent manager's user-chosen delete
//     ([UserDeleter.DeleteUserChosen], delete.go): torrents a person picked and confirmed after a backup. There is no
//     restart, unrestrict or link request (tests check the method sets, the paths, the HTTP methods and who calls the
//     deletes).
//
// Every request goes through the gateway as Target class provider.
//
// # Errors
//
// A refused or failed add is an *[Error] with a [Code] the router acts on (decypharr's error codes, widened for
// direct adds): see codes.go and docs/design/router.md.
//
// Request shapes follow the providers' API docs and decypharr (ref/sirrobot01_decypharr @ 917cfa2,
// pkg/debrid/providers/{realdebrid,alldebrid,torbox}); see rd.go, ad.go and tb.go.
package adder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// Adder adds magnets to one debrid account.
type Adder interface {
	// Key is the policy key (providers[Key]); Kind is realdebrid, alldebrid or torbox.
	Key() string
	Kind() string
	// AddMagnet adds m and returns the provider's torrent id. A refusal is an *Error.
	AddMagnet(ctx context.Context, m Magnet, o AddOptions) (*Added, error)
	// SelectsFiles reports whether the provider waits for a file selection after an add (Real-Debrid).
	SelectsFiles() bool
	// SelectFiles selects files (provider file ids) of torrent id. ErrNoSelection where the provider takes every
	// file.
	SelectFiles(ctx context.Context, id string, fileIDs []string) error
}

// AddOptions tune one add.
type AddOptions struct {
	// OnlyIfCached asks the provider to refuse the add unless it already holds the content (TorBox's
	// add_only_if_cached). Providers without such an option ignore it.
	OnlyIfCached bool
}

// Added is an accepted add.
type Added struct {
	ID   string `json:"id"`
	Hash string `json:"hash,omitempty"`
	Name string `json:"name,omitempty"`
	// Ready is true when the provider said at once that it holds the content (AllDebrid ready, TorBox "cached").
	// False means unknown until the status is read.
	Ready bool `json:"ready"`
}

// ErrNoSelection: the provider takes every file; there is nothing to select.
var ErrNoSelection = errors.New("adder: this provider takes every file")

// Magnet is a debrid magnet: a bare v1 hash. Only NewMagnet and Restore make one; the zero Magnet is refused by every
// Adder.
type Magnet struct {
	hash     string
	indexer  string
	title    string
	refused  bool // Real-Debrid's name rule matches the title
	trackers []string
	restore  bool // made by Restore: a hash from a backup, with no release (indexer is "")
}

// Hash is the v1 info hash, lower-case hex.
func (m Magnet) Hash() string { return m.hash }

// Indexer is the policy key of the indexer the release came from ("" for a restore).
func (m Magnet) Indexer() string { return m.indexer }

// Title is the release title (for logs and RD's name rule; it is not sent).
func (m Magnet) Title() string { return m.title }

// URI is the magnet URI: magnet:?xt=urn:btih:<hash>, then &tr= for each public tracker. No name is sent.
func (m Magnet) URI() string {
	if m.hash == "" {
		return ""
	}
	s := "magnet:?xt=urn:btih:" + m.hash
	for _, t := range m.trackers {
		s += "&tr=" + url.QueryEscape(t)
	}
	return s
}

// String never shows trackers.
func (m Magnet) String() string { return "magnet " + m.hash }

func (m Magnet) valid() bool { return len(m.hash) == 40 && (m.indexer != "" || m.restore) }

// ErrNoHash: NewMagnet was given a release with no v1 info hash.
var ErrNoHash = errors.New("adder: the release has no v1 info hash")

// NewMagnet makes the magnet for r: its v1 info hash (ErrNoHash without one) and trackers, the policy's public
// trackers (router.trackers, validated there); one that looks keyed is refused.
func NewMagnet(r *indexer.Release, trackers []string) (Magnet, error) {
	if r == nil {
		return Magnet{}, ErrNoHash
	}
	h := indexer.NormalizeHash(r.InfoHash)
	if h == "" {
		return Magnet{}, ErrNoHash
	}
	if err := checkTrackers(trackers); err != nil {
		return Magnet{}, err
	}
	return Magnet{hash: h, indexer: r.Indexer, title: r.Title, refused: indexer.RealDebridRefusesRelease(r),
		trackers: append([]string(nil), trackers...)}, nil
}

// checkTrackers refuses a tracker that looks keyed: public trackers carry no key.
func checkTrackers(trackers []string) error {
	for i, t := range trackers {
		if l := indexer.NewLink(t); l.Empty() || l.Keyed() || l.IsMagnet() {
			return fmt.Errorf("adder: tracker %d looks keyed; public trackers carry no key", i)
		}
	}
	return nil
}

// New returns the adder for providers[key]. apiKey is the value of its api_key_env; d is the gateway.
func New(key string, pv *policy.Provider, apiKey string, d provider.Doer) (Adder, error) {
	if pv == nil {
		return nil, fmt.Errorf("adder %q: not in the policy", key)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("adder %q: %w (%s)", key, provider.ErrNoKey, pv.APIKeyEnv)
	}
	base, err := url.Parse(strings.TrimRight(pv.URL, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("adder %q: bad url", key)
	}
	p := &poster{provider: key, base: base, key: apiKey, doer: d, redact: provider.NewRedactor(apiKey)}
	switch pv.Kind {
	case provider.KindRealDebrid:
		return &realDebrid{p: p, own: newLedger()}, nil
	case provider.KindAllDebrid:
		return newAllDebrid(p), nil
	case provider.KindTorBox:
		return &torBox{p: p}, nil
	}
	return nil, fmt.Errorf("adder %q: unknown kind %q", key, pv.Kind)
}

// poster sends the POST requests. It holds the key and puts it in the Authorization header, never in the URL or
// the form.
type poster struct {
	provider string
	base     *url.URL
	key      string
	redact   *provider.Redactor
	doer     provider.Doer
}

// maxBody bounds what is read from an answer.
const maxBody = 1 << 20

// send sends form to base+path with method (POST, or DELETE from delete.go only) through the gateway (class
// provider) and returns the status and body. Transport errors and gateway refusals
// come back as *Error with CodeUnavailable.
func (p *poster) send(ctx context.Context, method, path string, form url.Values) (int, []byte, time.Duration, error) {
	u := *p.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("%s: bad request", p.provider)
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.doer.Do(ctx, gateway.Target{Class: gateway.ClassProvider, Key: p.provider}, req)
	if err != nil {
		e := &Error{Provider: p.provider, Code: CodeUnavailable, Message: "request not sent or failed", Err: err}
		var r *gateway.Refused
		if errors.As(err, &r) {
			e.Message = "gateway refused: " + string(r.Reason)
			e.RetryAt = r.RetryAt
		}
		return 0, nil, 0, e
	}
	return p.readResponse(resp)
}

// readResponse bounds and closes every form/JSON response and carries Retry-After as a value to the decoder.
func (p *poster) readResponse(resp *http.Response) (int, []byte, time.Duration, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, 0, &Error{Provider: p.provider, Code: CodeUnavailable, Status: resp.StatusCode,
			Message: "reading the answer failed", Err: err}
	}
	var ra time.Duration
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if seconds, err := strconv.ParseInt(v, 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
			ra = time.Duration(seconds) * time.Second
		} else if at, err := http.ParseTime(v); err == nil {
			ra = max(0, time.Until(at))
		}
	}
	return resp.StatusCode, b, ra, nil
}

// post POSTs form and returns the provider's Retry-After alongside the status and bounded body.
func (p *poster) post(ctx context.Context, path string, form url.Values) (int, []byte, time.Duration, error) {
	return p.send(ctx, http.MethodPost, path, form)
}

// decodeJSON decodes b into out, as an *Error (CodeUnknown) when it isn't JSON of that shape.
func (p *poster) decodeJSON(status int, b []byte, out any) error {
	if err := json.Unmarshal(b, out); err != nil {
		return &Error{Provider: p.provider, Code: CodeUnknown, Status: status, Message: "undecodable answer"}
	}
	return nil
}
