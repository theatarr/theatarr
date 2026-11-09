// Package rootprobe bounds filesystem calls on library roots. A root on a network filesystem (NFS, SMB) that stops
// answering blocks stat and statfs in the kernel, and nothing can stop such a call: the goroutine and its OS thread
// stay until the filesystem answers. Settings root checks and the arr views' rootfolder and diskspace go through one
// Prober, so a hung share shows as unreachable everywhere instead of holding requests or piling up threads, and only
// the checks on that root are affected.
//
// Calls are counted per filesystem, not per path: a directory belongs to the mount it is in (the deepest mount point
// above it in /proc/self/mountinfo, by device, so bind mounts of one share count once), and each mount runs one call
// at a time. A call that gives up (its timeout or context) keeps its mount busy until its filesystem call returns;
// meanwhile new calls on any directory of that mount are refused at once (StuckError) without touching the
// filesystem again, so a hung share holds one blocked thread whatever paths are asked about. Calls on other mounts
// run as usual. Without a mount table (not Linux) each path counts as its own.
//
// A caller never calls Do from inside f: f would wait for slots it may hold itself. An operation that consults
// several roots resolves them one Do at a time (policy.RootResolver), or names them all in one Do.
package rootprobe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Prober is safe for concurrent use; the zero value is ready.
type Prober struct {
	mu    sync.Mutex
	roots map[string]*root // by mount key (keyOf)
}

type root struct {
	slot  chan struct{} // held by the call running on the root, including one that gave up
	stuck bool          // the holder gave up and still waits on the filesystem
	users int           // calls waiting for or holding the slot; the entry is dropped at zero
}

// Default is the process's prober.
var Default = &Prober{}

// StuckError refuses a call on a root whose earlier call gave up and still waits on the filesystem.
type StuckError struct{ Root string }

func (e *StuckError) Error() string {
	return fmt.Sprintf("%s: an earlier check is still waiting on a filesystem that does not answer; try again once that directory answers", e.Root)
}

// TimeoutError is a call that gave up after After.
type TimeoutError struct {
	Root  string
	After time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s: the check gave up after %s: the directory is on a filesystem that does not answer", e.Root, e.After)
}

// Stuck reports whether a call on dir's filesystem gave up and still waits on it.
func (p *Prober) Stuck(dir string) bool {
	key := keyOf(readMountTable(), filepath.Clean(dir))
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.roots[key]
	return r != nil && r.stuck
}

// Do runs f, the filesystem calls about dirs, until ctx ends or timeout passes. It waits for the calls already
// running on dirs' mounts, and refuses at once when one of them is stuck. A call that gives up marks the mounts of
// every one of dirs stuck until f returns. f should touch only dirs' filesystems, or accept that a hang elsewhere is
// charged to them. The slots of several mounts are always taken in the same (sorted) order.
func (p *Prober) Do(ctx context.Context, dirs []string, timeout time.Duration, f func() error) error {
	return p.do(ctx, dirs, timeout, f, true)
}

// stuckPoll is how often a call waiting for a slot (DoOnce) looks whether the slot's holder gave up.
var stuckPoll = 20 * time.Millisecond

// DoOnce is Do with timeout counted from when the slots are held, not from the call: a read that
// waits its turn behind other reads of the same filesystem is not taken for a hung one. The wait for the slots ends
// with ctx, or as soon as a holder gives up (StuckError), so a hung filesystem still costs one timeout, not one per
// waiting call. For a caller that splits its work into short reads (a directory listed in chunks) and wants each
// bounded on its own.
func (p *Prober) DoOnce(ctx context.Context, dirs []string, timeout time.Duration, f func() error) error {
	return p.do(ctx, dirs, timeout, f, false)
}

func (p *Prober) do(ctx context.Context, dirs []string, timeout time.Duration, f func() error, waitBounded bool) error {
	table := readMountTable()
	clean := make([]string, 0, len(dirs))
	for _, d := range dirs {
		clean = append(clean, filepath.Clean(d))
	}
	slices.Sort(clean)
	// Errors name the first directory asked about on a mount, not the mount's key.
	var keys []string
	labels := map[string]string{}
	for _, d := range clean {
		k := keyOf(table, d)
		if _, ok := labels[k]; !ok {
			labels[k] = d
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	label := "root"
	if len(clean) > 0 {
		label = clean[0]
	}
	entries, err := p.enter(keys, labels)
	if err != nil {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitTimer <-chan time.Time // the wait's bound: the call's timer (Do), or none (DoOnce)
	var poll <-chan time.Time      // DoOnce: when to look whether a holder gave up
	if waitBounded {
		waitTimer = timer.C
	} else {
		t := time.NewTicker(stuckPoll)
		defer t.Stop()
		poll = t.C
	}
	// Slots are taken in sorted order, so two calls on overlapping sets cannot hold one each and wait forever.
	held := 0
	release := func() {
		for _, r := range entries[:held] {
			<-r.slot
		}
		p.leave(keys, entries)
	}
	for _, r := range entries {
	wait:
		for {
			select {
			case r.slot <- struct{}{}:
				held++
				break wait
			case <-ctx.Done():
				release()
				return ctx.Err()
			case <-waitTimer:
				release()
				return &TimeoutError{Root: label, After: timeout}
			case <-poll:
				if stuck := p.firstStuck(keys); stuck != "" {
					release()
					return &StuckError{Root: labels[stuck]}
				}
			}
		}
	}
	if !waitBounded {
		timer.Reset(timeout)
	}
	// The wait may have outlasted a call that gave up: check again now that the slots are ours.
	if stuck := p.firstStuck(keys); stuck != "" {
		release()
		return &StuckError{Root: labels[stuck]}
	}
	var mu sync.Mutex
	finished, abandoned := false, false
	done := make(chan error, 1)
	go func() {
		err := f()
		mu.Lock()
		finished = true
		gaveUp := abandoned
		mu.Unlock()
		if gaveUp {
			p.mark(entries, false)
		}
		release()
		done <- err
	}()
	abandon := func() {
		mu.Lock()
		if !finished {
			abandoned = true
			p.mark(entries, true)
		}
		mu.Unlock()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		abandon()
		return ctx.Err()
	case <-timer.C:
		abandon()
		return &TimeoutError{Root: label, After: timeout}
	}
}

func (p *Prober) enter(keys []string, labels map[string]string) ([]*root, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range keys {
		if r := p.roots[k]; r != nil && r.stuck {
			return nil, &StuckError{Root: labels[k]}
		}
	}
	if p.roots == nil {
		p.roots = map[string]*root{}
	}
	out := make([]*root, len(keys))
	for i, k := range keys {
		r := p.roots[k]
		if r == nil {
			r = &root{slot: make(chan struct{}, 1)}
			p.roots[k] = r
		}
		r.users++
		out[i] = r
	}
	return out, nil
}

func (p *Prober) leave(keys []string, entries []*root) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, r := range entries {
		if r.users--; r.users == 0 {
			delete(p.roots, keys[i])
		}
	}
}

func (p *Prober) mark(entries []*root, stuck bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range entries {
		r.stuck = stuck
	}
}

func (p *Prober) firstStuck(keys []string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range keys {
		if r := p.roots[k]; r != nil && r.stuck {
			return k
		}
	}
	return ""
}

// MountsBelow lists the mount points strictly below the directory dir, sorted, from the mount table (nil without
// one): a walk of dir crosses into them, and each is a filesystem of its own that can stop answering while dir
// answers, so a caller about to walk dir checks each like dir itself.
func MountsBelow(dir string) []string {
	dir = filepath.Clean(dir)
	var out []string
	for _, m := range readMountTable() {
		if m.point != dir && within(m.point, dir) && !slices.Contains(out, m.point) {
			out = append(out, m.point)
		}
	}
	slices.Sort(out)
	return out
}

// Idle reports whether no call is running or waiting (tests wait for calls left behind).
func (p *Prober) Idle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.roots) == 0
}

// mountTablePath is the mount table Do reads (UseMountTable points it elsewhere in tests).
var mountTablePath atomic.Pointer[string]

// UseMountTable makes Do read the mount table at path (mountinfo format) instead of /proc/self/mountinfo, until
// restore is called. For tests (rootprobe/probetest): directories of one test tree can then count as separate mounts.
func UseMountTable(path string) (restore func()) {
	old := mountTablePath.Swap(&path)
	return func() { mountTablePath.Store(old) }
}

// mount is one line of the mount table: the filesystem's device and where it is mounted.
type mount struct{ dev, point string }

// readMountTable reads the mount table; nil when there is none (not Linux). Reading it makes no call on the mounted
// filesystems.
func readMountTable() []mount {
	path := "/proc/self/mountinfo"
	if p := mountTablePath.Load(); p != nil {
		path = *p
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []mount
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		out = append(out, mount{dev: f[2], point: unescape(f[4])})
	}
	return out
}

// keyOf is the slot of the clean directory dir: the device of the deepest mount point above it (a later mount on
// the same point covers an earlier one), found by path alone; dir itself without a mount table. A symlink in dir
// that leads to another mount is not followed: its hang is charged to dir's mount.
func keyOf(table []mount, dir string) string {
	var in *mount
	for i := range table {
		m := &table[i]
		if within(dir, m.point) && (in == nil || len(m.point) >= len(in.point)) {
			in = m
		}
	}
	if in == nil {
		return "path " + dir
	}
	return "dev " + in.dev
}

func within(path, dir string) bool {
	return path == dir || dir == "/" || strings.HasPrefix(path, dir+"/")
}

// unescape undoes the mount table's octal escapes (\040 for a space).
func unescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
