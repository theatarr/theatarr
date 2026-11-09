package indexer

import "context"

// recordSeaDex stores complete observations fetched now (test fixtures).
func (e *Evidence) recordSeaDex(ctx context.Context, snapshots []SeaDexSnapshot) error {
	if e == nil {
		return nil
	}
	err := e.observeSeaDex(ctx, snapshots)
	return err
}
