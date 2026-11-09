package settings

import (
	"context"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// ReadBase keeps database-owned sections out of subsequent startup and CLI reads. Explicit imports use Read.
func ReadBase(ctx context.Context, db *store.Store, dir string) (*policy.Policy, error) {
	var gen, quality int64
	if err := db.Reader().QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),0) FROM settings_generation").Scan(&gen); err != nil {
		return nil, err
	}
	if gen == 0 {
		return policy.ReadOrEmpty(dir)
	}
	if err := db.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM settings_quality_state").Scan(&quality); err != nil {
		return nil, err
	}
	return policy.ReadSettingsOrEmpty(dir, quality != 0)
}
