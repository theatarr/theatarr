// Package plex asks Plex Media Server for partial library scans (docs/design/library.md): GET
// /library/sections/{id}/refresh?path=<folder>, with the token in the X-Plex-Token header (never in the URL, so it
// stays out of logs). Every request goes through the gateway as target plex:server, so the host allowlist, the
// optional budget and the back-off apply. Nothing here empties the trash or changes library settings.
package plex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/gateway"
)

// Doer is the gateway's Do.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// Target is the gateway target for Plex.
var Target = gateway.Target{Class: gateway.ClassPlex, Key: gateway.PlexKey}

// Client is a Plex server.
type Client struct {
	gw    Doer
	base  *url.URL
	token string
}

// New builds a client for baseURL (http://host:32400) with the token's value.
func New(gw Doer, baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("plex: bad URL %q", baseURL)
	}
	if token == "" {
		return nil, fmt.Errorf("plex: no token")
	}
	return &Client{gw: gw, base: u, token: token}, nil
}

// Scan asks for a partial scan of path in a section.
func (c *Client) Scan(ctx context.Context, section int, path string) error {
	if section <= 0 {
		return fmt.Errorf("plex: bad section %d", section)
	}
	u := *c.base
	u.Path += "/library/sections/" + strconv.Itoa(section) + "/refresh"
	u.RawQuery = url.Values{"path": {path}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plex-Token", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.gw.Do(ctx, Target, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("plex: section %d refresh: HTTP %d", section, resp.StatusCode)
	}
	return nil
}
