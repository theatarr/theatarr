package library

import (
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The mount metrics read the library current returns at each scrape, so after a save that moves the mount
// (a fresh guard in the new generation's library) they report the new guard, not the startup one.
func TestMountMetricsFollowCurrentLibrary(t *testing.T) {
	t.Parallel()
	startup := &Library{mount: &Mount{Path: "/mnt/old"}}
	moved := &Library{mount: &Mount{Path: "/mnt/new"}}
	g := startup.mount.guard()
	g.mu.Lock()
	g.timeouts = 7
	g.mu.Unlock()
	var current atomic.Pointer[Library]
	current.Store(startup)
	reg := prometheus.NewPedanticRegistry()
	RegisterMountMetrics(reg, current.Load)
	read := func() float64 {
		t.Helper()
		v, err := testutil.GatherAndCount(reg, "theatarr_library_mount_timeouts_total")
		if err != nil || v != 1 {
			t.Fatalf("gather: %d %v", v, err)
		}
		mfs, _ := reg.Gather()
		for _, mf := range mfs {
			if mf.GetName() == "theatarr_library_mount_timeouts_total" {
				return mf.GetMetric()[0].GetCounter().GetValue()
			}
		}
		return -1
	}
	if got := read(); got != 7 {
		t.Fatalf("startup guard: %v", got)
	}
	current.Store(moved)
	if got := read(); got != 0 {
		t.Fatalf("metrics still read the startup guard after the mount moved: %v", got)
	}
	current.Store(nil)
	if got := read(); got != 0 {
		t.Fatalf("no library: %v", got)
	}
}
