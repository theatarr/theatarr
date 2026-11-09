package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/logring"
)

func TestRotatingLogs(t *testing.T) {
	for _, keep := range []int{1, 3} {
		t.Run(fmt.Sprint(keep), func(t *testing.T) {
			dir := t.TempDir()
			w, err := NewRotatingWriter(dir, "theatarr.log", 8, keep)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 6; i++ {
				if _, err := fmt.Fprintf(w, "%07d\n", i); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != keep {
				t.Fatalf("kept %d, want %d", len(entries), keep)
			}
			for i := 0; i < keep; i++ {
				name := "theatarr.log"
				if i > 0 {
					name += fmt.Sprintf(".%d", i)
				}
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(b) != fmt.Sprintf("%07d\n", 5-i) {
					t.Fatalf("%s: %q %v", name, b, err)
				}
			}
			// A restart appends, and reducing retention prunes older numbered files.
			w, err = NewRotatingWriter(dir, "theatarr.log", 100, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("after restart\n")); err != nil {
				t.Fatal(err)
			}
			w.Close()
			b, _ := os.ReadFile(filepath.Join(dir, "theatarr.log"))
			if string(b) != "0000005\nafter restart\n" {
				t.Fatalf("restart: %q", b)
			}
			info, _ := os.Stat(filepath.Join(dir, "theatarr.log"))
			if info.Mode().Perm() != 0600 {
				t.Errorf("mode: %v", info.Mode())
			}
			entries, _ = os.ReadDir(dir)
			if len(entries) != 1 {
				t.Errorf("retention reduction: %d files", len(entries))
			}
			if _, err := w.Write([]byte("closed")); err == nil {
				t.Error("write after close succeeded")
			}
		})
	}
}

func TestRotatingLogsConcurrentJSON(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir, "theatarr.log", 150, 100)
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := NewRuntimeLogger(w, "debug")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { log.Info(strings.Repeat("x", 200)) })
	}
	wg.Wait()
	w.Close()
	entries, _ := os.ReadDir(dir)
	lines := 0
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatalf("split record in %s: %q", e.Name(), line)
			}
			lines++
		}
	}
	if lines != 20 {
		t.Errorf("records: %d", lines)
	}
}

func TestLogRotationSettings(t *testing.T) {
	for _, tc := range []struct {
		size int64
		keep int
	}{{0, 1}, {-1, 1}, {1, 0}} {
		if w, err := NewRotatingWriter(t.TempDir(), "theatarr.log", tc.size, tc.keep); err == nil {
			w.Close()
			t.Errorf("accepted %+v", tc)
		}
	}
	if _, err := NewRotatingWriter(t.TempDir(), "../theatarr.log", 10, 2); err == nil {
		t.Error("accepted unsafe base")
	}
}

func TestRuntimeLogLevelAllOutputs(t *testing.T) {
	var stderr, file bytes.Buffer
	log, level, err := NewRuntimeLogger(io.MultiWriter(&stderr, &file), "info")
	if err != nil {
		t.Fatal(err)
	}
	ring := logring.New(20)
	log = slog.New(ring.Handler(log.Handler()))
	child := log.With("component", "worker")
	child.Debug("hidden")
	if err := level.Set("DEBUG"); err != nil {
		t.Fatal(err)
	}
	child.Debug("visible")
	if err := level.Set("error"); err != nil {
		t.Fatal(err)
	}
	log.Warn("hidden warning")
	child.Error("visible error")
	if stderr.String() != file.String() || strings.Contains(file.String(), "hidden") {
		t.Fatalf("outputs: %s", file.String())
	}
	if entries := ring.Recent(logring.Filter{Level: slog.LevelDebug}); len(entries) != 2 {
		t.Errorf("ring: %+v", entries)
	}
	for _, invalid := range []string{"loud", "", "INFO+3"} {
		if err := level.Set(invalid); err == nil || level.Level() != "ERROR" {
			t.Errorf("invalid %q changed level", invalid)
		}
	}
	_, restarted, _ := NewRuntimeLogger(io.Discard, "info")
	if restarted.Level() != "INFO" {
		t.Error("runtime level persisted")
	}
}
