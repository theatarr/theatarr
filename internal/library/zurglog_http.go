package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
)

// ZurgLogTarget is the gateway target for zurg's /logs endpoint (library.mount.zurg_log as a URL).
var ZurgLogTarget = gateway.Target{Class: gateway.ClassZurgLog, Key: gateway.ZurgLogKey}

// ZurgLogDoer sends a request through the gateway.
type ZurgLogDoer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// zurgHeadBytes is how much of the log's head is fingerprinted to tell a new file from the one already read.
const zurgHeadBytes = 4096

// ZurgLogHTTPFollower follows zurg's log over HTTP, for when the file is on another host: zurg serves the whole
// current log at /logs (no Range support), so every poll fetches it and only the bytes past the last processed
// line are read for renames.
//
// It remembers the length it has processed and a fingerprint of the file's head. A body shorter than that, or
// with a different head, is a new file (zurg rotated or truncated it) and is read from its start. Lines written
// to the old file after the last poll and before the rotation are not served anywhere and are lost; OnRotate runs
// so the caller can sweep at once instead of waiting for the hourly one. When the server sends ETag or
// Last-Modified, the next poll is conditional and a 304 skips the body (zurg currently sends neither).
//
// Like the file follower it starts at the current end (older renames are the sweep's and the dangling report's) and
// hands the renames it saw within Batch to OnRenames at once. A failed fetch keeps the position and backs off,
// doubling the interval up to MaxBackoff (or until the gateway's own back-off ends).
type ZurgLogHTTPFollower struct {
	URL        string
	GW         ZurgLogDoer
	Interval   time.Duration // default 15s
	Batch      time.Duration // default 10s
	MaxBackoff time.Duration // default 5m
	Logger     *slog.Logger
	OnRenames  func(ctx context.Context, renames map[string]string)
	// OnRotate is called when a new file is seen after the first poll. Optional.
	OnRotate func(ctx context.Context)
	// OnLine, when set, gets every new line (the now playing page's stream health patterns).
	OnLine func(line string)

	started      bool
	offset       int64
	headLen      int
	headSum      [sha256.Size]byte
	etag         string
	lastModified string
	batch        renameBatch
}

// Run follows the log until ctx ends. It only reads.
func (z *ZurgLogHTTPFollower) Run(ctx context.Context) {
	interval, batch, maxBackoff := z.Interval, z.Batch, z.MaxBackoff
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if batch <= 0 {
		batch = 10 * time.Second
	}
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Minute
	}
	log := z.Logger
	if log == nil {
		log = slog.Default()
	}
	failures := 0
	for {
		wait := interval
		if err := z.poll(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait = min(interval<<min(failures, 16), maxBackoff)
			var r *gateway.Refused
			if errors.As(err, &r) && !r.RetryAt.IsZero() {
				wait = min(max(wait, time.Until(r.RetryAt)), maxBackoff)
			}
			if failures == 1 {
				log.Warn("library: zurg log fetch failed; backing off", "err", err, "retry_in", wait)
			} else {
				log.Debug("library: zurg log fetch failed", "err", err, "failures", failures, "retry_in", wait)
			}
		} else if failures > 0 {
			log.Info("library: zurg log fetch works again", "after_failures", failures)
			failures = 0
		}
		z.batch.flush(ctx, batch, z.OnRenames)
		// Wake up for a pending batch even when the poll interval is longer.
		if len(z.batch.pending) > 0 {
			wait = min(wait, max(batch-time.Since(z.batch.since), time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// poll fetches the log once and reads the new lines.
func (z *ZurgLogHTTPFollower) poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, z.URL, nil)
	if err != nil {
		return errors.New("library: bad zurg_log URL")
	}
	if z.etag != "" {
		req.Header.Set("If-None-Match", z.etag)
	}
	if z.lastModified != "" {
		req.Header.Set("If-Modified-Since", z.lastModified)
	}
	resp, err := z.GW.Do(ctx, ZurgLogTarget, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("library: zurg log: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	z.etag, z.lastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	z.read(ctx, body)
	return nil
}

// read processes the complete lines past the remembered offset, after deciding whether body is still the same
// file.
func (z *ZurgLogHTTPFollower) read(ctx context.Context, body []byte) {
	end := int64(bytes.LastIndexByte(body, '\n') + 1) // only whole lines; a partial one waits for the next poll
	if !z.started {
		// Only renames from now on.
		z.started, z.offset = true, end
		z.setHead(body)
		return
	}
	n := int64(len(body))
	if n < z.offset || n < int64(z.headLen) || sha256.Sum256(body[:z.headLen]) != z.headSum {
		z.offset = 0
		if z.OnRotate != nil {
			z.OnRotate(ctx)
		}
	}
	for z.offset < end {
		i := int64(bytes.IndexByte(body[z.offset:end], '\n'))
		line := string(body[z.offset : z.offset+i+1])
		z.batch.line(line)
		if z.OnLine != nil {
			z.OnLine(line)
		}
		z.offset += i + 1
	}
	z.setHead(body)
}

func (z *ZurgLogHTTPFollower) setHead(body []byte) {
	z.headLen = min(len(body), zurgHeadBytes)
	z.headSum = sha256.Sum256(body[:z.headLen])
}
