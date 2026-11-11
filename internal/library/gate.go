package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe"
)

// ErrRefused matches every gate refusal.
var ErrRefused = errors.New("library: write refused")

// Refused is a gate refusal: why a write may not happen.
type Refused struct {
	Reason string // disabled, shadow, unlisted, outside, symlinked, not_symlink, unreachable
	Detail string
}

func (r *Refused) Error() string { return "library: write refused (" + r.Reason + "): " + r.Detail }

// Is matches ErrRefused, and ErrRootDown for a root that does not answer.
func (r *Refused) Is(err error) bool {
	return err == ErrRefused || (err == ErrRootDown && r.Reason == "unreachable")
}

// ErrRootDown is a library root (or the debrid mount's parent) on a filesystem that does not answer: the gate's
// "unreachable" refusal. Like ErrMountDown it is a pause, not a verdict: nothing under that root is read (a read of
// a hung share blocks, unbounded) and nothing is planned or recorded for it; the work is retried once it answers.
var ErrRootDown = errors.New("library: a library root does not answer")

// Unavailable reports whether err is a filesystem that does not answer: the debrid mount (ErrMountDown) or a library
// root (ErrRootDown). The caller stops what it does there and retries later, recording nothing.
func Unavailable(err error) bool {
	return errors.Is(err, ErrMountDown) || errors.Is(err, ErrRootDown)
}

// Gate decides whether a filesystem write or a Plex call may happen (docs/design/library.md "The gate"):
// library.enabled must be true, the version must be mode: active and listed under library.versions, and the path
// must be under one of that version's roots (the trash directory for links moved aside). Paths are cleaned and
// their parent directories resolved, so a symlinked directory inside a root cannot lead a write outside it, and no
// root or directory may lead into the debrid mount (policy.ResolvePath, which never touches the mount itself).
type Gate struct {
	policy   *policy.Policy
	enabled  bool
	trash    string
	versions map[string]gateVersion
}

type gateVersion struct {
	active   bool
	followed bool // other versions follow it: its roots are its arr's
	roots    []string
}

// NewGate builds the gate from the policy.
func NewGate(p *policy.Policy) *Gate {
	s := p.LibrarySettings()
	g := &Gate{policy: p, enabled: s.Enabled, trash: s.TrashDir, versions: map[string]gateVersion{}}
	for k, lv := range s.Versions {
		vp := p.Versions[k]
		// A followed version's roots are its arr's: never written, whatever its mode says (policy.validateFollow
		// refuses that mode too).
		active := vp != nil && vp.Mode == policy.ModeActive && len(p.Followers(k)) == 0
		g.versions[k] = gateVersion{active: active, followed: len(p.Followers(k)) > 0, roots: lv.Roots}
	}
	return g
}

// Enabled reports library.enabled.
func (g *Gate) Enabled() bool { return g.enabled }

// Active reports whether a version may write at all (enabled, active, listed).
func (g *Gate) Active(version string) bool {
	v, ok := g.versions[version]
	return g.enabled && ok && v.active && len(v.roots) > 0
}

// Roots returns a version's roots.
func (g *Gate) Roots(version string) []string { return g.versions[version].roots }

// Versions returns the versions listed under library.versions, sorted.
func (g *Gate) Versions() []string {
	var ks []string
	for k := range g.versions {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Summary describes the gate for the startup log.
func (g *Gate) Summary() string {
	if !g.enabled {
		return "library.enabled is false: no filesystem writes and no Plex calls; shadow dry runs only"
	}
	var active, shadow []string
	for _, k := range g.Versions() {
		if g.Active(k) {
			active = append(active, k+" "+strings.Join(g.versions[k].roots, ","))
		} else {
			shadow = append(shadow, k)
		}
	}
	return fmt.Sprintf("library writes enabled for: %s; dry run only: %s", orNone(active), orNone(shadow))
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, "; ")
}

// CheckVersion refuses a write for a version that may not write.
func (g *Gate) CheckVersion(version string) error {
	return g.checkVersion(version, policy.NewRootResolver(context.Background()))
}

// checkVersion is CheckVersion with the roots resolved through rr, shared with the rest of the gate operation.
func (g *Gate) checkVersion(version string, rr *policy.RootResolver) error {
	if !g.enabled {
		return &Refused{Reason: "disabled", Detail: "library.enabled is false"}
	}
	v, ok := g.versions[version]
	if !ok {
		return &Refused{Reason: "unlisted", Detail: fmt.Sprintf("version %q is not listed under library.versions", version)}
	}
	if v.followed {
		return &Refused{Reason: "shadow", Detail: fmt.Sprintf("version %q is followed: its roots are its arr's and stay read-only", version)}
	}
	if !v.active {
		return &Refused{Reason: "shadow", Detail: fmt.Sprintf("version %q is not Live", version)}
	}
	// Every root is resolved through the root prober: a root on a share that stops answering refuses the write
	// ("not answering") after one bounded wait, and at once while that call is stuck, instead of holding the
	// caller's link locks and worker slot on a filesystem call nothing can interrupt.
	if _, err := g.policy.CheckRootSeparationWith(version, rr); err != nil {
		if errors.Is(err, policy.ErrRootUnreachable) {
			return &Refused{Reason: "unreachable", Detail: err.Error()}
		}
		return &Refused{Reason: "symlinked", Detail: err.Error()}
	}
	return nil
}

// CheckPath refuses a write to path for version unless it is under one of the version's roots. One resolver serves
// the whole check: the roots, the mount's aliases and path's directory are each resolved once, bounded.
func (g *Gate) CheckPath(version, path string) error {
	rr := policy.NewRootResolver(context.Background())
	if err := g.checkVersion(version, rr); err != nil {
		return err
	}
	return g.checkUnder(rr, path, g.versions[version].roots)
}

// CheckTrash refuses a write to path unless it is under the trash directory.
func (g *Gate) CheckTrash(version, path string) error {
	rr := policy.NewRootResolver(context.Background())
	if err := g.checkVersion(version, rr); err != nil {
		return err
	}
	if g.trash == "" {
		return &Refused{Reason: "outside", Detail: "library.trash.dir is not set"}
	}
	return g.checkUnder(rr, path, []string{g.trash})
}

// Reachable refuses ("unreachable", ErrRootDown) a root, whatever its version's mode, whose filesystem does not
// answer, or one of the filesystems mounted below it (a walk of the root crosses into them): each is resolved once
// through the root prober, bounded and charged to its own filesystem, so a scan or a plan reads nothing under a hung
// share. Any other finding about the root is left to the gate's checks.
func (g *Gate) Reachable(ctx context.Context, root string) error {
	rr := policy.NewRootResolver(ctx)
	mounts, err := g.policy.MountPathsWith(rr)
	if errors.Is(err, policy.ErrRootUnreachable) {
		return &Refused{Reason: "unreachable", Detail: err.Error()}
	}
	for _, dir := range append([]string{root}, rootprobe.MountsBelow(root)...) {
		if _, err := rr.Resolve(dir, mounts); errors.Is(err, policy.ErrRootUnreachable) {
			return &Refused{Reason: "unreachable", Detail: fmt.Sprintf("%q: %v", dir, err)}
		}
	}
	return nil
}

// CheckPlex refuses Plex calls while the library is disabled.
func (g *Gate) CheckPlex() error {
	if !g.enabled {
		return &Refused{Reason: "disabled", Detail: "library.enabled is false"}
	}
	return nil
}

// checkUnder refuses path unless it is under one of roots, resolved through rr: the roots (resolved already by the
// gate's root check), the mount's aliases and path's directory are each one bounded call through the root prober, so
// a root that answers but holds a hung subtree, or a hung share above the mount, refuses the write ("unreachable")
// instead of holding the caller's link locks.
func (g *Gate) checkUnder(rr *policy.RootResolver, path string, roots []string) error {
	if !filepath.IsAbs(path) {
		return &Refused{Reason: "outside", Detail: fmt.Sprintf("%q is not absolute", path)}
	}
	clean := filepath.Clean(path)
	if clean != path && clean+"/" != path {
		return &Refused{Reason: "outside", Detail: fmt.Sprintf("%q is not a clean path", path)}
	}
	mounts, err := g.policy.MountPathsWith(rr)
	if err != nil {
		return &Refused{Reason: refusedReason(err), Detail: err.Error()}
	}
	for _, m := range mounts {
		if policy.Within(clean, m) {
			return &Refused{Reason: "outside", Detail: fmt.Sprintf("%q is inside the mount %q", clean, m)}
		}
	}
	for _, r := range roots {
		if clean == r || !policy.Within(clean, r) {
			continue
		}
		// Resolve both through the filesystem: a symlinked directory must not lead outside the root, nor into the
		// mount (the mount paths include its physical aliases: policy.MountPaths).
		root, err := rr.Resolve(r, mounts)
		if err != nil {
			return &Refused{Reason: refusedReason(err), Detail: fmt.Sprintf("root %q: %v", r, err)}
		}
		realRoot := root.Resolved
		realDir, err := rr.ResolvePath(filepath.Dir(clean), mounts)
		if err != nil {
			return &Refused{Reason: refusedReason(err), Detail: fmt.Sprintf("%q: %v", clean, err)}
		}
		if !policy.Within(realDir, realRoot) {
			return &Refused{Reason: "symlinked", Detail: fmt.Sprintf("%q resolves to %q, outside %q", filepath.Dir(clean), realDir, realRoot)}
		}
		return nil
	}
	return &Refused{Reason: "outside", Detail: fmt.Sprintf("%q is under none of %s", clean, strings.Join(roots, ", "))}
}

func refusedReason(err error) string {
	switch {
	case errors.Is(err, policy.ErrInMount):
		return "symlinked"
	case errors.Is(err, policy.ErrRootUnreachable):
		return "unreachable"
	}
	return "outside"
}
