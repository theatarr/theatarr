//go:build linux

package library

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A symlink under the root that flips between a folder inside and one outside never lets the helper read the
// outside file or report an outside path: what is checked is what is used (openat2 on Linux, os.Root elsewhere).
func TestMountHelperContainmentRace(t *testing.T) {
	for _, portable := range []bool{false, true} {
		t.Run("portable="+strconv.FormatBool(portable), func(t *testing.T) {
			if !portable && !openat2Works() {
				t.Skip("no openat2")
			}
			old := openat2Works
			openat2Works = func() bool { return !portable }
			t.Cleanup(func() { openat2Works = old })
			root := filepath.Join(t.TempDir(), "mount")
			outside := t.TempDir()
			in := filepath.Join(root, "__all__", "In")
			for _, d := range []string{in, outside} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(in, "f"), []byte("inside"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "f"), []byte("outside"), 0o644); err != nil {
				t.Fatal(err)
			}
			flip := filepath.Join(root, "__all__", "Flip")
			if err := os.Symlink("In", flip); err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				tmp := flip + ".tmp"
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					target := "In"
					if i%2 == 1 {
						target = outside
					}
					_ = os.Remove(tmp)
					if os.Symlink(target, tmp) == nil {
						_ = os.Rename(tmp, flip)
					}
				}
			}()
			defer func() { close(stop); wg.Wait() }()
			s := &helperServer{root: root}
			p := filepath.Join(flip, "f")
			rel, _, _ := s.place(opProbe, p)
			deadline := time.Now().Add(time.Second)
			var reads, evals int
			for time.Now().Before(deadline) {
				if f, err := s.open("open", p, rel, false); err == nil {
					b, _ := io.ReadAll(f)
					f.Close()
					if string(b) == "outside" {
						t.Fatalf("read the outside file through %s", p)
					}
					reads++
				}
				if real, err := s.eval(p, rel); err == nil {
					if strings.HasPrefix(real, outside) || !strings.Contains(real, string(filepath.Separator)+"mount"+string(filepath.Separator)) {
						t.Fatalf("eval %s: %s", p, real)
					}
					evals++
				}
			}
			if reads == 0 || evals == 0 {
				t.Errorf("no call got through: %d reads, %d evals", reads, evals)
			}
		})
	}
}
