package settings

// Linking formats to TRaSH ids. The arr import keeps the arrs' formats by name, without TRaSH
// ids (Notifiarr and Recyclarr synced them into the arrs; the arrs do not store the id), so a tracked source had
// almost nothing to compare. Linking is an explicit, previewed and audited administrator action: it matches each
// unlinked format of the source's flavour against the TRaSH tree by specification, uses the name only to choose
// between TRaSH formats of the same specification or to report a near-match, and stores the chosen id on the format
// itself (FormatRef.TrashID, inside a set too). The display name is never changed. Nothing links at startup or in
// a sync run.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// Link states of a format.
const (
	LinkLinked    = "linked"    // already carries a TRaSH id
	LinkMatched   = "matched"   // TRaSH has its specification under its name (or its name without " - Notifiarr")
	LinkDiverged  = "diverged"  // a TRaSH format of its name has another specification, or its specification has another name
	LinkAmbiguous = "ambiguous" // several TRaSH formats have its specification and the name picks none of them
	LinkUnmatched = "unmatched" // nothing in TRaSH resembles it: one of our own
)

// TrashCandidate is a TRaSH format a local one may stand for.
type TrashCandidate struct {
	TrashID string `json:"trash_id"`
	Name    string `json:"name"`
	// SameSpec: the specifications match the local one's (implementation, negate, required and field values).
	SameSpec bool `json:"same_spec"`
	// SameName: the TRaSH name is the local name, or the local name without Notifiarr's suffix.
	SameName bool `json:"same_name"`
}

// TrashLinkItem is one format of the source's flavour and how it relates to TRaSH.
type TrashLinkItem struct {
	// Set is the format set holding the format inline; empty for a standalone format.
	Set        string           `json:"set,omitempty"`
	Key        string           `json:"key"`
	Status     string           `json:"status"`
	TrashID    string           `json:"trash_id,omitempty"` // the current link, or the proposed one of a match
	Candidates []TrashCandidate `json:"candidates,omitempty"`
	Reason     string           `json:"reason,omitempty"`
}

// TrashLinkPreview lists every format of a source's flavour with its link state, at one settings generation and
// TRaSH commit.
type TrashLinkPreview struct {
	Generation int64           `json:"generation"`
	Source     string          `json:"source"`
	Flavour    string          `json:"flavour"`
	Commit     string          `json:"commit"`
	Items      []TrashLinkItem `json:"items"`
	Counts     map[string]int  `json:"counts"`
}

// TrashLink asks to link one format (Set empty: a standalone one) to a TRaSH id.
type TrashLink struct {
	Set     string `json:"set,omitempty"`
	Key     string `json:"key"`
	TrashID string `json:"trash_id"`
}

// ErrLinkRejected is an apply asking for a link its preview did not offer.
var ErrLinkRejected = errors.New("settings: link not offered by the preview")

// linkTreeTTL keeps a preview's TRaSH tree for its apply, so a tracked source is not fetched twice in a row.
const linkTreeTTL = 30 * time.Minute

type linkTree struct {
	source, flavour, commit string
	trash                   *format.Trash
	at                      time.Time
}

// trashMatcher indexes a TRaSH flavour by canonical specification and by name.
type trashMatcher struct {
	bySpec map[string][]*format.Format
	byName map[string][]*format.Format
	canon  map[string]string // trash_id -> canonical specification
}

func newTrashMatcher(t *format.Trash) *trashMatcher {
	m := &trashMatcher{bySpec: map[string][]*format.Format{}, byName: map[string][]*format.Format{}, canon: map[string]string{}}
	for _, f := range t.Formats {
		m.byName[f.Name] = append(m.byName[f.Name], f)
		if c, ok := canonicalFormat(t.Raw(f.TrashID)); ok {
			m.bySpec[c] = append(m.bySpec[c], f)
			m.canon[f.TrashID] = c
		}
	}
	return m
}

func (m *trashMatcher) classify(key, linked string, raw json.RawMessage) TrashLinkItem {
	item := TrashLinkItem{Key: key}
	if linked != "" {
		item.Status, item.TrashID = LinkLinked, linked
		return item
	}
	base := trashBaseName(key)
	sameName := func(f *format.Format) bool { return f.Name == key || f.Name == base }
	canon, ok := canonicalFormat(raw)
	seen := map[string]bool{}
	add := func(f *format.Format, sameSpec bool) {
		if !seen[f.TrashID] {
			seen[f.TrashID] = true
			item.Candidates = append(item.Candidates, TrashCandidate{TrashID: f.TrashID, Name: f.Name, SameSpec: sameSpec, SameName: sameName(f)})
		}
	}
	var picked []*format.Format
	if ok {
		for _, f := range m.bySpec[canon] {
			add(f, true)
			if sameName(f) {
				picked = append(picked, f)
			}
		}
	}
	for _, name := range []string{key, base} {
		for _, f := range m.byName[name] {
			add(f, ok && m.canon[f.TrashID] == canon)
		}
	}
	sort.Slice(item.Candidates, func(i, j int) bool {
		a, b := item.Candidates[i], item.Candidates[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.TrashID < b.TrashID
	})
	same := 0
	for _, c := range item.Candidates {
		if c.SameSpec {
			same++
		}
	}
	switch {
	case len(picked) == 1:
		item.Status, item.TrashID = LinkMatched, picked[0].TrashID
		if same > 1 {
			item.Reason = fmt.Sprintf("%d TRaSH formats have this specification; the name picks %s", same, picked[0].Name)
		}
	case len(picked) > 1:
		item.Status, item.Reason = LinkAmbiguous, "several TRaSH formats have this specification and name"
	case same > 1:
		item.Status, item.Reason = LinkAmbiguous, fmt.Sprintf("%d TRaSH formats have this specification; the name picks none", same)
	case same == 1:
		item.Status, item.Reason = LinkDiverged, "TRaSH has this specification under another name"
	case len(item.Candidates) > 0:
		item.Status, item.Reason = LinkDiverged, "the TRaSH format of this name has another specification"
	default:
		item.Status = LinkUnmatched
		if !ok {
			item.Reason = "specification unreadable"
		}
	}
	return item
}

// matchTrashLinks classifies every format of flavour in records: standalone formats of that flavour (a format
// without a flavour may serve both and is left alone) and the inline formats of the flavour's sets. A set entry
// referencing a standalone format is that format.
func matchTrashLinks(records Records, t *format.Trash, flavour string) []TrashLinkItem {
	m := newTrashMatcher(t)
	out := []TrashLinkItem{}
	formats := append([]CustomFormat(nil), records.Formats...)
	sort.Slice(formats, func(i, j int) bool { return formats[i].Key < formats[j].Key })
	for _, f := range formats {
		if f.Config.Flavour == flavour {
			out = append(out, m.classify(f.Key, customTrashID(f), f.Specification))
		}
	}
	sets := append([]FormatSet(nil), records.FormatSets...)
	sort.Slice(sets, func(i, j int) bool { return sets[i].Key < sets[j].Key })
	for _, set := range sets {
		if set.Config.Flavour != flavour {
			continue
		}
		for _, key := range sortedStringKeys(set.Config.Formats) {
			ref := set.Config.Formats[key]
			if ref == nil || ref.Ref != "" || (ref.Flavour != "" && ref.Flavour != flavour) {
				continue
			}
			item := m.classify(key, customTrashID(CustomFormat{Config: *ref, Specification: set.Specifications[key]}), set.Specifications[key])
			item.Set = set.Key
			out = append(out, item)
		}
	}
	return out
}

func linkCounts(items []TrashLinkItem) map[string]int {
	counts := map[string]int{LinkLinked: 0, LinkMatched: 0, LinkDiverged: 0, LinkAmbiguous: 0, LinkUnmatched: 0}
	for _, item := range items {
		counts[item.Status]++
	}
	return counts
}

// canonicalFormat is a format's matching behaviour, independent of its display, ids and JSON shape: per
// specification its implementation, negate, required and field values (the arr's field list and TRaSH's field
// object read alike; a zero, false, empty or missing value is the same as the arr's default), in a fixed order.
// Condition names, the renaming flag and TRaSH's own keys do not count.
func canonicalFormat(raw json.RawMessage) (string, bool) {
	var j format.FormatJSON
	if len(raw) == 0 || json.Unmarshal(raw, &j) != nil || len(j.Specifications) == 0 {
		return "", false
	}
	specs := make([]string, 0, len(j.Specifications))
	for _, s := range j.Specifications {
		fields, ok := canonicalFields(s.Fields)
		if !ok {
			return "", false
		}
		b, _ := json.Marshal([]any{s.Implementation, s.Negate, s.Required, fields})
		specs = append(specs, string(b))
	}
	sort.Strings(specs)
	return strings.Join(specs, "\n"), true
}

func canonicalFields(raw json.RawMessage) (map[string]string, bool) {
	values := map[string]json.RawMessage{}
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || string(raw) == "null":
	case raw[0] == '{':
		if json.Unmarshal(raw, &values) != nil {
			return nil, false
		}
	default:
		var list []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil, false
		}
		for _, f := range list {
			values[f.Name] = f.Value
		}
	}
	out := map[string]string{}
	for name, v := range values {
		var x any
		if len(v) == 0 {
			continue
		}
		if json.Unmarshal(v, &x) != nil {
			return nil, false
		}
		var s string
		switch y := x.(type) {
		case nil:
			continue
		case string:
			s = y
		case bool:
			s = strconv.FormatBool(y)
		case float64:
			s = strconv.FormatFloat(y, 'f', -1, 64)
		default:
			b, _ := json.Marshal(y)
			s = string(b)
		}
		if s == "" || s == "0" || s == "false" {
			continue
		}
		out[name] = s
	}
	return out, true
}

// applyTrashLinks links the requested formats in records. Each link must name an item of items that is not yet
// linked and one of its candidates with the same specification or the same name (TrashCandidate). A format whose
// specification matches its TRaSH one stores TRaSH's JSON from then on (its name inside the JSON included, which
// is how the sync recognises a later upstream rename; the display name is the format's key and stays), so the next
// sync compares like with like. A format linked by name alone keeps its specification until a sync brings TRaSH's.
// The arr's renaming flag is pinned on the link when TRaSH's differs, so naming never changes through a link or a
// later definition update.
func applyTrashLinks(records *Records, items []TrashLinkItem, t *format.Trash, flavour string, links []TrashLink) (int, error) {
	if len(links) == 0 {
		return 0, fmt.Errorf("%w: no format selected", ErrLinkRejected)
	}
	byKey := map[[2]string]TrashLinkItem{}
	for _, item := range items {
		byKey[[2]string{item.Set, item.Key}] = item
	}
	done := map[[2]string]bool{}
	for _, link := range links {
		k := [2]string{link.Set, link.Key}
		item, ok := byKey[k]
		if !ok || done[k] || item.Status == LinkLinked {
			return 0, fmt.Errorf("%w: %s/%s", ErrLinkRejected, link.Set, link.Key)
		}
		var cand *TrashCandidate
		for i := range item.Candidates {
			if c := item.Candidates[i]; c.TrashID == link.TrashID && (c.SameSpec || c.SameName) {
				cand = &item.Candidates[i]
			}
		}
		upstream, found := t.Format(link.TrashID)
		if cand == nil || !found {
			return 0, fmt.Errorf("%w: %s/%s as %s", ErrLinkRejected, link.Set, link.Key, link.TrashID)
		}
		done[k] = true
		ref, spec, setSpec := findLinkTarget(records, link)
		if ref == nil {
			return 0, fmt.Errorf("%w: %s/%s", ErrLinkRejected, link.Set, link.Key)
		}
		var local format.FormatJSON
		_ = json.Unmarshal(spec, &local)
		ref.TrashID, ref.Flavour = link.TrashID, flavour
		if ref.IncludeWhenRenaming == nil && local.IncludeCustomFormatWhenRenaming != upstream.IncludeWhenRenaming {
			keep := local.IncludeCustomFormatWhenRenaming
			ref.IncludeWhenRenaming = &keep
		}
		if cand.SameSpec {
			setSpec(append(json.RawMessage(nil), t.Raw(link.TrashID)...))
		}
	}
	return len(done), nil
}

// findLinkTarget returns the format a link names: its config, its stored specification and a setter for it.
func findLinkTarget(records *Records, link TrashLink) (*policy.FormatRef, json.RawMessage, func(json.RawMessage)) {
	if link.Set == "" {
		for i := range records.Formats {
			if f := &records.Formats[i]; f.Key == link.Key {
				return &f.Config, f.Specification, func(raw json.RawMessage) { f.Specification = raw }
			}
		}
		return nil, nil, nil
	}
	for i := range records.FormatSets {
		set := &records.FormatSets[i]
		if set.Key != link.Set {
			continue
		}
		ref := set.Config.Formats[link.Key]
		if ref == nil || ref.Ref != "" {
			return nil, nil, nil
		}
		return ref, set.Specifications[link.Key], func(raw json.RawMessage) { set.Specifications[link.Key] = raw }
	}
	return nil, nil, nil
}

// LinkPreview matches the formats of the source's flavour against the source's TRaSH tree (fetched as a sync run
// would) and changes nothing.
func (s *QualitySync) LinkPreview(ctx context.Context, source QualitySyncSource) (*TrashLinkPreview, error) {
	if err := source.Config.validate(); err != nil {
		return nil, err
	}
	resolved, err := s.resolve(ctx, source.Config)
	if err != nil {
		return nil, err
	}
	snap, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	t, commit, err := s.linkTrash(ctx, source.Key, resolved, "")
	if err != nil {
		return nil, err
	}
	records, err := qualityRecordsFromPolicy(snap.Policy)
	if err != nil {
		return nil, err
	}
	items := matchTrashLinks(records, t, resolved.Flavour)
	return &TrashLinkPreview{Generation: snap.Generation, Source: source.Key, Flavour: resolved.Flavour, Commit: commit, Items: items, Counts: linkCounts(items)}, nil
}

// LinkApply links the chosen formats, matched again at the previewed generation and TRaSH commit (a change of
// either is a conflict), in one audited settings generation (operation trash-link). Only specifications and links
// change; no score, profile or set membership does.
func (s *QualitySync) LinkApply(ctx context.Context, source QualitySyncSource, expected int64, commit string, links []TrashLink, actor string) (*Snapshot, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := source.Config.validate(); err != nil {
		return nil, 0, err
	}
	snap, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	if snap.Generation != expected || commit == "" {
		return nil, 0, ErrConflict
	}
	resolved := source.Config
	if resolved.Origin == OriginGitHub {
		if !commitRE.MatchString(commit) {
			return nil, 0, ErrConflict
		}
		// The previewed commit, not the branch's newest: what the administrator saw is what gets linked.
		resolved.ArchiveURL, resolved.Commit = TrackedArchiveURL(resolved, commit), commit
	}
	t, got, err := s.linkTrash(ctx, source.Key, resolved, commit)
	if err != nil {
		return nil, 0, err
	}
	if got != commit {
		return nil, 0, ErrConflict
	}
	records, err := qualityRecordsFromPolicy(snap.Policy)
	if err != nil {
		return nil, 0, err
	}
	n, err := applyTrashLinks(&records, matchTrashLinks(records, t, resolved.Flavour), t, resolved.Flavour, links)
	if err != nil {
		return nil, 0, err
	}
	all := snap.Records
	all.Profiles, all.Formats, all.FormatSets, all.Naming = records.Profiles, records.Formats, records.FormatSets, records.Naming
	saved, err := s.Coordinator.saveAndApplyWithHook(ctx, SaveRequest{ExpectedGeneration: expected, Actor: actor, Records: all, Base: s.Coordinator.base}, "trash-link", func(ctx context.Context, tx *sql.Tx) error {
		// The source's next run reads its upstream again, even at the same commit: the links change what it updates.
		_, err := tx.ExecContext(ctx, "UPDATE settings_quality_sync_run SET config_digest='' WHERE source_key=?", source.Key)
		return err
	})
	return saved, n, err
}

// linkTrash loads the source's TRaSH tree, reusing the one its last link preview loaded when that is the same
// source, flavour and commit (want; empty: any) and recent.
func (s *QualitySync) linkTrash(ctx context.Context, key string, c QualitySyncConfig, want string) (*format.Trash, string, error) {
	s.linkMu.Lock()
	cached := s.linkTree
	s.linkMu.Unlock()
	if want != "" && cached != nil && cached.source == key && cached.flavour == c.Flavour && cached.commit == want && s.Store.clock().Sub(cached.at) < linkTreeTTL {
		return cached.trash, cached.commit, nil
	}
	_, t, commit, cleanup, err := s.loadTree(ctx, c)
	if err != nil {
		return nil, "", err
	}
	cleanup()
	s.linkMu.Lock()
	s.linkTree = &linkTree{source: key, flavour: c.Flavour, commit: commit, trash: t, at: s.Store.clock()}
	s.linkMu.Unlock()
	return t, commit, nil
}
