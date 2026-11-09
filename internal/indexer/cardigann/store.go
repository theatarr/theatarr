package cardigann

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the definitions sync source: the catalog the engine runs from, kept in a data folder and refreshed from
// the definitions repository through Fetch (the gateway). Nothing of the repository is committed or shipped; the
// last good snapshot stays on disk, so a failed refresh keeps the catalog as it was.
//
// The operator's own definitions in custom/ (a site the repository does not carry) are listed with the synced ones
// and win over a synced one with the same id. The sync never writes there; they are read at start and again at each
// refresh, and nothing about them is held.
//
// A refresh applies by itself except where it could change what a configured indexer does behind the
// administrator's back. For a definition that an indexer uses (InUse), these wait as holds until approved, and the
// indexer keeps running the definition it had:
//   - new_required_settings: it asks for a setting it did not need before;
//   - removed_in_use: it is gone from the repository;
//   - newly_unsupported: it now needs something this engine cannot run.
type Store struct {
	// Dir is the data folder (<data>/cardigann/definitions).
	Dir string
	// Fetch reads the repository; nil means the catalog never refreshes.
	Fetch Fetcher
	// InUse lists the definition ids configured indexers use; nil means none.
	InUse  func() []string
	Logger *slog.Logger
	now    func() time.Time

	refreshMu sync.Mutex // one refresh at a time
	mu        sync.Mutex
	loaded    bool
	state     storeState
	defs      map[string]*storeEntry // applied definitions by id
	custom    map[string]*storeEntry // the operator's definitions (custom/) by id
}

// Fetcher reads the definitions repository: the newest commit of the tracked branch, and that commit's archive.
type Fetcher interface {
	Commit(ctx context.Context) (string, error)
	Archive(ctx context.Context, commit string) ([]byte, error)
}

// Hold reasons.
const (
	HoldNewRequiredSettings = "new_required_settings"
	HoldRemovedInUse        = "removed_in_use"
	HoldNewlyUnsupported    = "newly_unsupported"
)

// Hold is an update of an in-use definition that waits for review.
type Hold struct {
	ID      string   `json:"id"`
	Reasons []string `json:"reasons"`
	// Detail says what changed, for the review.
	Detail []string `json:"detail"`
	// Commit is the repository commit the update came from.
	Commit string    `json:"commit"`
	Since  time.Time `json:"since"`
	// Removed: approving drops the definition (indexers using it stop working); otherwise approving applies the
	// held text.
	Removed bool `json:"removed,omitempty"`
	// Hash identifies the held text.
	Hash string `json:"hash,omitempty"`
}

// Status is the sync state, for the catalog API and health.
type Status struct {
	Commit      string    `json:"commit,omitempty"`
	Schema      int       `json:"schema,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	LastCheck   time.Time `json:"last_check,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	Definitions int       `json:"definitions"`
	Holds       int       `json:"holds"`
}

type storeState struct {
	Commit    string    `json:"commit,omitempty"`
	Schema    int       `json:"schema,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	LastCheck time.Time `json:"last_check,omitzero"`
	LastError string    `json:"last_error,omitempty"`
	Holds     []Hold    `json:"holds,omitempty"`
}

type storeEntry struct {
	data []byte
	hash string
	def  *Definition // nil when the file does not parse
	err  error
	sum  *Summary
	// custom: read from custom/, not synced
	custom bool
}

const (
	stateFile  = "state.json"
	currentDir = "current"
	heldDir    = "held"
	customDir  = "custom"
)

// ErrNoDefinition means the catalog has no definition with that id.
var ErrNoDefinition = errors.New("cardigann: no such definition in the catalog")

// ErrNoHold is returned by Approve for a definition with no held update.
var ErrNoHold = errors.New("cardigann: no held update")

func (s *Store) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// loadLocked reads the snapshot from disk once.
func (s *Store) loadLocked() error {
	if s.loaded {
		return nil
	}
	s.defs = map[string]*storeEntry{}
	if raw, err := os.ReadFile(filepath.Join(s.Dir, stateFile)); err == nil {
		if err := json.Unmarshal(raw, &s.state); err != nil {
			return fmt.Errorf("cardigann: read %s: %w", stateFile, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cardigann: read %s: %w", stateFile, err)
	}
	files, err := filepath.Glob(filepath.Join(s.Dir, currentDir, "*.yml"))
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("cardigann: read %s: %w", filepath.Base(f), err)
		}
		s.defs[strings.TrimSuffix(filepath.Base(f), ".yml")] = newEntry(data)
	}
	s.loadCustomLocked()
	s.loaded = true
	return nil
}

// loadCustomLocked reads custom/*.yml. A file is checked like a synced one: one that does not parse or has an
// unusable id is left out, with a warning, since the operator put it there.
func (s *Store) loadCustomLocked() {
	s.custom = map[string]*storeEntry{}
	files, _ := filepath.Glob(filepath.Join(s.Dir, customDir, "*.yml"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			s.log().Warn("cardigann: custom definition not read", "file", filepath.Base(f), "err", err)
			continue
		}
		e := newEntry(data)
		if e.def == nil || !definitionFileRe.MatchString(e.def.ID) {
			s.log().Warn("cardigann: custom definition left out: it does not parse or has no usable id", "file", filepath.Base(f), "err", e.err)
			continue
		}
		e.custom = true
		s.custom[e.def.ID] = e
	}
}

// appliedLocked is what runs: the synced definitions, with the custom ones over them.
func (s *Store) appliedLocked() map[string]*storeEntry {
	if len(s.custom) == 0 {
		return s.defs
	}
	out := make(map[string]*storeEntry, len(s.defs)+len(s.custom))
	for id, e := range s.defs {
		out[id] = e
	}
	for id, e := range s.custom {
		out[id] = e
	}
	return out
}

func newEntry(data []byte) *storeEntry {
	sum := sha256.Sum256(data)
	e := &storeEntry{data: data, hash: hex.EncodeToString(sum[:])}
	e.def, e.err = ParseDefinition(data)
	return e
}

func (e *storeEntry) summary() Summary {
	if e.sum == nil {
		sum := Summarize(e.def)
		sum.Custom = e.custom
		e.sum = &sum
	}
	return *e.sum
}

// Load returns an applied definition (Definitions for the engine). A definition renamed upstream is found by its
// old id too.
func (s *Store) Load(_ context.Context, id string) (*Definition, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, "", err
	}
	e := s.findLocked(id)
	if e == nil {
		if len(s.defs) == 0 && len(s.custom) == 0 {
			return nil, "", fmt.Errorf("%w: %q (the catalog has not been fetched yet)", ErrNoDefinition, id)
		}
		return nil, "", fmt.Errorf("%w: %q", ErrNoDefinition, id)
	}
	if e.err != nil {
		return nil, "", fmt.Errorf("definition %q: %w", id, e.err)
	}
	if err := e.def.Validate(); err != nil {
		return nil, "", err
	}
	return e.def, e.hash, nil
}

func (s *Store) findLocked(id string) *storeEntry {
	applied := s.appliedLocked()
	if e := applied[id]; e != nil {
		return e
	}
	for _, k := range sortedIDs(applied) {
		if e := applied[k]; e.def != nil && slices.Contains(e.def.Replaces, id) {
			return e
		}
	}
	return nil
}

// Summary describes one applied definition.
func (s *Store) Summary(id string) (Summary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadLocked() != nil {
		return Summary{}, false
	}
	e := s.findLocked(id)
	if e == nil || e.def == nil {
		return Summary{}, false
	}
	return e.summary(), true
}

// Catalog lists the applied definitions, sorted by name.
func (s *Store) Catalog() ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	applied := s.appliedLocked()
	out := make([]Summary, 0, len(applied))
	for _, id := range sortedIDs(applied) {
		if e := applied[id]; e.def != nil {
			out = append(out, e.summary())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Status reports the sync state.
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Status{LastError: err.Error()}
	}
	return Status{Commit: s.state.Commit, Schema: s.state.Schema, UpdatedAt: s.state.UpdatedAt, LastCheck: s.state.LastCheck,
		LastError: s.state.LastError, Definitions: len(s.appliedLocked()), Holds: len(s.state.Holds)}
}

// Holds lists the updates waiting for review.
func (s *Store) Holds() []Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadLocked() != nil {
		return nil
	}
	return slices.Clone(s.state.Holds)
}

// Due reports whether a refresh is due: never fetched, or the last check is older than every.
func (s *Store) Due(every time.Duration) bool {
	st := s.Status()
	return st.LastCheck.IsZero() || s.clock().Sub(st.LastCheck) >= every
}

// Refresh checks the repository and applies what may apply by itself. An unchanged commit costs one small request.
// On an error the catalog stays as it was.
func (s *Store) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.loaded {
		s.loadCustomLocked() // picks up files dropped in custom/ since the last read
	}
	s.mu.Unlock()
	if s.Fetch == nil {
		return errors.New("cardigann: no definitions source is configured")
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	err := s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastCheck = s.clock().UTC()
	s.state.LastError = ""
	if err != nil {
		s.state.LastError = err.Error()
		s.log().Warn("cardigann: definitions refresh failed; the catalog stays as it was", "err", err)
	}
	if werr := s.writeStateLocked(); werr != nil && err == nil {
		err = werr
	}
	return err
}

func (s *Store) refresh(ctx context.Context) error {
	commit, err := s.Fetch.Commit(ctx)
	if err != nil {
		return fmt.Errorf("cardigann: newest definitions commit: %w", err)
	}
	s.mu.Lock()
	if err := s.loadLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	same := commit == s.state.Commit && len(s.defs) > 0
	s.mu.Unlock()
	if same {
		return nil
	}
	archive, err := s.Fetch.Archive(ctx, commit)
	if err != nil {
		return fmt.Errorf("cardigann: definitions archive: %w", err)
	}
	files, schema, err := ExtractDefinitions(archive, SupportedSchema)
	if err != nil {
		return err
	}
	incoming := map[string]*storeEntry{}
	for _, data := range files {
		e := newEntry(data)
		if e.def == nil || !definitionFileRe.MatchString(e.def.ID) {
			continue // a file that does not parse is not listed
		}
		incoming[e.def.ID] = e
	}
	if len(incoming) == 0 {
		return errors.New("cardigann: the archive has no usable definitions")
	}
	var inUse []string
	if s.InUse != nil {
		inUse = s.InUse()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock().UTC()
	applied := map[string]*storeEntry{}
	for id, e := range incoming {
		applied[id] = e
	}
	oldHolds := map[string]Hold{}
	for _, h := range s.state.Holds {
		oldHolds[h.ID] = h
	}
	var holds []Hold
	held := map[string][]byte{}
	for _, id := range uniqueSorted(inUse) {
		old := s.defs[id]
		if old == nil || old.def == nil {
			continue // nothing applied to protect: the indexer takes what comes
		}
		next := incoming[id]
		h := Hold{ID: id, Commit: commit, Since: now}
		switch {
		case next == nil:
			h.Removed = true
			h.Reasons = []string{HoldRemovedInUse}
			h.Detail = []string{"the definition is no longer in the repository"}
			for _, e := range incoming {
				if slices.Contains(e.def.Replaces, id) {
					h.Detail = append(h.Detail, "it is replaced by "+e.def.ID)
				}
			}
		case next.hash == old.hash:
			continue
		default:
			h.Reasons, h.Detail = reviewReasons(old, next)
			h.Hash = next.hash
		}
		if len(h.Reasons) == 0 {
			continue
		}
		if prev, ok := oldHolds[id]; ok && prev.Hash == h.Hash && prev.Removed == h.Removed {
			h.Since = prev.Since // the same update still waits
		}
		applied[id] = old
		holds = append(holds, h)
		if next != nil {
			held[id] = next.data
		}
	}
	if err := s.writeSnapshotLocked(applied, held); err != nil {
		return err
	}
	s.defs = applied
	s.state.Commit, s.state.Schema, s.state.UpdatedAt, s.state.Holds = commit, schema, now, holds
	s.log().Info("cardigann: definitions refreshed", "commit", shortCommit(commit), "schema", schema, "definitions", len(applied), "holds", len(holds))
	return nil
}

// reviewReasons compares an in-use definition with its update.
func reviewReasons(old, next *storeEntry) (reasons, detail []string) {
	before, after := old.summary(), Summary{Supported: false}
	if next.def != nil {
		after = next.summary()
	}
	had := map[string]bool{}
	for _, n := range before.RequiredSettings() {
		had[n] = true
	}
	var added []string
	for _, n := range after.RequiredSettings() {
		if !had[n] {
			added = append(added, n)
		}
	}
	if len(added) > 0 {
		reasons = append(reasons, HoldNewRequiredSettings)
		detail = append(detail, "new required settings: "+strings.Join(added, ", "))
	}
	if before.Supported && !after.Supported {
		reasons = append(reasons, HoldNewlyUnsupported)
		detail = append(detail, "no longer runs here: "+strings.Join(after.Problems, "; "))
	}
	return reasons, detail
}

// Approve applies a held update (or drops a removed definition).
func (s *Store) Approve(id string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	i := slices.IndexFunc(s.state.Holds, func(h Hold) bool { return h.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNoHold, id)
	}
	h := s.state.Holds[i]
	applied := map[string]*storeEntry{}
	for k, e := range s.defs {
		applied[k] = e
	}
	held := map[string][]byte{}
	for _, other := range s.state.Holds {
		if other.ID == id || other.Removed {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, heldDir, other.ID+".yml"))
		if err != nil {
			return fmt.Errorf("cardigann: read held %s: %w", other.ID, err)
		}
		held[other.ID] = data
	}
	if h.Removed {
		delete(applied, id)
	} else {
		data, err := os.ReadFile(filepath.Join(s.Dir, heldDir, id+".yml"))
		if err != nil {
			return fmt.Errorf("cardigann: read held %s: %w", id, err)
		}
		e := newEntry(data)
		if e.hash != h.Hash {
			return fmt.Errorf("cardigann: the held update of %q changed on disk", id)
		}
		applied[id] = e
	}
	if err := s.writeSnapshotLocked(applied, held); err != nil {
		return err
	}
	s.defs = applied
	s.state.Holds = slices.Delete(slices.Clone(s.state.Holds), i, i+1)
	s.log().Info("cardigann: held definition update approved", "id", id, "removed", h.Removed)
	return s.writeStateLocked()
}

// writeSnapshotLocked replaces the applied and held folders: written beside them, then swapped in. Nothing else in
// Dir (custom/, state.json) is touched.
func (s *Store) writeSnapshotLocked(applied map[string]*storeEntry, held map[string][]byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("cardigann: create %s: %w", s.Dir, err)
	}
	write := func(name string, files map[string][]byte) error {
		tmp := filepath.Join(s.Dir, name+".new")
		if err := os.RemoveAll(tmp); err != nil {
			return err
		}
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return err
		}
		for id, data := range files {
			if err := os.WriteFile(filepath.Join(tmp, id+".yml"), data, 0o600); err != nil {
				return err
			}
		}
		final := filepath.Join(s.Dir, name)
		old := filepath.Join(s.Dir, name+".old")
		if err := os.RemoveAll(old); err != nil {
			return err
		}
		if err := os.Rename(final, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(tmp, final); err != nil {
			return err
		}
		return os.RemoveAll(old)
	}
	files := map[string][]byte{}
	for id, e := range applied {
		files[id] = e.data
	}
	if err := write(currentDir, files); err != nil {
		return fmt.Errorf("cardigann: write definitions: %w", err)
	}
	if err := write(heldDir, held); err != nil {
		return fmt.Errorf("cardigann: write held definitions: %w", err)
	}
	return nil
}

func (s *Store) writeStateLocked() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("cardigann: create %s: %w", s.Dir, err)
	}
	raw, err := json.MarshalIndent(s.state, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir, stateFile+".new")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("cardigann: write %s: %w", stateFile, err)
	}
	return os.Rename(tmp, filepath.Join(s.Dir, stateFile))
}

// Run refreshes when due (every; a failed refresh tries again after retry) until ctx ends.
func (s *Store) Run(ctx context.Context, every, retry time.Duration) {
	for {
		wait := every
		if s.Due(every) {
			if err := s.Refresh(ctx); err != nil && retry < every {
				wait = retry
			}
		} else if last := s.Status().LastCheck; !last.IsZero() {
			wait = max(every-s.clock().Sub(last), time.Minute)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func sortedIDs(m map[string]*storeEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
