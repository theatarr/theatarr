package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/store"
)

// CardigannSessions keeps the cardigann indexers' site sessions in the database, beside the settings secrets and
// the same way (plain text in the 0600 database; the database is restricted by New). No API returns them.
type CardigannSessions struct {
	db *store.Store
}

// NewCardigannSessions returns the session store of db, restricting the database first.
func NewCardigannSessions(db *store.Store) (*CardigannSessions, error) {
	if err := restrictDB(db); err != nil {
		return nil, err
	}
	return &CardigannSessions{db: db}, nil
}

// LoadSession returns an indexer's saved session, or nil.
func (c *CardigannSessions) LoadSession(ctx context.Context, indexer string) (*cardigann.SavedSession, error) {
	var value string
	err := c.db.Reader().QueryRowContext(ctx, "SELECT value FROM cardigann_session WHERE indexer = ?", indexer).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, dbError(err)
	}
	var s cardigann.SavedSession
	if err := json.Unmarshal([]byte(value), &s); err != nil {
		return nil, fmt.Errorf("settings: cardigann session of %s is unreadable", indexer)
	}
	return &s, nil
}

// SaveSession replaces an indexer's saved session.
func (c *CardigannSessions) SaveSession(ctx context.Context, indexer string, s cardigann.SavedSession) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return dbError(c.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO cardigann_session (indexer, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (indexer) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			indexer, string(b), store.Millis(time.Now()))
		return err
	}))
}

// DeleteSession forgets an indexer's saved session.
func (c *CardigannSessions) DeleteSession(ctx context.Context, indexer string) error {
	return dbError(c.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM cardigann_session WHERE indexer = ?", indexer)
		return err
	}))
}
