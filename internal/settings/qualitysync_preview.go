package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/trashbundle"
)

func (s *QualitySync) Preview(ctx context.Context, source QualitySyncSource) (*QualityPreview, error) {
	if err := source.Config.validate(); err != nil {
		return nil, err
	}
	var err error
	if source.Config, err = s.resolve(ctx, source.Config); err != nil {
		return nil, err
	}
	return s.previewResolved(withBundledTrash(ctx), source)
}

// bundledTrashKey marks a context of a sync step the user started (a preview, a run from the page): only these fall
// back to the bundled TRaSH snapshot without a checkout. A scheduled run without one fails, as before the bundle.
type bundledTrashKey struct{}

func withBundledTrash(ctx context.Context) context.Context {
	return context.WithValue(ctx, bundledTrashKey{}, true)
}

// previewResolved builds the preview of a validated, resolved source (a tracked one carries its pinned archive).
func (s *QualitySync) previewResolved(ctx context.Context, source QualitySyncSource) (*QualityPreview, error) {
	snap, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	root, t, commit, cleanup, err := s.loadTree(ctx, source.Config)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	records, err := qualityRecordsFromPolicy(snap.Policy)
	if err != nil {
		return nil, err
	}
	out := &QualityPreview{Generation: snap.Generation, Source: source.Key, Commit: commit, Added: []FormatChange{}, Changed: []FormatChange{}, Retired: []FormatChange{}, Scores: []ScoreChange{}, Held: []HeldScore{}, HeldFields: []HeldField{}, Fields: []ProfileFieldChange{}, Profiles: []string{}, Notes: []string{}, State: "pending"}
	saved, ok := savedSource(snap.Records, source.Key)
	out.draft = !ok || configDigest(saved.Config) != configDigest(unresolved(source.Config))
	existing := syncExistingFormats(&records, t, source.Config.Flavour, out)
	desired := map[string]CustomFormat{}
	for _, id := range source.Config.TrashIDs {
		if existing[id] {
			continue
		}
		f, ok := t.Format(id)
		if !ok {
			return nil, fmt.Errorf("settings: TRaSH format %s is missing", id)
		}
		desired[f.Name] = CustomFormat{Record: Record{Key: f.Name}, Config: policy.FormatRef{TrashID: id, Flavour: source.Config.Flavour}, Specification: append(json.RawMessage(nil), t.Raw(id)...)}
	}
	fieldState := map[string]map[string]FieldMemory{}
	var recyclarrProfiles []RecyclarrProfile
	if source.Config.Kind == "recyclarr" {
		if s.ConvertRecyclarr == nil {
			return nil, errors.New("settings: Recyclarr converter unavailable")
		}
		converted, err := s.ConvertRecyclarr([]byte(source.Config.Recyclarr), root, source.Config.Flavour, source.Config.sizesOn())
		if err != nil {
			return nil, err
		}
		out.Notes = append(out.Notes, converted.Notes...)
		retired := map[string]bool{}
		for _, change := range out.Retired {
			retired[change.TrashID] = true
		}
		for _, f := range converted.Formats {
			if !retired[customTrashID(f)] {
				desired[f.Key] = f
			}
		}
		recyclarrProfiles = converted.Profiles
	}
	for _, key := range sortedStringKeys(desired) {
		incoming := desired[key]
		found := false
		for i, rec := range records.Formats {
			if rec.Key != key {
				continue
			}
			found = true
			if customTrashID(rec) != customTrashID(incoming) {
				return nil, fmt.Errorf("settings: upstream name %q collides with an existing format", key)
			}
			incoming.Record = rec.Record
			incoming.Config.IncludeWhenRenaming = rec.Config.IncludeWhenRenaming
			if !sameFormatSpec(rec.Specification, incoming.Specification) {
				out.Changed = append(out.Changed, FormatChange{Key: key, TrashID: customTrashID(incoming)})
			}
			records.Formats[i] = incoming
		}
		if !found {
			records.Formats = append(records.Formats, incoming)
			out.Added = append(out.Added, FormatChange{Key: key, TrashID: customTrashID(incoming)})
		}
	}
	// A Recyclarr source touches only the managed fields of a profile the settings hold; a new profile is taken whole.
	// Formats are merged first, so the reset sees the retired ones.
	if len(recyclarrProfiles) > 0 {
		retired := map[string]bool{}
		for _, change := range out.Retired {
			retired[change.TrashID] = true
		}
		for _, p := range recyclarrProfiles {
			i := slices.IndexFunc(records.Profiles, func(r QualityProfile) bool { return r.Key == p.Key })
			if i < 0 {
				if len(p.Config.Qualities) == 0 {
					return nil, fmt.Errorf("settings: the Recyclarr config gives profile %q no qualities and the settings do not hold it", p.Key)
				}
				records.Profiles = append(records.Profiles, QualityProfile{Record: Record{Key: p.Key}, Config: p.Config})
				if !out.draft {
					fieldState[p.Key] = newProfileMemory(p)
				}
				continue
			}
			ids := scopedTrashIDs(records, records.Profiles[i].Config.FormatSet)
			keep := func(k string) bool { return retired[ids[k]] || retired[k] }
			next, held, err := reconcileProfile(&records.Profiles[i].Config, p, saved.Config.FieldState[p.Key], out.draft, keep)
			if err != nil {
				return nil, err
			}
			out.HeldFields = append(out.HeldFields, held...)
			if len(next) > 0 {
				fieldState[p.Key] = next
			}
		}
	}
	// Explicit score-set bindings update only scores, preserving qualities, cutoffs and safety switches. A saved
	// source's bound score follows TRaSH while it still equals TRaSH's last score; ours is held when TRaSH changes it.
	retired := map[string]bool{}
	for _, change := range out.Retired {
		retired[change.TrashID] = true
	}
	state := map[string]ProfileScoreState{}
	for i, rec := range records.Profiles {
		scoreSet, managed := source.Config.Profiles[rec.Key]
		if !managed {
			continue
		}
		scores := map[string]int{}
		for key, score := range rec.Config.Formats {
			scores[key] = score
		}
		last := map[string]TrashScoreState{}
		if prev, ok := saved.Config.ScoreState[rec.Key]; ok && prev.Set == scoreSet {
			last = prev.Scores
		}
		next := map[string]TrashScoreState{}
		for key, id := range scopedTrashIDs(records, rec.Config.FormatSet) {
			if retired[id] {
				// A retired format keeps its score and what we remember of it.
				for _, k := range []string{key, id} {
					if st, ok := last[k]; ok {
						next[k] = st
					}
				}
				continue
			}
			f, ok := t.Format(id)
			if !ok {
				continue
			}
			_, byID := scores[id]
			_, byKey := scores[key]
			if !byID && !byKey && !slices.Contains(source.Config.TrashIDs, id) {
				continue
			}
			score, ok := format.TrashScore(f, scoreSet)
			if !ok {
				continue
			}
			scoreKey := key
			if byID {
				scoreKey = f.TrashID
			}
			if out.draft {
				scores[scoreKey] = score
				continue
			}
			if st, held := followScore(scores, scoreKey, score, last); held {
				h := HeldScore{Profile: rec.Key, Format: scoreKey, Trash: score}
				if ours, ok := scores[scoreKey]; ok {
					h.Ours = &ours
				}
				if st, ok := last[scoreKey]; ok {
					h.Last = &st.Trash
				}
				out.Held = append(out.Held, h)
				if st, ok := last[scoreKey]; ok {
					next[scoreKey] = st
				}
			} else {
				next[scoreKey] = st
			}
		}
		records.Profiles[i].Config.Formats = scores
		if len(next) > 0 {
			state[rec.Key] = ProfileScoreState{Set: scoreSet, Scores: next}
		}
	}
	sort.Slice(out.HeldFields, func(i, j int) bool {
		a, b := out.HeldFields[i], out.HeldFields[j]
		return a.Profile < b.Profile || (a.Profile == b.Profile && a.Field < b.Field)
	})
	sort.Slice(out.Held, func(i, j int) bool {
		a, b := out.Held[i], out.Held[j]
		return a.Profile < b.Profile || (a.Profile == b.Profile && a.Format < b.Format)
	})
	if !out.draft && (!reflect.DeepEqual(normalScoreState(saved.Config.ScoreState), normalScoreState(state)) || !sameFieldState(saved.Config.FieldState, fieldState)) {
		out.scoreState = true
		updated := saved
		updated.Config.ScoreState, updated.Config.FieldState = state, fieldState
		records.SyncSources = []QualitySyncSource{updated}
	}
	// The sync replaces only the quality sections; every other section is carried from the snapshot.
	all := snap.Records
	all.Profiles, all.Formats, all.FormatSets, all.Naming = records.Profiles, records.Formats, records.FormatSets, records.Naming
	next, err := candidate(snap.Policy, all)
	if err != nil {
		return nil, err
	}
	for key, pr := range next.Profiles {
		before := snap.Policy.Profiles[key]
		if before == nil {
			out.Profiles = append(out.Profiles, key)
			continue
		}
		a, b := *before, *pr
		a.Formats, b.Formats = nil, nil
		if !reflect.DeepEqual(a, b) {
			out.Profiles = append(out.Profiles, key)
			out.Fields = append(out.Fields, fieldChanges(key, before, pr)...)
		}
		keys := map[string]bool{}
		for k := range before.Formats {
			keys[k] = true
		}
		for k := range pr.Formats {
			keys[k] = true
		}
		for _, k := range sortedStringKeys(keys) {
			oldScore, oldOK := before.Formats[k]
			newScore, newOK := pr.Formats[k]
			if oldScore != newScore || oldOK != newOK {
				out.Scores = append(out.Scores, ScoreChange{Profile: key, Format: k, Before: before.Formats[k], After: pr.Formats[k]})
			}
		}
	}
	sort.Strings(out.Profiles)
	sort.SliceStable(out.Fields, func(i, j int) bool { return out.Fields[i].Profile < out.Fields[j].Profile })
	if s.Impact == nil {
		return nil, errors.New("settings: decision impact service unavailable")
	}
	out.Impact, err = s.Impact(ctx, snap.Policy, next)
	if err != nil {
		return nil, err
	}
	out.candidate = records
	return out, nil
}

// followScore decides one bound score whose TRaSH score is now trash. It follows TRaSH (scores updated) while ours
// still equals TRaSH's last score, and is left alone while TRaSH has not changed it; ours (differing or kept) is held
// when TRaSH changes it, and so is any differing score on the first run after binding (no last score). It returns
// the state to remember when nothing is held.
func followScore(scores map[string]int, key string, trash int, last map[string]TrashScoreState) (TrashScoreState, bool) {
	ours, scored := scores[key]
	prev, known := last[key]
	switch {
	case scored && ours == trash:
		return TrashScoreState{Trash: trash, Pinned: known && prev.Pinned}, false
	case known && prev.Trash == trash:
		return prev, false
	case known && !prev.Pinned && ours == prev.Trash:
		scores[key] = trash
		return TrashScoreState{Trash: trash}, false
	case !known && !scored && trash == 0:
		// A format the profile does not score yet, which TRaSH scores 0: nothing to ask.
		return TrashScoreState{Trash: trash}, false
	}
	return TrashScoreState{}, true
}

// normalScoreState drops empty entries, so a state with nothing to remember equals none.
func normalScoreState(s map[string]ProfileScoreState) map[string]ProfileScoreState {
	out := map[string]ProfileScoreState{}
	for k, v := range s {
		if len(v.Scores) > 0 {
			out[k] = v
		}
	}
	return out
}

func savedSource(records Records, key string) (QualitySyncSource, bool) {
	for _, s := range records.SyncSources {
		if s.Key == key {
			return s, true
		}
	}
	return QualitySyncSource{}, false
}

// unresolved is a resolved tracked config as saved: without the archive pinned to the branch's newest commit.
func unresolved(c QualitySyncConfig) QualitySyncConfig {
	if c.Origin == OriginGitHub {
		c.ArchiveURL, c.Commit = "", ""
	}
	return c
}

// loadTree is trashTree with the source flavour's TRaSH data loaded.
func (s *QualitySync) loadTree(ctx context.Context, c QualitySyncConfig) (string, *format.Trash, string, func(), error) {
	root, commit, cleanup, err := s.trashTree(ctx, c)
	if err != nil {
		return "", nil, "", cleanup, err
	}
	t, err := format.LoadTrash(root, format.Flavour(c.Flavour))
	if err != nil {
		cleanup()
		return "", nil, "", func() {}, err
	}
	return root, t, commit, cleanup, nil
}

// trashTree is the TRaSH tree of a validated, resolved source: its pinned archive unpacked into a temporary
// directory (removed by cleanup), the local tree, or without one, for a step the user started, the bundled snapshot.
// commit attributes it.
func (s *QualitySync) trashTree(ctx context.Context, c QualitySyncConfig) (root, commit string, cleanup func(), err error) {
	cleanup = func() {}
	if c.ArchiveURL == "" && s.TrashRoot == "" && ctx.Value(bundledTrashKey{}) != nil {
		// No local tree (THEATARR_TRASH unset): the snapshot bundled with the binary, copied out for the readers that
		// take a directory (the Recyclarr converter, the size tables).
		return bundledTrashTree()
	}
	if c.ArchiveURL == "" {
		commit, err = trashCommit(s.TrashRoot)
		return s.TrashRoot, commit, cleanup, err
	}
	if s.FetchArchive == nil {
		return "", "", cleanup, errors.New("settings: archive gateway unavailable")
	}
	data, err := s.FetchArchive(ctx, c)
	if err != nil {
		return "", "", cleanup, err
	}
	dir, err := unpackQualityArchive(data)
	if err != nil {
		return "", "", cleanup, err
	}
	return dir, c.Commit, func() { os.RemoveAll(dir) }, nil
}

// bundledTrashTree is the bundled TRaSH snapshot (internal/trashbundle) in a temporary directory, removed by cleanup.
func bundledTrashTree() (root, commit string, cleanup func(), err error) {
	cleanup = func() {}
	fsys, err := trashbundle.FS()
	if err != nil {
		return "", "", cleanup, err
	}
	dir, err := os.MkdirTemp("", "theatarr-trash-bundle-")
	if err != nil {
		return "", "", cleanup, err
	}
	if err := os.CopyFS(dir, fsys); err != nil {
		os.RemoveAll(dir)
		return "", "", cleanup, err
	}
	return dir, trashbundle.Commit(), func() { os.RemoveAll(dir) }, nil
}

func customTrashID(f CustomFormat) string {
	if f.Config.TrashID != "" {
		return f.Config.TrashID
	}
	var j format.FormatJSON
	_ = json.Unmarshal(f.Specification, &j)
	return j.TrashID
}
func sameFormatSpec(a, b json.RawMessage) bool {
	var x, y format.FormatJSON
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	x.ID, y.ID = 0, 0
	x.TrashScores, y.TrashScores = nil, nil
	// SQLite's JSON snapshots compact RawMessage fields. Whitespace and object order are not spec changes.
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	var left, right any
	_ = json.Unmarshal(ax, &left)
	_ = json.Unmarshal(by, &right)
	return reflect.DeepEqual(left, right)
}
func sortedStringKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func trashCommit(root string) (string, error) {
	if commit, found, err := localTrashCommit(root); found || err != nil {
		return commit, err
	}
	// Exported trees have no .git; attribute their content rather than inventing a commit.
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			h.Write([]byte(rel))
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "content:" + hex.EncodeToString(h.Sum(nil)), nil
}
