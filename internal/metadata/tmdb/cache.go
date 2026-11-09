package tmdb

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// SQLCache keeps responses in the metadata_cache table, gzip-compressed, keyed by source and request path.
// Retention prunes expired TMDb search/discovery/trending rows in batches; detail responses stay for offline reads.
type SQLCache struct {
	Store  *store.Store
	Source string // "tmdb"
	// Now is the clock. Default time.Now.
	Now func() time.Time
}

func (c *SQLCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get implements Cache.
func (c *SQLCache) Get(ctx context.Context, key string) (int, []byte, bool, error) {
	var status int
	var body []byte
	var expires int64
	err := c.Store.Reader().QueryRowContext(ctx, `SELECT status, body, expires_at FROM metadata_cache
		WHERE source = ? AND key = ?`, c.Source, key).Scan(&status, &body, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	if expires <= store.Millis(c.now()) {
		return 0, nil, false, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return 0, nil, false, nil // a broken entry is a miss
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return 0, nil, false, nil
	}
	return status, raw, true, nil
}

// Put implements Cache.
func (c *SQLCache) Put(ctx context.Context, key string, status int, body []byte, ttl time.Duration) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	now := c.now()
	_, err := c.Store.Writer().ExecContext(ctx, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (source, key) DO UPDATE SET status = excluded.status, body = excluded.body,
		fetched_at = excluded.fetched_at, expires_at = excluded.expires_at`,
		c.Source, key, status, buf.Bytes(), store.Millis(now), store.Millis(now.Add(ttl)))
	return err
}
