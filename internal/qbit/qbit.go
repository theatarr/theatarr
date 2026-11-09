// Package qbit is the qBittorrent Web API subset Theatarr sends a client-routed version's grabs through
// (versions[*].add_via: client; docs/design/external-clients.md): log in, add a bare magnet under the
// client's category, and read torrents back by hash. decypharr (v2.7.1, pkg/server/qbit) speaks it and answers a
// refused add with a JSON error ({error, code, retryable, permanent}). Nothing here removes, pauses or changes a
// torrent on the client: Theatarr never deletes from a client.
//
// Requests go through the gateway (class client); the client's credentials go in the login form only and its SID
// cookie in the Cookie header.
package qbit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// Doer sends a request through the gateway.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// maxBody bounds a response read: a torrents/info answer for a poll's open downloads, or an error.
const maxBody = 8 << 20

// Client is one clients[key] entry.
type Client struct {
	key      string
	base     string
	user     string
	pass     string
	category string
	doer     Doer

	mu  sync.Mutex
	sid string // the SID cookie of the last login
}

// New returns the client for clients[key]; user and pass are the values of its username_env and password_env.
// Constructing it sends nothing.
func New(key string, c *policy.Client, user, pass string, d Doer) (*Client, error) {
	if c == nil {
		return nil, fmt.Errorf("qbit %q: not in the policy", key)
	}
	u, err := url.Parse(strings.TrimRight(c.URL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("qbit %q: bad url", key)
	}
	return &Client{key: key, base: u.String(), user: user, pass: pass, category: c.Category, doer: d}, nil
}

// Key is the client's policy key.
func (c *Client) Key() string { return c.key }

// Category is the category Theatarr's adds go under.
func (c *Client) Category() string { return c.category }

// Torrent is one entry of torrents/info.
type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"` // 0 to 1
	State    string  `json:"state"`
	Category string  `json:"category"`
	SavePath string  `json:"save_path"`
	// ContentPath is the torrent's content as the client sees it: its folder, or its one file.
	ContentPath string `json:"content_path"`
}

// Status is what a torrent's state means for its download.
type Status int

const (
	StatusDownloading Status = iota
	StatusReady
	StatusFailed
)

// Status maps the qBittorrent state: error and missingFiles failed; the seeding and complete states ready
// (decypharr sets pausedUP once its symlinks are made); any other state downloading, at progress 1 too (decypharr
// reports progress 1 while downloading before it makes its links): metaDL, queuedDL, downloading, stalledDL,
// pausedDL, checkingDL, forcedDL.
func (t Torrent) Status() Status {
	switch t.State {
	case "error", "missingFiles":
		return StatusFailed
	case "pausedUP", "stoppedUP", "uploading", "stalledUP", "queuedUP", "forcedUP", "checkingUP":
		return StatusReady
	}
	return StatusDownloading
}

// Error is a request the client answered with an error: its HTTP status and, from decypharr, its JSON error.
type Error struct {
	Status    int
	Code      string
	Msg       string
	Retryable bool
	Permanent bool
}

func (e *Error) Error() string {
	s := fmt.Sprintf("qbit: HTTP %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	return s
}

// Verdict is what a failed add means for the route.
type Verdict int

const (
	// Deferred: try again later (a retryable answer, a 5xx, no JSON, a transport error or a gateway refusal).
	Deferred Verdict = iota
	// Miss: the client's debrid account does not have it cached and the client adds no uncached release.
	Miss
	// Refused: the client will never take this release (blocked, an invalid magnet, a permanent error).
	Refused
)

// Classify says what a failed add means. torrent_not_cached comes first: decypharr marks it retryable.
func Classify(err error) Verdict {
	var e *Error
	if !errors.As(err, &e) {
		return Deferred
	}
	switch {
	case e.Code == "torrent_not_cached":
		return Miss
	case e.Permanent || e.Code == "torrent_blocked" || e.Code == "invalid_magnet" || e.Code == codeFails:
		return Refused
	}
	return Deferred
}

// codeFails is qBittorrent's own refusal: 200 with the body "Fails.".
const codeFails = "fails"

// errLogin: the client refused the credentials.
var errLogin = errors.New("qbit: login refused")

// Add adds a bare magnet for hash under the client's category.
func (c *Client) Add(ctx context.Context, hash string) error {
	form := url.Values{"urls": {"magnet:?xt=urn:btih:" + strings.ToLower(hash)}}
	if c.category != "" {
		form.Set("category", c.category)
	}
	body, err := c.call(ctx, http.MethodPost, "/api/v2/torrents/add", form)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) == "Fails." {
		return &Error{Status: http.StatusOK, Code: codeFails, Msg: "the client refused the add"}
	}
	return nil
}

// Info reads the torrents with these hashes, one request per hash, under any category: decypharr 2.7.1 reads each
// hashes value as one hash (pkg/server/qbit/context.go hashesContext), not qBittorrent's "h1|h2". A hash held
// under another category (an arr's, before the handover) is in the answer with that category. A hash the client
// does not hold is not.
func (c *Client) Info(ctx context.Context, hashes []string) ([]Torrent, error) {
	var out []Torrent
	for _, h := range hashes {
		h = strings.ToLower(h)
		q := url.Values{"hashes": {h}}
		body, err := c.call(ctx, http.MethodGet, "/api/v2/torrents/info?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var all []Torrent
		if err := json.Unmarshal(body, &all); err != nil {
			return nil, fmt.Errorf("qbit %s: torrents/info: %w", c.key, err)
		}
		for _, t := range all {
			if t.Hash = strings.ToLower(t.Hash); t.Hash == h {
				out = append(out, t)
				break
			}
		}
	}
	return out, nil
}

// call sends one request with the session cookie, logging in first when there is none, and once more when the
// client answers 401 or 403 (an expired session).
func (c *Client) call(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	c.mu.Lock()
	sid := c.sid
	c.mu.Unlock()
	for try := 0; ; try++ {
		if sid == "" {
			var err error
			if sid, err = c.login(ctx); err != nil {
				return nil, err
			}
		}
		status, body, err := c.send(ctx, method, path, form, sid)
		if err != nil {
			return nil, err
		}
		if (status == http.StatusUnauthorized || status == http.StatusForbidden) && try == 0 {
			c.mu.Lock()
			if c.sid == sid {
				c.sid = ""
			}
			c.mu.Unlock()
			sid = ""
			continue
		}
		if status < 200 || status > 299 {
			return nil, answerError(status, body)
		}
		return body, nil
	}
}

// login posts the credentials and keeps the SID cookie.
func (c *Client) login(ctx context.Context) (string, error) {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	if c.category != "" {
		form.Set("category", c.category) // decypharr binds the login to the category's arr
	}
	req, err := c.request(ctx, http.MethodPost, "/api/v2/auth/login", form, "")
	if err != nil {
		return "", err
	}
	resp, err := c.doer.Do(ctx, gateway.Target{Class: gateway.ClassClient, Key: c.key}, req)
	if err != nil {
		return "", fmt.Errorf("qbit %s: login: %w", c.key, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) == "Fails." {
		return "", fmt.Errorf("qbit %s: %w (HTTP %d)", c.key, errLogin, resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if strings.EqualFold(ck.Name, "SID") && ck.Value != "" {
			c.mu.Lock()
			c.sid = ck.Value
			c.mu.Unlock()
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("qbit %s: login answered without a SID cookie", c.key)
}

func (c *Client) send(ctx context.Context, method, path string, form url.Values, sid string) (int, []byte, error) {
	req, err := c.request(ctx, method, path, form, sid)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.doer.Do(ctx, gateway.Target{Class: gateway.ClassClient, Key: c.key}, req)
	if err != nil {
		return 0, nil, fmt.Errorf("qbit %s: %w", c.key, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("qbit %s: %w", c.key, err)
	}
	return resp.StatusCode, body, nil
}

func (c *Client) request(ctx context.Context, method, path string, form url.Values, sid string) (*http.Request,
	error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: "SID", Value: sid})
	}
	return req, nil
}

// answerError is the error of a non-2xx answer: decypharr's JSON error when the body is one.
func answerError(status int, body []byte) error {
	e := &Error{Status: status}
	var j struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
		Permanent bool   `json:"permanent"`
	}
	if json.Unmarshal(body, &j) == nil && (j.Code != "" || j.Error != "") {
		e.Code, e.Msg, e.Retryable, e.Permanent = j.Code, j.Error, j.Retryable, j.Permanent
	} else {
		e.Msg = strings.TrimSpace(string(body[:min(len(body), 200)]))
	}
	return e
}
