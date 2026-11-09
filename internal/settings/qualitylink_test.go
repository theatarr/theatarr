package settings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// TRaSH ids of the fixture tree (testdata/trash/radarr) the link tests use.
const (
	link3D      = "b8cd450cbfa689c0259a01d9e29ba3d6"
	linkHDR     = "caa37d0df9c348912df1fb1d88f9273a"
	linkLQ      = "90a6f9a284dff5103f6346090e6280c8"
	linkBRDisk  = "ed38b889b31be83fda192888e2286d83"
	linkBRCopy  = "0123456789abcdef0123456789abcdef" // a second TRaSH format with BR-DISK's specification
	linkSetKey  = "radarr-arr"
	linkProfile = "anime-arr"
)

// arrShape turns a TRaSH format JSON into what an arr's customformat API returns (and what the arr import stores):
// an id, no trash fields, fields as a list of objects with labels, and the arr's own display name.
func arrShape(t *testing.T, root, id, name string, edit func(map[string]any)) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "radarr", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	delete(f, "trash_id")
	delete(f, "trash_scores")
	delete(f, "trash_regex")
	f["id"], f["name"] = 117, name
	for _, s := range f["specifications"].([]any) {
		spec := s.(map[string]any)
		list := []any{}
		for k, v := range spec["fields"].(map[string]any) {
			list = append(list, map[string]any{"order": 0, "name": k, "label": "Regular Expression", "value": v, "type": "textbox", "advanced": false, "privacy": "normal", "isFloat": false})
		}
		spec["fields"] = list
		spec["implementationName"] = "Release Title"
	}
	if edit != nil {
		edit(f)
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// linkService is syncService plus what the arr import leaves behind: a format set holding arr formats inline by
// name (some TRaSH under their own or a "- Notifiarr" name, one diverged, one of our own) and a profile scoring them.
func linkService(t *testing.T) (*QualitySync, *policy.Policy, *Snapshot) {
	t.Helper()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	root := svc.TrashRoot
	// A second TRaSH format with BR-DISK's specification, so a spec match alone has two candidates.
	editCopy := func() {
		b, err := os.ReadFile(filepath.Join(root, "radarr", linkBRDisk+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var f map[string]any
		_ = json.Unmarshal(b, &f)
		f["trash_id"], f["name"] = linkBRCopy, "BR-DISK (Alt)"
		b, _ = json.Marshal(f)
		if err := os.WriteFile(filepath.Join(root, "radarr", linkBRCopy+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	editCopy()
	specs := map[string]json.RawMessage{
		"3D":                       arrShape(t, root, link3D, "3D", nil),
		"HDR10+ Boost - Notifiarr": arrShape(t, root, linkHDR, "HDR10+ Boost - Notifiarr", func(f map[string]any) { f["includeCustomFormatWhenRenaming"] = true }),
		"LQ": arrShape(t, root, linkLQ, "LQ", func(f map[string]any) {
			specs := f["specifications"].([]any)
			f["specifications"] = specs[:len(specs)-1]
		}),
		"BR-DISK":       arrShape(t, root, linkBRDisk, "BR-DISK", nil),
		"Disc releases": arrShape(t, root, linkBRDisk, "Disc releases", nil),
		"Tier Fallback": json.RawMessage(`{"name":"Tier Fallback","includeCustomFormatWhenRenaming":false,"specifications":[{"name":"any","implementation":"ReleaseTitleSpecification","negate":false,"required":false,"fields":[{"name":"value","value":"fallback-tier"}]}]}`),
	}
	set := FormatSet{Record: Record{Key: linkSetKey}, Config: policy.FormatSet{Flavour: "radarr", Formats: map[string]*policy.FormatRef{}}, Specifications: specs}
	for key := range specs {
		set.Config.Formats[key] = &policy.FormatRef{Flavour: "radarr"}
	}
	req := saveRequest(base, initial)
	req.Records.FormatSets = append(req.Records.FormatSets, set)
	var profile QualityProfile
	for _, p := range req.Records.Profiles {
		if p.Key == "remux-2160p" {
			profile = p
		}
	}
	profile.Record = Record{Key: linkProfile}
	profile.Config.FormatSet = linkSetKey
	profile.Config.Formats = map[string]int{"3D": -10000, "HDR10+ Boost - Notifiarr": 100, "Tier Fallback": 5}
	req.Records.Profiles = append(req.Records.Profiles, profile)
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return svc, base, saved
}

func setFormat(t *testing.T, snap *Snapshot, key string) (*policy.FormatRef, json.RawMessage) {
	t.Helper()
	for _, set := range snap.Records.FormatSets {
		if set.Key == linkSetKey {
			return set.Config.Formats[key], set.Specifications[key]
		}
	}
	t.Fatal("no fixture set")
	return nil, nil
}

func specName(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var f struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Name
}

// A set format linked to TRaSH under its Notifiarr name follows a spec change in auto_definitions mode: applied by
// itself, its display name kept, and not taken for an upstream rename.
func TestQualitySyncUpdatesLinkedSetFormatSpec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, saved := linkService(t)
	req := saveRequest(base, saved)
	for i := range req.Records.FormatSets {
		if req.Records.FormatSets[i].Key == linkSetKey {
			req.Records.FormatSets[i].Config.Formats["HDR10+ Boost - Notifiarr"].TrashID = linkHDR
		}
	}
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "trash-radarr"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Mode: ModeAutoDefinitions, Enabled: true}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	editUpstream(t, svc.TrashRoot, linkHDR, func(f map[string]any) {
		f["specifications"].([]any)[0].(map[string]any)["fields"] = map[string]any{"value": `\bHDR10(\+|P(lus)?)\b`}
	})
	if err := svc.RunSource(ctx, saved.Records.SyncSources[0], true); err != nil {
		t.Fatal(err)
	}
	run := lastRunOf(t, svc)
	head, err := svc.Coordinator.Snapshot(ctx)
	if err != nil || run.Outcome != "applied" || head.Generation != saved.Generation+1 {
		t.Fatalf("linked set format spec change not applied: %+v", run)
	}
	ref, spec := setFormat(t, head, "HDR10+ Boost - Notifiarr")
	if ref.TrashID != linkHDR || specName(t, spec) != "HDR10+ Boost - Notifiarr" || !strings.Contains(string(spec), `HDR10(\\+|P(lus)?)`) {
		t.Fatalf("set format not updated in place: %s", spec)
	}
}

// A profile bound to a TRaSH score set never takes a score change by itself, not even in auto mode: the run is held
// as a pending review. Set formats count by their link.
func TestQualitySyncBoundScoreChangeHeldForReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, mode := range []string{"auto", ModeAutoDefinitions} {
		svc, base, saved := linkService(t)
		req := saveRequest(base, saved)
		for i := range req.Records.FormatSets {
			if req.Records.FormatSets[i].Key == linkSetKey {
				req.Records.FormatSets[i].Config.Formats["3D"].TrashID = link3D
			}
		}
		req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "trash-radarr"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Mode: mode, Enabled: true, Profiles: map[string]string{linkProfile: "anime-radarr"}}}}
		saved, err := svc.Coordinator.SaveAndApply(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		editUpstream(t, svc.TrashRoot, link3D, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": -10000, "anime-radarr": -20000} })
		if err := svc.RunSource(ctx, saved.Records.SyncSources[0], true); err != nil {
			t.Fatal(err)
		}
		run := lastRunOf(t, svc)
		pending, _ := svc.Reviews(ctx)
		// Only the held score waits; whatever else the run has applies (here the fixture's other formats).
		if run.Outcome != "review" || svc.Coordinator.Current().Policy.Profiles[linkProfile].Formats["3D"] != -10000 || len(pending) != 1 {
			t.Fatalf("%s: bound score change not held: %+v %+v", mode, run, pending)
		}
		// The first run after binding asks once about each score that differs from TRaSH's (take or keep).
		if h := pending[0].Held; pending[0].Kind != ReviewScores || len(h) != 1 || h[0].Profile != linkProfile || h[0].Format != "3D" || h[0].Trash != -20000 || h[0].Ours == nil || *h[0].Ours != -10000 || h[0].Last != nil {
			t.Fatalf("%s: wrong score review: %+v", mode, pending[0])
		}
	}
}

func linkItems(p *TrashLinkPreview) map[string]TrashLinkItem {
	out := map[string]TrashLinkItem{}
	for _, item := range p.Items {
		if item.Set == linkSetKey {
			out[item.Key] = item
		}
	}
	return out
}

// Matching by specification against a TRaSH fixture: exact, Notifiarr-suffixed, diverged, our own, and two TRaSH
// formats with one specification (the name picks one, or none). A preview changes nothing.
func TestTrashLinkPreviewClassifies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, saved := linkService(t)
	source := QualitySyncSource{Record: Record{Key: "trash-radarr"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr"}}
	p, err := svc.LinkPreview(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	items := linkItems(p)
	want := map[string][2]string{
		"3D":                       {LinkMatched, link3D},
		"HDR10+ Boost - Notifiarr": {LinkMatched, linkHDR},
		"BR-DISK":                  {LinkMatched, linkBRDisk},
		"LQ":                       {LinkDiverged, ""},
		"Disc releases":            {LinkAmbiguous, ""},
		"Tier Fallback":            {LinkUnmatched, ""},
	}
	for key, w := range want {
		if got := items[key]; got.Status != w[0] || got.TrashID != w[1] {
			t.Errorf("%s: got %s %s (%s), want %s %s", key, got.Status, got.TrashID, got.Reason, w[0], w[1])
		}
	}
	if c := items["LQ"].Candidates; len(c) != 1 || c[0].TrashID != linkLQ || c[0].SameSpec || !c[0].SameName {
		t.Errorf("diverged candidate: %+v", c)
	}
	if c := items["Disc releases"].Candidates; len(c) != 2 || !c[0].SameSpec || c[0].SameName || c[1].SameName {
		t.Errorf("ambiguous candidates: %+v", c)
	}
	if len(items["Tier Fallback"].Candidates) != 0 {
		t.Error("own format got a candidate")
	}
	if p.Generation != saved.Generation || svc.Coordinator.Current().Generation != saved.Generation || p.Counts[LinkMatched] < 3 {
		t.Fatalf("preview: generation %d counts %v", p.Generation, p.Counts)
	}
	for _, item := range p.Items {
		if item.Set == "" && item.Status != LinkLinked {
			t.Errorf("standalone TRaSH format not seen as linked: %+v", item)
		}
	}
}

// Linking stores the id on the set format, keeps its name and scores, pins its renaming flag, refuses what the
// preview did not offer, and from then on the tracked sync keeps the specification current by itself.
func TestTrashLinkApplyThenSyncUpdatesSetFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, saved := linkService(t)
	req := saveRequest(base, saved)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "trash-radarr"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Mode: ModeAutoDefinitions, Enabled: true}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	source := saved.Records.SyncSources[0]
	p, err := svc.LinkPreview(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]TrashLink{
		nil,
		{{Set: linkSetKey, Key: "Tier Fallback", TrashID: link3D}},
		{{Set: linkSetKey, Key: "3D", TrashID: linkHDR}},
		{{Set: linkSetKey, Key: "3D", TrashID: link3D}, {Set: linkSetKey, Key: "3D", TrashID: link3D}},
	} {
		if _, _, err := svc.LinkApply(ctx, source, p.Generation, p.Commit, bad, "admin"); !errors.Is(err, ErrLinkRejected) {
			t.Fatalf("link %v accepted: %v", bad, err)
		}
	}
	if _, _, err := svc.LinkApply(ctx, source, p.Generation, "content:other", []TrashLink{{Set: linkSetKey, Key: "3D", TrashID: link3D}}, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatal("link against another TRaSH commit accepted", err)
	}
	// The ambiguous one only as the administrator's explicit choice among the formats of its specification.
	links := []TrashLink{{Set: linkSetKey, Key: "3D", TrashID: link3D}, {Set: linkSetKey, Key: "HDR10+ Boost - Notifiarr", TrashID: linkHDR}, {Set: linkSetKey, Key: "LQ", TrashID: linkLQ}, {Set: linkSetKey, Key: "Disc releases", TrashID: linkBRCopy}}
	linked, n, err := svc.LinkApply(ctx, source, p.Generation, p.Commit, links, "admin")
	if err != nil || n != 4 || linked.Generation != saved.Generation+1 {
		t.Fatalf("link apply: n=%d err=%v", n, err)
	}
	found := false
	for _, a := range audit(t, svc.Store) {
		found = found || (a.Operation == "trash-link" && a.Actor == "admin" && a.Generation == linked.Generation)
	}
	if !found {
		t.Fatal("link not audited")
	}
	ref, spec := setFormat(t, linked, "HDR10+ Boost - Notifiarr")
	if ref.TrashID != linkHDR || ref.Flavour != "radarr" || ref.IncludeWhenRenaming == nil || !*ref.IncludeWhenRenaming {
		t.Fatalf("link stored wrong: %+v %s", ref, spec)
	}
	if !sameFormatSpec(spec, svcTrashRaw(t, svc, linkHDR)) {
		t.Fatal("matched format did not take TRaSH's definition")
	}
	// The display name is the key, in the decision engine and the arr views; the renaming flag stays the arr's.
	if _, ok := linked.Policy.FormatSets[linkSetKey].Formats["HDR10+ Boost - Notifiarr"]; !ok || linked.Policy.Profiles[linkProfile].Formats["HDR10+ Boost - Notifiarr"] != 100 {
		t.Fatal("display name changed")
	}
	if ref, _ := setFormat(t, linked, "Disc releases"); ref.TrashID != linkBRCopy {
		t.Fatal("explicit choice among same-spec formats not linked")
	}
	if _, lq := setFormat(t, linked, "LQ"); !sameFormatSpec(lq, mustSetSpec(t, saved, "LQ")) {
		t.Fatal("diverged format's specification replaced by the link")
	}
	if ref, _ := setFormat(t, linked, "Tier Fallback"); ref.TrashID != "" {
		t.Fatal("own format linked")
	}
	if !reflect.DeepEqual(linked.Policy.Profiles[linkProfile].Formats, saved.Policy.Profiles[linkProfile].Formats) {
		t.Fatal("link changed scores")
	}
	// An immediate sync: only the diverged LQ differs from TRaSH; applied by itself as a definition change.
	if err := svc.RunSource(ctx, source, true); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.Reviews(ctx)
	if run := lastRunOf(t, svc); run.Outcome != "applied" || len(pending) != 0 {
		t.Fatalf("first sync after linking: %+v", run)
	}
	head, _ := svc.Coordinator.Snapshot(ctx)
	if _, lq := setFormat(t, head, "LQ"); !sameFormatSpec(lq, svcTrashRaw(t, svc, linkLQ)) {
		t.Fatal("linked diverged format not brought to TRaSH")
	}
	editUpstream(t, svc.TrashRoot, link3D, func(f map[string]any) {
		f["specifications"].([]any)[0].(map[string]any)["fields"] = map[string]any{"value": `\b3D\b`}
	})
	svc.Store.SetClock(func() time.Time { return time.Now().Add(400 * 24 * time.Hour) })
	if err := svc.RunSource(ctx, source, true); err != nil {
		t.Fatal(err)
	}
	head, _ = svc.Coordinator.Snapshot(ctx)
	if _, d3 := setFormat(t, head, "3D"); lastRunOf(t, svc).Outcome != "applied" || !strings.Contains(string(d3), `\\b3D\\b`) {
		t.Fatalf("linked set format not updated: %s", d3)
	}
	if !reflect.DeepEqual(head.Policy.Profiles[linkProfile].Formats, saved.Policy.Profiles[linkProfile].Formats) {
		t.Fatal("definition sync changed scores")
	}
}

func svcTrashRaw(t *testing.T, svc *QualitySync, id string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(svc.TrashRoot, "radarr", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustSetSpec(t *testing.T, snap *Snapshot, key string) json.RawMessage {
	t.Helper()
	_, spec := setFormat(t, snap, key)
	return spec
}

// Nothing links by itself: not the settings bootstrap, not a new coordinator on the stored head, not a scheduled
// sync run (which only follows formats already linked).
func TestTrashLinkNoStartupOrSyncSideEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, saved := linkService(t)
	req := saveRequest(base, saved)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "trash-radarr"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Mode: "auto", Enabled: true}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Store.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimeCoordinator(svc.Store, base, head, nil, func(context.Context, *Snapshot, *Runtime) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Coordinator.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range after.Records.FormatSets {
		if set.Key != linkSetKey {
			continue
		}
		for key, ref := range set.Config.Formats {
			if ref.TrashID != "" {
				t.Errorf("%s linked without an administrator", key)
			}
			if !sameFormatSpec(set.Specifications[key], mustSetSpec(t, saved, key)) {
				t.Errorf("%s changed without a link", key)
			}
		}
	}
	for _, a := range audit(t, svc.Store) {
		if a.Operation == "trash-link" {
			t.Fatal("trash-link outside an administrator action")
		}
	}
}
