package indexer

import (
	"context"
	"os"
	"testing"
)

func TestSidecarUsesDatabaseMode(t *testing.T) {
	st := lockMappedStore(t)
	if err := os.Chmod(st.Path(), 0640); err != nil {
		t.Fatal(err)
	}
	ev := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy()})
	unlock, err := ev.lockSeaDex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	info, err := os.Stat(st.Path() + ".seadex-lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("sidecar mode=%o, want database mode 640", info.Mode().Perm())
	}
}
