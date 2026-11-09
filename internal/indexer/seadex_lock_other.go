//go:build !unix

package indexer

import "context"

// Non-Unix platforms serialize writers in this process only.
func lockSeaDexPath(ctx context.Context, path string) (func(), error) {
	return lockSeaDexMemory(ctx, path)
}
