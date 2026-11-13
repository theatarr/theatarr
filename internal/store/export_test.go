package store

import (
	"context"
	"testing"
)

// OpenAt and UpTo let the external tests of this package (package store_test, which may import packages that import
// store) run a migration on a database at an older version.
func OpenAt(t *testing.T, version int64) *Store { return openAt(t, version) }

func (s *Store) UpTo(ctx context.Context, version int64) error {
	p, err := s.provider(false)
	if err != nil {
		return err
	}
	_, err = p.UpTo(ctx, version)
	return err
}
