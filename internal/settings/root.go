package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe"
)

type rootQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// RootCheck is the read-only result of validating one proposed version root.
type RootCheck struct {
	Root     string `json:"root"`
	Resolved string `json:"resolved"`
	// Writable is true when the root was probed writable; only an active version's root is probed.
	Writable bool `json:"writable"`
}

// validateVersionChanges runs the version rules and then the bounded filesystem checks of changed roots.
func validateVersionChanges(ctx context.Context, q rootQueryer, previous, next *policy.Policy, bootstrap bool) error {
	if err := validateVersionRules(ctx, q, previous, next, bootstrap); err != nil {
		return err
	}
	return validateVersionRoots(ctx, previous, next, bootstrap)
}

// rootsToCheck is the versions whose roots are checked: all at bootstrap, else those whose roots changed or that
// become active. Unchanged versions are not stat'ed again; policy.Validate already checks the separation of
// active versions.
func rootsToCheck(previous, next *policy.Policy, key string, bootstrap bool) bool {
	before, after := previous.Versions[key], next.Versions[key]
	return bootstrap || before == nil || versionRootsChanged(previous, next, key) ||
		(before.Mode != policy.ModeActive && after.Mode == policy.ModeActive)
}

// validateVersionRules is the part of the version checks that reads the database and no filesystem: a save runs it
// inside the write transaction, after validateVersionRoots ran before it.
func validateVersionRules(ctx context.Context, q rootQueryer, previous, next *policy.Policy, bootstrap bool) error {
	for _, key := range sortedPolicyKeys(next.Versions) {
		before := previous.Versions[key]
		if bootstrap || !rootsToCheck(previous, next, key, false) {
			continue
		}
		if before != nil && versionRootsChanged(previous, next, key) {
			links, err := activeVersionLinks(ctx, q, key)
			if err != nil {
				return err
			}
			if links > 0 {
				return rootFieldError("versions["+key+"].library", fmt.Sprintf("%d active link(s) would stay at the old root; use the procedure in docs/migration.md#changing-a-root-with-links", links))
			}
		}
		if before == nil {
			// A key deleted and created again has no previous roots to compare: its active links left from before
			// must sit inside the new roots, as a root change requires.
			links, err := activeLinksOutside(ctx, q, key, configuredVersionRoots(next, key))
			if err != nil {
				return err
			}
			if links > 0 {
				return rootFieldError("versions["+key+"].library", fmt.Sprintf("%d active link(s) of this library are outside its roots; use the old root, or the procedure in docs/migration.md#changing-a-root-with-links", links))
			}
		}
	}
	return nil
}

// activeLinksOutside counts the active links of version key whose path is in none of roots.
func activeLinksOutside(ctx context.Context, q rootQueryer, key string, roots []string) (int, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.path FROM link l
		JOIN link_version lv ON lv.link_id = l.id
		JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state = 'active'`, key)
	if err != nil {
		return 0, errors.New("settings: cannot inspect library links")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return 0, errors.New("settings: cannot inspect library links")
		}
		inside := false
		for _, root := range roots {
			if rel, err := filepath.Rel(root, filepath.Clean(path)); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel) {
				inside = true
				break
			}
		}
		if !inside {
			count++
		}
	}
	if rows.Err() != nil {
		return 0, errors.New("settings: cannot inspect library links")
	}
	return count, nil
}

// validateVersionRoots checks the changed roots on the filesystem, bounded (policy.RootResolver, probeRoot). Roots may
// be anywhere outside the debrid mount (policy.ResolveLibraryRootWith), the trash and other libraries. It reads no
// database, so a save runs it before its write transaction.
func validateVersionRoots(ctx context.Context, previous, next *policy.Policy, bootstrap bool) error {
	// A root at or in the mount is refused by its path, before anything stats it.
	for _, key := range sortedPolicyKeys(next.Versions) {
		if !rootsToCheck(previous, next, key, bootstrap) {
			continue
		}
		for _, path := range configuredVersionRoots(next, key) {
			if err := lexicalMountSeparation(next, path); err != nil {
				return rootFieldError("versions["+key+"].library", err.Error())
			}
		}
	}
	// A save that changes no root and makes no version active (Pause, sections, a version back to
	// shadow) touches no filesystem, so a stuck check of some root never holds it.
	var keys []string
	for _, key := range sortedPolicyKeys(next.Versions) {
		if rootsToCheck(previous, next, key, bootstrap) {
			keys = append(keys, key)
		}
	}
	// Every root the checks consult (versions' roots, the trash) is resolved once, each through
	// its own probe slot (policy.RootResolver): a root that does not answer is refused by name, never charged to the
	// changed root, and no slot is held while another is taken.
	rr := policy.NewRootResolver(ctx)
	for _, key := range keys {
		field := "versions[" + key + "].library"
		if _, err := next.CheckRootSeparationWith(key, rr); err != nil {
			return rootFieldError(field, err.Error())
		}
		for _, path := range configuredVersionRoots(next, key) {
			root, err := next.ResolveLibraryRootWith(rr, path)
			if err != nil {
				return rootFieldError(field, resolveError("directory", err).Error())
			}
			if err := physicalPathSeparation(next, rr, key, root); err != nil {
				return rootFieldError(field, err.Error())
			}
		}
		if bootstrap {
			continue
		}
		for _, root := range configuredVersionRoots(next, key) {
			if _, err := validateOneRoot(ctx, rr, next, key, root, next.Versions[key].Mode == policy.ModeActive); err != nil {
				return rootFieldError(field, err.Error())
			}
		}
	}
	return nil
}

// resolveError is the message for a root (what) that could not be resolved: by name when it does not answer.
func resolveError(what string, err error) error {
	if errors.Is(err, policy.ErrRootUnreachable) {
		return fmt.Errorf("%s %w", what, err)
	}
	return errors.New(what + " cannot be resolved")
}

// rootCheckTimeout bounds the filesystem part of root validation; rootStat is its stat of a proposed root (tests
// replace it).
var (
	rootCheckTimeout = 15 * time.Second
	rootStat         = os.Stat
)

// probeRoot runs f, the filesystem calls on the proposed root path that resolution does not make (its stat and
// writability), through the process's root prober (rootprobe.Default) until ctx ends or rootCheckTimeout passes: a
// root on a filesystem that stops answering fails the check instead of holding the request. f never touches the
// debrid mount (policy.ResolvePath) nor calls the prober. A call stuck in the kernel cannot be stopped; until it
// returns, new checks on the same filesystem are refused at once instead of piling more blocked calls onto it, and
// checks of other filesystems run.
func probeRoot(ctx context.Context, path string, f func() error) error {
	err := rootprobe.Default.Do(ctx, []string{path}, rootCheckTimeout, f)
	var stuck *rootprobe.StuckError
	var timeout *rootprobe.TimeoutError
	switch {
	case errors.As(err, &stuck):
		return errors.New("an earlier root check of " + stuck.Root + " is still waiting on a filesystem that " +
			"does not answer; try again once that directory answers")
	case errors.As(err, &timeout):
		return errors.New("the root check gave up after " + timeout.After.String() + ": " + timeout.Root +
			" is on a filesystem that does not answer")
	}
	return err
}

// lexicalMountSeparation refuses a root at, in or above the configured mount by its path alone, before any
// filesystem call on it.
func lexicalMountSeparation(p *policy.Policy, path string) error {
	if p.Library == nil {
		return nil
	}
	for _, m := range []string{p.Library.Mount.Path, p.Library.Mount.LinkPath} {
		if m != "" && filepath.IsAbs(m) && (policy.Within(path, m) || policy.Within(m, path)) {
			return errors.New("directory overlaps the library mount")
		}
	}
	return nil
}

func rootFieldError(path, message string) error {
	return &policy.Errors{List: []policy.FieldError{{Path: path, Msg: message}}}
}

func configuredVersionRoots(p *policy.Policy, key string) []string {
	var roots []string
	if v := p.Versions[key]; v != nil && v.Library != "" {
		roots = append(roots, filepath.Clean(v.Library))
	}
	if p.Library != nil && p.Library.Versions[key] != nil {
		for _, root := range p.Library.Versions[key].Roots {
			roots = append(roots, filepath.Clean(root))
		}
	}
	sort.Strings(roots)
	return slices.Compact(roots)
}

func versionRootsChanged(before, after *policy.Policy, key string) bool {
	a, b := configuredVersionRoots(before, key), configuredVersionRoots(after, key)
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

// validateOneRoot checks a proposed root of version key in p. rr resolves every root it consults (the operation's RootResolver); the root's own stat and
// writability are bounded by probeRoot.
func validateOneRoot(ctx context.Context, rr *policy.RootResolver, p *policy.Policy, key, path string, writable bool) (RootCheck, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return RootCheck{}, errors.New("must be an absolute, clean path")
	}
	if path == "/" {
		return RootCheck{}, errors.New("directory cannot be the filesystem root")
	}
	if err := lexicalMountSeparation(p, path); err != nil {
		return RootCheck{}, err
	}
	root, err := p.ResolveLibraryRootWith(rr, path)
	if errors.Is(err, policy.ErrInMount) {
		return RootCheck{}, errors.New("directory overlaps the library mount")
	}
	if err != nil {
		return RootCheck{}, resolveError("directory", err)
	}
	if root.Resolved == "/" {
		return RootCheck{}, errors.New("directory cannot be the filesystem root")
	}
	var check error
	if err := probeRoot(ctx, path, func() error {
		info, err := rootStat(root.Resolved)
		switch {
		case err != nil:
			check = errors.New("directory must exist")
		case !info.IsDir():
			check = errors.New("path is not a directory")
		case writable && rootWritable(path) != nil:
			check = errors.New("active library root is not writable by Theatarr")
		}
		return nil
	}); err != nil {
		return RootCheck{}, err // a check that gave up may still write check
	}
	if check != nil {
		return RootCheck{}, check
	}
	if err := physicalPathSeparation(p, rr, key, root); err != nil {
		return RootCheck{}, err
	}
	return RootCheck{Root: path, Resolved: root.Resolved, Writable: writable}, nil
}

func physicalPathSeparation(p *policy.Policy, rr *policy.RootResolver, key string, root policy.ResolvedRoot) error {
	for _, other := range sortedPolicyKeys(p.Versions) {
		if other == key {
			continue
		}
		for _, path := range configuredVersionRoots(p, other) {
			resolved, err := p.ResolveLibraryRootWith(rr, path)
			if err != nil {
				return resolveError(fmt.Sprintf("library %s root %q", other, path), err)
			}
			if root.Overlaps(resolved) {
				return fmt.Errorf("directory overlaps library %s root %q", other, path)
			}
		}
	}
	if p.Library != nil {
		if path := p.Library.Trash.Dir; path != "" && filepath.IsAbs(path) {
			resolved, err := p.ResolveLibraryRootWith(rr, path)
			if err != nil {
				return resolveError("library trash", err)
			}
			if root.Overlaps(resolved) {
				return errors.New("directory overlaps the library trash")
			}
		}
		// The mount is compared by path only: a stat of a hung or disconnected FUSE mount would block or fail
		// the save (the library's mount guard and breaker exist for that).
		for _, path := range []string{p.Library.Mount.Path, p.Library.Mount.LinkPath} {
			if path == "" || !filepath.IsAbs(path) {
				continue
			}
			for _, r := range []string{root.Path, root.Resolved} {
				if policy.Within(r, path) || policy.Within(path, r) {
					return errors.New("directory overlaps the library mount")
				}
			}
		}
	}
	return nil
}

func activeVersionLinks(ctx context.Context, q rootQueryer, key string) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM link l
		JOIN link_version lv ON lv.link_id = l.id
		JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state = 'active'`, key).Scan(&count)
	if err != nil {
		return 0, errors.New("settings: cannot inspect library links")
	}
	return count, nil
}

// ActiveVersionLinks counts managed links without changing their rows or paths.
func (s *Store) ActiveVersionLinks(ctx context.Context, key string) (int, error) {
	return activeVersionLinks(ctx, s.db.Reader(), key)
}

func versionLinks(ctx context.Context, q rootQueryer, key string) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM link l
		JOIN link_version lv ON lv.link_id = l.id
		JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state != 'unlinked'`, key).Scan(&count)
	if err != nil {
		return 0, errors.New("settings: cannot inspect library links")
	}
	return count, nil
}

// VersionLinks counts live or dangling managed link rows; it never changes them.
func (s *Store) VersionLinks(ctx context.Context, key string) (int, error) {
	return versionLinks(ctx, s.db.Reader(), key)
}

// ValidateRoot performs the no-write path check used by the admin root validator.
func (s *Store) ValidateRoot(ctx context.Context, base *policy.Policy, key, root string) (RootCheck, error) {
	snap, err := s.Load(ctx, base)
	if err != nil {
		return RootCheck{}, err
	}
	if !filepath.IsAbs(root) {
		return RootCheck{}, rootFieldError("root", "must be an absolute path, such as /media/movies")
	}
	version := snap.Policy.Versions[key]
	// One resolver for the whole check: each root it consults is resolved once, through its own probe slot.
	rr := policy.NewRootResolver(ctx)
	if version == nil {
		checked, err := validateOneRoot(ctx, rr, snap.Policy, key, root, false)
		if err != nil {
			return RootCheck{}, rootFieldError("root", err.Error())
		}
		return checked, nil
	}
	p, err := snap.Policy.Clone()
	if err != nil {
		return RootCheck{}, errors.New("settings: cannot copy policy")
	}
	p.Versions[key].Library = root
	if p.Library == nil {
		return RootCheck{}, errors.New("settings: library roots are not configured")
	}
	section := p.Library.Versions[key]
	if section == nil {
		section = &policy.LibraryVersion{}
		p.Library.Versions[key] = section
	}
	section.Roots = []string{root}
	// The root is refused by its path alone before anything stats it; the rest is bounded.
	if root == "/" {
		return RootCheck{}, rootFieldError("root", "directory cannot be the filesystem root")
	}
	if err := lexicalMountSeparation(p, root); err != nil {
		return RootCheck{}, rootFieldError("root", err.Error())
	}
	// Validate bounds its own root calls (and compares a root that does not answer by its path). The separation of
	// root itself is then checked in full.
	if err := p.Validate(); err != nil {
		return RootCheck{}, err
	}
	if v := p.Versions[key]; v.Mode == policy.ModeActive {
		if _, err := p.CheckRootSeparationWith(key, rr); err != nil {
			return RootCheck{}, rootFieldError("root", err.Error())
		}
	}
	if root != version.Library {
		links, err := s.ActiveVersionLinks(ctx, key)
		if err != nil {
			return RootCheck{}, err
		}
		if links > 0 {
			return RootCheck{}, rootFieldError("root", "active links would stay at the old root; use the procedure in docs/migration.md#changing-a-root-with-links")
		}
	}
	checked, err := validateOneRoot(ctx, rr, p, key, root, version.Mode == policy.ModeActive)
	if err != nil {
		return RootCheck{}, rootFieldError("root", err.Error())
	}
	return checked, nil
}
