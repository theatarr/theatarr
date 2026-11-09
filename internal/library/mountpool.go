package library

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
)

// MountHelper runs the mount's calls in helper processes (mounthelper.go): theatarr serve's default. The command is Exe
// with Args, then "-root" and the mount path; it gets Env and no other environment.
type MountHelper struct {
	Exe  string
	Args []string
	Env  []string
	// Size is the number of helper processes (DefaultMountHelpers when 0); IdleExit stops a helper unused that
	// long (DefaultMountHelperIdle when 0).
	Size     int
	IdleExit time.Duration
	// Max caps the helper processes there are at once, killed ones not yet gone included (2×Size when 0, at least
	// Size+1): helpers stuck in the kernel past SIGKILL use up the room for new ones, and at the cap a call fails as
	// the mount down instead of starting another.
	Max int
}

// Defaults for the helper pool.
const (
	DefaultMountHelpers    = 4
	DefaultMountHelperIdle = 5 * time.Minute
	// helperBackoffMax caps the wait before a helper is started again after kills in a row.
	helperBackoffMax = 30 * time.Second
	// helperKillWindow is how far back HelperState.RecentKills counts.
	helperKillWindow = 15 * time.Minute
)

// Why a helper call gave no answer. Each is a mount that did not answer in time (ErrMountDown); none leaves anything
// blocked in Theatarr. errHelperBusy is the exception: every helper is running a call that may well answer (a long probe),
// so the call fails as ErrMountDown but the guard does not count it against the mount.
var (
	errHelperTimeout = errors.New("mount helper: no answer in time; killed")
	errHelperBusy    = errors.New("mount helper: no helper free in time")
	errHelperBackoff = errors.New("mount helper: none could start in time after kills")
	errHelperStart   = errors.New("mount helper: cannot start")
	errHelperCrashed = errors.New("mount helper: ended mid-call")
	errHelperStuck   = errors.New("mount helper: killed helpers still stuck in the kernel; no room to start another")
)

func helperNoAnswer(err error) bool {
	return errors.Is(err, errHelperTimeout) || errors.Is(err, errHelperBackoff) || errors.Is(err, errHelperStart) ||
		errors.Is(err, errHelperCrashed) || errors.Is(err, errHelperStuck)
}

// HelperState is the helper pool's state, for health checks and metrics.
type HelperState struct {
	Size        int       `json:"size"`
	Max         int       `json:"max"`     // processes at most, lingering ones included
	Running     int       `json:"running"` // processes started and not killed, busy or idle
	Busy        int       `json:"busy"`
	Starts      int64     `json:"starts"`       // since start, the first ones included
	Kills       int64     `json:"kills"`        // helpers killed for a call that did not answer in time
	Crashes     int64     `json:"crashes"`      // helpers that ended mid-call, or could not start
	RecentKills int       `json:"recent_kills"` // kills and crashes in the last 15 minutes
	Lingering   int       `json:"lingering"`    // killed helpers not yet gone (a call stuck in the kernel)
	LastError   string    `json:"last_error,omitempty"`
	LastKill    time.Time `json:"last_kill,omitzero"`
	RetryAt     time.Time `json:"retry_at,omitzero"` // no helper is started before this (back-off after kills)
}

// helperPool is the helper processes of one mount. Each process answers one call at a time: a call that hangs
// holds only its own process, and killing it fails no other call. A pool of a few covers the library's
// concurrency (imports, relink, previews) without pipelining.
type helperPool struct {
	root     string
	cfg      MountHelper
	now      func() time.Time
	start    func() (*helperProc, error) // startProc; tests replace it
	reapFn   func(*os.Process) bool      // reapProcess; tests replace it (under mu)
	idleExit time.Duration

	mu      sync.Mutex
	idle    []*helperProc
	running int
	busy    int
	wake    chan struct{} // closed when a helper is freed or a slot opens
	backoff time.Duration
	retryAt time.Time
	zombies []*helperProc
	kills   []time.Time
	lastErr string
	idleT   *time.Timer
	reapT   *time.Timer
	reapIn  time.Duration
	closed  bool

	nextID  atomic.Uint64
	starts  atomic.Int64
	killed  atomic.Int64
	crashes atomic.Int64
}

func newHelperPool(root string, cfg MountHelper) *helperPool {
	if cfg.Size <= 0 {
		cfg.Size = DefaultMountHelpers
	}
	if cfg.Max <= 0 {
		cfg.Max = 2 * cfg.Size
	}
	cfg.Max = max(cfg.Max, cfg.Size+1)
	p := &helperPool{root: filepath.Clean(root), cfg: cfg, now: time.Now, idleExit: cfg.IdleExit,
		wake: make(chan struct{})}
	if p.idleExit <= 0 {
		p.idleExit = DefaultMountHelperIdle
	}
	p.start = p.startProc
	p.reapFn = reapProcess
	return p
}

type helperProc struct {
	proc     *os.Process
	in       *os.File // the helper's stdin
	out      *os.File // the helper's stdout
	r        *bufio.Reader
	lastUsed time.Time
}

func (p *helperPool) startProc() (*helperProc, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	args := append(append([]string{p.cfg.Exe}, p.cfg.Args...), "-root", p.root)
	env := p.cfg.Env
	if env == nil {
		env = []string{}
	}
	proc, err := os.StartProcess(p.cfg.Exe, args, &os.ProcAttr{Env: env, Files: []*os.File{inR, outW, os.Stderr},
		Sys: helperSysProcAttr()})
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, err
	}
	return &helperProc{proc: proc, in: inW, out: outR, r: bufio.NewReader(outR)}, nil
}

// notify wakes the callers waiting for a helper. Caller holds mu.
func (p *helperPool) notify() {
	close(p.wake)
	p.wake = make(chan struct{})
}

// acquire returns an idle helper, or starts one in a free slot once the back-off allows, waiting up to deadline.
// Killed helpers not yet gone count toward cfg.Max: with no room left it waits for a running helper to come back,
// and with none running fails at once (errHelperStuck) rather than start another.
func (p *helperPool) acquire(ctx context.Context, deadline time.Time) (*helperProc, error) {
	for {
		p.mu.Lock()
		p.reap()
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("%w: closed", errHelperStart)
		}
		if n := len(p.idle); n > 0 {
			h := p.idle[n-1]
			p.idle = p.idle[:n-1]
			p.busy++
			p.mu.Unlock()
			return h, nil
		}
		wait := deadline
		if p.running < p.cfg.Size && p.running+len(p.zombies) >= p.cfg.Max {
			if p.running == 0 {
				err := fmt.Errorf("%w (%d of at most %d)", errHelperStuck, len(p.zombies), p.cfg.Max)
				p.mu.Unlock()
				return nil, err
			}
		} else if p.running < p.cfg.Size {
			if now := p.now(); !now.Before(p.retryAt) {
				p.running++
				p.busy++
				p.mu.Unlock()
				h, err := p.start()
				p.starts.Add(1)
				if err != nil {
					p.mu.Lock()
					p.running--
					p.busy--
					p.crashes.Add(1)
					p.failed(err.Error())
					p.notify()
					p.mu.Unlock()
					return nil, fmt.Errorf("%w: %v", errHelperStart, err)
				}
				return h, nil
			} else if p.retryAt.Before(wait) {
				wait = p.retryAt
			}
		}
		wake := p.wake
		p.mu.Unlock()
		t := time.NewTimer(time.Until(wait))
		select {
		case <-wake:
			t.Stop()
		case <-t.C:
			if !time.Now().Before(deadline) {
				// Every helper alive and busy is load, not the mount; a slot held back by the back-off or by
				// helpers stuck in the kernel is the mount failing.
				p.mu.Lock()
				full := p.running >= p.cfg.Size
				p.mu.Unlock()
				if full {
					return nil, errHelperBusy
				}
				return nil, errHelperBackoff
			}
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// failed records a kill or crash and pushes the next start back: 0 for the first in a row, then 250ms doubling up
// to helperBackoffMax. Caller holds mu.
func (p *helperPool) failed(why string) {
	now := p.now()
	p.lastErr = why
	p.kills = append(p.kills, now)
	if len(p.kills) > 64 {
		p.kills = p.kills[len(p.kills)-64:]
	}
	p.retryAt = now.Add(p.backoff)
	switch {
	case p.backoff == 0:
		p.backoff = 250 * time.Millisecond
	case p.backoff < helperBackoffMax:
		p.backoff = min(2*p.backoff, helperBackoffMax)
	}
}

// release puts a helper that answered back in the pool.
func (p *helperPool) release(h *helperProc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy--
	p.backoff = 0
	if p.closed {
		p.running--
		p.kill(h)
		return
	}
	h.lastUsed = p.now()
	p.idle = append(p.idle, h)
	if p.idleT == nil {
		p.idleT = time.AfterFunc(p.idleExit, p.stopIdle)
	}
	p.notify()
}

// discard kills a helper that did not answer (or whose call was given up). Caller does not hold mu.
func (p *helperPool) discard(h *helperProc, why string, counted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy--
	p.running--
	p.kill(h)
	if counted {
		p.failed(why)
	}
	p.notify()
}

// kill sends SIGKILL and closes the pipes. A helper blocked in the kernel (D state) may stay until the call
// returns; it holds nothing of Theatarr's, and reap collects it without waiting. Caller holds mu.
func (p *helperPool) kill(h *helperProc) {
	_ = h.proc.Kill()
	h.in.Close()
	h.out.Close()
	p.zombies = append(p.zombies, h)
	p.reap()
	p.scheduleReap()
}

// scheduleReap retries reap later while killed helpers remain: 50ms, doubling to 30s. Caller holds mu.
func (p *helperPool) scheduleReap() {
	if len(p.zombies) == 0 {
		p.reapIn = 0
		return
	}
	if p.reapT != nil {
		return
	}
	p.reapIn = min(max(2*p.reapIn, 50*time.Millisecond), helperBackoffMax)
	p.reapT = time.AfterFunc(p.reapIn, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.reapT = nil
		n := len(p.zombies)
		p.reap()
		if len(p.zombies) < n {
			p.notify() // room to start a helper again
		}
		p.scheduleReap()
	})
}

// reap collects killed helpers that have gone, without blocking. Caller holds mu.
func (p *helperPool) reap() {
	kept := p.zombies[:0]
	for _, z := range p.zombies {
		if !p.reapFn(z.proc) {
			kept = append(kept, z)
		}
	}
	clear(p.zombies[len(kept):])
	p.zombies = kept
}

// stopIdle ends helpers unused for idleExit (they exit at EOF on stdin).
func (p *helperPool) stopIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idleT = nil
	cut := p.now().Add(-p.idleExit)
	kept := p.idle[:0]
	for _, h := range p.idle {
		if h.lastUsed.After(cut) {
			kept = append(kept, h)
			continue
		}
		p.running--
		p.kill(h)
	}
	clear(p.idle[len(kept):])
	p.idle = kept
	if len(p.idle) > 0 {
		p.idleT = time.AfterFunc(p.idleExit, p.stopIdle)
	}
	p.notify()
}

// close ends every idle helper; busy ones end when their call does.
func (p *helperPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, h := range p.idle {
		p.running--
		p.kill(h)
	}
	p.idle = nil
	if p.idleT != nil {
		p.idleT.Stop()
		p.idleT = nil
	}
	p.notify()
}

// state reports the pool.
func (p *helperPool) state() HelperState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reap()
	s := HelperState{Size: p.cfg.Size, Max: p.cfg.Max, Running: p.running, Busy: p.busy, Starts: p.starts.Load(),
		Kills: p.killed.Load(), Crashes: p.crashes.Load(), Lingering: len(p.zombies), LastError: p.lastErr}
	cut := p.now().Add(-helperKillWindow)
	for _, k := range p.kills {
		if k.After(cut) {
			s.RecentKills++
		}
	}
	if n := len(p.kills); n > 0 {
		s.LastKill = p.kills[n-1]
	}
	if p.retryAt.After(p.now()) {
		s.RetryAt = p.retryAt
	}
	return s
}

// call sends one request and waits for its answer until deadline. A missed deadline kills the helper
// (errHelperTimeout); a helper that ends or answers out of turn is killed too (errHelperCrashed); a cancelled ctx
// kills it without counting it (the call may have been fine). No goroutine waits on the helper: the pipes are
// read and written with deadlines.
func (p *helperPool) call(ctx context.Context, deadline time.Time, req helperRequest) (*helperResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, err := p.acquire(ctx, deadline)
	if err != nil {
		return nil, err
	}
	req.ID = p.nextID.Add(1)
	_ = h.in.SetWriteDeadline(deadline)
	_ = h.out.SetReadDeadline(deadline)
	stop := context.AfterFunc(ctx, func() {
		_ = h.in.SetWriteDeadline(time.Now())
		_ = h.out.SetReadDeadline(time.Now())
	})
	var resp helperResponse
	err = writeFrame(h.in, req)
	if err == nil {
		err = readFrame(h.r, &resp)
	}
	cancelled := !stop() // the AfterFunc ran (or runs): this helper's deadlines are no longer ours
	switch {
	case err == nil && resp.ID == req.ID && !cancelled:
		p.release(h)
		return &resp, nil
	case err == nil && resp.ID == req.ID:
		p.discard(h, "", false)
		return &resp, nil
	case cancelled || ctx.Err() != nil:
		p.discard(h, "", false)
		return nil, ctx.Err()
	case errors.Is(err, os.ErrDeadlineExceeded):
		p.killed.Add(1)
		why := fmt.Sprintf("%s %s: no answer in time", req.Op, req.Path)
		p.discard(h, why, true)
		return nil, fmt.Errorf("%w (%s)", errHelperTimeout, why)
	default:
		if err == nil {
			err = fmt.Errorf("answer %d to request %d", resp.ID, req.ID)
		}
		p.crashes.Add(1)
		why := fmt.Sprintf("%s %s: %v", req.Op, req.Path, err)
		p.discard(h, why, true)
		return nil, fmt.Errorf("%w (%s)", errHelperCrashed, why)
	}
}

// helperFS is the mount's calls made through the helper pool for one guarded call: every call answers by the
// deadline or fails. done is set when the guarded call has returned.
type helperFS struct {
	m        *Mount
	pool     *helperPool
	ctx      context.Context
	deadline time.Time
	done     atomic.Bool
}

func (f *helperFS) do(op, p string, n int, max int64) (*helperResponse, error) {
	resp, err := f.pool.call(f.ctx, f.deadline, helperRequest{Op: op, Path: []byte(p), N: n, Max: max})
	if err != nil {
		return nil, err
	}
	if err := resp.Err.decode(); err != nil {
		return nil, err
	}
	return resp, nil
}

func (f *helperFS) stat(op, name string) (os.FileInfo, error) {
	resp, err := f.do(op, name, 0, 0)
	if err != nil {
		return nil, err
	}
	if resp.Info == nil {
		return nil, fmt.Errorf("mount helper: %s %s: no info", op, name)
	}
	return helperFileInfo{*resp.Info}, nil
}

func (f *helperFS) Stat(name string) (os.FileInfo, error)  { return f.stat(opStat, name) }
func (f *helperFS) Lstat(name string) (os.FileInfo, error) { return f.stat(opLstat, name) }

func (f *helperFS) Readlink(name string) (string, error) {
	resp, err := f.do(opReadlink, name, 0, 0)
	if err != nil {
		return "", err
	}
	return string(resp.Target), nil
}

func (f *helperFS) EvalSymlinks(name string) (string, error) {
	resp, err := f.do(opEval, name, 0, 0)
	if err != nil {
		return "", err
	}
	return string(resp.Target), nil
}

func (f *helperFS) ReadDir(name string) ([]os.DirEntry, error) { return f.readDir(name, 0) }

func (f *helperFS) readDir(name string, n int) ([]os.DirEntry, error) {
	resp, err := f.do(opReadDir, name, n, 0)
	if err != nil {
		return nil, err
	}
	out := make([]os.DirEntry, len(resp.Entries))
	for i, e := range resp.Entries {
		out[i] = &helperDirEntry{fs: f, dir: name, e: e}
	}
	return out, nil
}

func (f *helperFS) probe(name string, max int64) (*probe.Result, error) {
	resp, err := f.do(opProbe, name, 0, max)
	if err != nil {
		return nil, err
	}
	if resp.Probe == nil {
		return nil, fmt.Errorf("mount helper: probe %s: no result", name)
	}
	return resp.Probe, nil
}

func (f *helperFS) head(name string, max int64) (int64, error) {
	resp, err := f.do(opHead, name, 0, max)
	if err != nil {
		return 0, err
	}
	return resp.Read, nil
}

func (f *helperFS) readRange(name string, off, n, rate int64) (int64, int64, error) {
	resp, err := f.pool.call(f.ctx, f.deadline, helperRequest{Op: opRange, Path: []byte(name), Off: off, Max: n, Rate: rate})
	if err != nil {
		return 0, 0, err
	}
	return resp.Read, resp.Size, resp.Err.decode()
}

// helperDirEntry is a listed entry. Its Info is an lstat through the helper: within the guarded call that listed
// it, under that call's deadline; after it, as a guarded call of its own.
type helperDirEntry struct {
	fs  *helperFS
	dir string
	e   helperEntry
}

func (d *helperDirEntry) Name() string      { return string(d.e.Name) }
func (d *helperDirEntry) IsDir() bool       { return fs.FileMode(d.e.Type).IsDir() }
func (d *helperDirEntry) Type() fs.FileMode { return fs.FileMode(d.e.Type) }
func (d *helperDirEntry) String() string    { return fs.FormatDirEntry(d) }

func (d *helperDirEntry) Info() (fs.FileInfo, error) {
	p := filepath.Join(d.dir, string(d.e.Name))
	if !d.fs.done.Load() {
		return d.fs.Lstat(p)
	}
	v, err := d.fs.m.withTimeout(context.Background(), p, func(f mountFS) (any, error) { return f.Lstat(p) })
	if err != nil {
		return nil, err
	}
	return v.(os.FileInfo), nil
}

// testMountHelper is the helper New uses when Options set none: nil, except in this package's tests run with
// THEATARR_TEST_MOUNT_HELPER=1, which run every library test's mount calls through helper processes.
var testMountHelper *MountHelper
