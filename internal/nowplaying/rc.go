package nowplaying

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/theatarr/theatarr/internal/gateway"
)

// RCTarget is the gateway target of the mount's rclone rc (library.mount.rclone_rc).
var RCTarget = gateway.Target{Class: gateway.ClassRcloneRC, Key: gateway.RcloneRCKey}

// Doer sends a request through the gateway.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// RC reads the mount's rclone remote control: core/stats (what the VFS cache is fetching now, with its speed) and
// vfs/stats (the disk cache). rclone's rc takes POST even for these reads; nothing else is called. The credentials go
// in the Authorization header only and are never logged or returned.
type RC struct {
	URL      string
	User     string
	Password string
	GW       Doer
}

// Transfer is one entry of core/stats transferring[]: a VFS cache downloader fetching a file from zurg.
type Transfer struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Bytes    int64   `json:"bytes"`
	Speed    float64 `json:"speed"`    // bytes/s now
	SpeedAvg float64 `json:"speedAvg"` // bytes/s, moving average
}

// Cache is vfs/stats' diskCache.
type Cache struct {
	BytesUsed    int64 `json:"bytesUsed"`
	Files        int   `json:"files"`
	ErroredFiles int   `json:"erroredFiles"`
	OutOfSpace   bool  `json:"outOfSpace"`
}

func (r *RC) call(ctx context.Context, method string, out any) error {
	u, err := url.Parse(strings.TrimRight(r.URL, "/") + "/" + method)
	if err != nil || u.Host == "" {
		return errors.New("nowplaying: bad rclone_rc URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.User != "" || r.Password != "" {
		req.SetBasicAuth(r.User, r.Password)
	}
	resp, err := r.GW.Do(ctx, RCTarget, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("nowplaying: rclone rc %s: HTTP %d", method, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("nowplaying: rclone rc %s: %w", method, err)
	}
	return nil
}

// Transfers lists what the cache is fetching now. Reads served from the cache do not appear.
func (r *RC) Transfers(ctx context.Context) ([]Transfer, error) {
	var out struct {
		Transferring []Transfer `json:"transferring"`
	}
	err := r.call(ctx, "core/stats", &out)
	return out.Transferring, err
}

// Cache reads the disk cache's state.
func (r *RC) Cache(ctx context.Context) (*Cache, error) {
	var out struct {
		DiskCache *Cache `json:"diskCache"`
	}
	if err := r.call(ctx, "vfs/stats", &out); err != nil {
		return nil, err
	}
	if out.DiskCache == nil {
		return nil, errors.New("nowplaying: rclone rc vfs/stats: no diskCache (is --vfs-cache-mode set?)")
	}
	return out.DiskCache, nil
}

// Version tests an rc connection with the read-only core/version method.
func (r *RC) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := r.call(ctx, "core/version", &out)
	return out.Version, err
}
