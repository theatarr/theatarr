package indexer

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestTorrentioUnknownOptionDoesNotQuoteCredential(t *testing.T) {
	_, err := newTorrentio(base{key: "torrentio", ix: &policy.Indexer{URL: "https://fixture.test/key-with-secret-value"}})
	if err == nil || strings.Contains(err.Error(), "key-with-secret-value") {
		t.Fatalf("error quoted credential: %v", err)
	}
}
