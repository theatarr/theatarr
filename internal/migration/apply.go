package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

// Settings is what apply writes through: the settings coordinator (settings.RuntimeCoordinator), whose saves run the
// whole-policy validation and the write-enabling confirmations.
type Settings interface {
	Snapshot(ctx context.Context) (*settings.Snapshot, error)
	SaveAndApply(ctx context.Context, req settings.SaveRequest) (*settings.Snapshot, error)
}

// Checkpointer takes a rollback checkpoint: a backup set (theatarr backup now) and a copy of the settings.
type Checkpointer func(ctx context.Context) (Checkpoint, error)

// ApplyOptions choose what apply runs.
type ApplyOptions struct {
	// Steps limits apply to these step ids; empty runs every automatic step that is not done.
	Steps []string
	// Confirm names steps whose stored confirmation token apply may send: the human reviewed their effects.
	Confirm []string
	// ConfirmationTokens is the explicit token per step for callers that return the reviewed proof (MCP).
	// It cannot be combined with Confirm, which is the CLI/UI's step-name confirmation flow.
	ConfirmationTokens map[string]string
	// Keys resolves the credentials steps name (Payload.Secret). A key not given leaves the record without one.
	Keys  Keys
	Actor string
	Now   func() time.Time
}

// applyMu serializes applies in this process: two applies of one plan must not interleave.
var applyMu sync.Mutex

// Apply runs the plan's automatic steps through the settings coordinator, in order, saving each step's outcome in
// the plan. Before the first change it takes a checkpoint and records it. A step that is done is not run again; a
// step whose record already exists unchanged is marked done without a write; one whose record differs is a conflict
// and left alone. Old services are never contacted.
func Apply(ctx context.Context, st *store.Store, id int64, set Settings, checkpoint Checkpointer, o ApplyOptions) (*Plan, error) {
	applyMu.Lock()
	defer applyMu.Unlock()
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Actor == "" {
		o.Actor = "migrate"
	}
	if len(o.Confirm) > 0 && len(o.ConfirmationTokens) > 0 {
		return nil, errors.New("give confirm or confirmation_tokens, not both")
	}
	p, _, err := Load(ctx, st.Reader(), id)
	if err != nil {
		return nil, err
	}
	for sid, token := range o.ConfirmationTokens {
		s := p.Step(sid)
		if s == nil || s.Confirmation == nil || token == "" || token != s.Confirmation.Token ||
			!slices.Contains(o.Steps, sid) {
			return nil, errors.New("confirmation_tokens must match a selected step's current confirmation; review its effects again")
		}
	}
	for _, sid := range append(append([]string(nil), o.Steps...), o.Confirm...) {
		s := p.Step(sid)
		if s == nil {
			return nil, fmt.Errorf("plan %d has no step %q", id, sid)
		}
		if !s.Automatic {
			return nil, fmt.Errorf("step %s is manual: apply never runs it", sid)
		}
	}
	var run []int
	for i, s := range p.Steps {
		if s.Automatic && s.Status != StatusDone && (len(o.Steps) == 0 || slices.Contains(o.Steps, s.ID)) {
			run = append(run, i)
		}
	}
	if len(run) == 0 {
		return p, nil
	}
	if checkpoint == nil {
		return nil, errors.New("no checkpoint: apply needs a backup first")
	}
	cp, err := checkpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: %w (nothing was applied)", err)
	}
	p.Checkpoints = append(p.Checkpoints, cp)
	p.UpdatedAt = o.Now().UTC()
	if err := update(ctx, st, p); err != nil {
		return nil, err
	}
	for _, i := range run {
		s := &p.Steps[i]
		s.Error, s.Notes = "", withoutApplyNotes(s.Notes)
		switch {
		case s.Blocked != "":
			s.Status, s.Error = StatusBlocked, s.Blocked
		case waitsFor(p, s) != "":
			s.Status, s.Error = StatusBlocked, "waits for "+waitsFor(p, s)
		default:
			token := o.ConfirmationTokens[s.ID]
			if s.Confirmation != nil && slices.Contains(o.Confirm, s.ID) {
				token = s.Confirmation.Token
			}
			applyStep(ctx, st, set, s, o, token)
		}
		p.UpdatedAt = o.Now().UTC()
		if err := update(ctx, st, p); err != nil {
			return p, err
		}
	}
	return p, nil
}

const applyNote = "apply: "

func withoutApplyNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		if !strings.HasPrefix(n, applyNote) {
			out = append(out, n)
		}
	}
	return out
}

// waitsFor names the first dependency that is not done.
func waitsFor(p *Plan, s *Step) string {
	for _, d := range s.DependsOn {
		if dep := p.Step(d); dep != nil && dep.Automatic && dep.Status != StatusDone {
			return d
		}
	}
	return ""
}

func applyStep(ctx context.Context, st *store.Store, set Settings, s *Step, o ApplyOptions, token string) {
	if s.Kind == KindQuality || s.Kind == KindVersion {
		// Ownership is recorded by bootstrap, even when the install has no profiles yet.
		var initialized bool
		if err := st.Reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM settings_quality_state WHERE id=1)").Scan(&initialized); err != nil {
			s.Status, s.Error = StatusFailed, "read quality initialization: "+err.Error()
			return
		}
		if !initialized {
			s.Status, s.Error = StatusFailed, "Theatarr's settings do not manage quality profiles yet (start theatarr serve once)"
			return
		}
	}
	head, err := set.Snapshot(ctx)
	if err != nil {
		s.Status, s.Error = StatusFailed, "read settings: "+safeError(err, o.Keys, s)
		return
	}
	records := cloneRecords(head.Records)
	changed, err := mutate(&records, s)
	if err != nil {
		s.Status, s.Error = StatusConflict, err.Error()
		var refused *refusal
		if errors.As(err, &refused) {
			s.Status = StatusFailed
		}
		return
	}
	now := o.Now().UTC()
	if !changed {
		s.Status, s.Generation, s.AppliedAt, s.Confirmation = StatusDone, head.Generation, &now, nil
		s.Notes = append(s.Notes, applyNote+"already in the settings, nothing written")
		return
	}
	req := settings.SaveRequest{ExpectedGeneration: head.Generation, Actor: o.Actor, Records: records, ConfirmationToken: token}
	if name := s.Payload.Secret; name != "" {
		entity := map[string]string{KindIndexer: "indexers", KindProvider: "providers"}[s.Kind]
		if v, ok := lookupKey(o.Keys, name); ok && entity != "" {
			req.SecretWrites = map[string]settings.Secret{entity + "/" + s.Payload.Key: settings.NewSecret(v)}
		} else {
			s.Notes = append(s.Notes, applyNote+"no credential set: "+name+" was not given; enter it in Settings")
		}
	}
	saved, err := set.SaveAndApply(ctx, req)
	var ce *settings.ConfirmationError
	switch {
	case errors.As(err, &ce):
		s.Status = StatusConfirmation
		s.Confirmation = ce.Confirmation
		if s.Confirmation == nil {
			s.Confirmation = &settings.Confirmation{Effects: ce.Effects}
		}
		s.Error = "the change needs confirmation: review its effects and confirm step " + s.ID + " in the UI (" + uipages.Migration + ")"
		if ce.ConfirmInUI {
			s.Error = "the change needs confirmation this caller cannot give: apply step " + s.ID + " in the UI (" + uipages.Migration + ") or with a user's API key"
		}
	case err != nil:
		s.Status, s.Error = StatusFailed, safeError(err, o.Keys, s)
	default:
		s.Status, s.Generation, s.AppliedAt, s.Confirmation = StatusDone, saved.Generation, &now, nil
	}
}

func lookupKey(keys Keys, name string) (string, bool) {
	if keys == nil {
		return "", false
	}
	return keys(name)
}

// safeError is an error's text without the step's credential.
func safeError(err error, keys Keys, s *Step) string {
	msg := err.Error()
	if s.Payload != nil && s.Payload.Secret != "" {
		if v, ok := lookupKey(keys, s.Payload.Secret); ok && len(v) > 0 {
			msg = strings.ReplaceAll(msg, v, "***")
		}
	}
	return msg
}

// refusal is a step apply refuses to write whatever the settings say.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

func cloneRecords(r settings.Records) settings.Records {
	return settings.Records{
		Indexers: slices.Clone(r.Indexers), Providers: slices.Clone(r.Providers), Budgets: slices.Clone(r.Budgets),
		Versions: slices.Clone(r.Versions), Views: slices.Clone(r.Views), Profiles: slices.Clone(r.Profiles),
		Formats: slices.Clone(r.Formats), FormatSets: slices.Clone(r.FormatSets), Naming: slices.Clone(r.Naming),
		SyncSources: slices.Clone(r.SyncSources),
	}
}

// same reports whether two configs say the same thing in the policy files' terms.
func same(a, b any) bool {
	ja, err1 := configJSON(a)
	jb, err2 := configJSON(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

func exists(key string) error {
	return fmt.Errorf("%s already exists with other settings; left unchanged", key)
}

// mutate adds the step's records. changed is false when everything is already there as planned.
func mutate(r *settings.Records, s *Step) (bool, error) {
	p := s.Payload
	if p == nil || p.Key == "" {
		return false, &refusal{"step has no payload"}
	}
	switch s.Kind {
	case KindBudget:
		var cfg policy.Budget
		if err := decodeConfig(p.Config, &cfg); err != nil {
			return false, &refusal{"budget config: " + err.Error()}
		}
		for _, b := range r.Budgets {
			if b.Key == p.Key {
				if same(b.Config, cfg) {
					return false, nil
				}
				return false, exists("budget " + p.Key)
			}
		}
		r.Budgets = append(r.Budgets, settings.Budget{Record: settings.Record{Key: p.Key}, Config: cfg})
	case KindIndexer:
		var cfg policy.Indexer
		if err := decodeConfig(p.Config, &cfg); err != nil {
			return false, &refusal{"indexer config: " + err.Error()}
		}
		for _, ix := range r.Indexers {
			if ix.Key == p.Key {
				if same(ix.Config, cfg) {
					return false, nil
				}
				return false, exists("indexer " + p.Key)
			}
		}
		// Sources always land disabled: enabling one is the human's step.
		r.Indexers = append(r.Indexers, settings.Indexer{Record: settings.Record{Key: p.Key}, Enabled: false,
			Priority: p.Priority, Config: cfg})
	case KindProvider:
		var cfg policy.Provider
		if err := decodeConfig(p.Config, &cfg); err != nil {
			return false, &refusal{"provider config: " + err.Error()}
		}
		for _, pr := range r.Providers {
			if pr.Key == p.Key {
				if same(pr.Config, cfg) {
					return false, nil
				}
				return false, exists("provider " + p.Key)
			}
		}
		r.Providers = append(r.Providers, settings.Provider{Record: settings.Record{Key: p.Key}, Enabled: false, Config: cfg})
	case KindQuality, KindVersion:
		if s.Kind == KindQuality {
			return mutateQuality(r, p)
		}
		return mutateVersion(r, p)
	case KindView:
		return mutateView(r, p)
	default:
		return false, &refusal{"step kind " + s.Kind + " is not applied"}
	}
	return true, nil
}

func mutateQuality(r *settings.Records, p *Payload) (bool, error) {
	var prof policy.Profile
	if err := decodeConfig(p.Config, &prof); err != nil {
		return false, &refusal{"profile config: " + err.Error()}
	}
	var set policy.FormatSet
	if err := decodeConfig(p.FormatConfig, &set); err != nil {
		return false, &refusal{"format set config: " + err.Error()}
	}
	changed := false
	found := false
	for _, fs := range r.FormatSets {
		if fs.Key == p.FormatSet {
			found = true
			if !same(fs.Config, set) || !sameSpecs(fs.Specifications, p.Specifications) {
				return false, exists("format set " + p.FormatSet)
			}
		}
	}
	if !found {
		r.FormatSets = append(r.FormatSets, settings.FormatSet{Record: settings.Record{Key: p.FormatSet}, Config: set,
			Specifications: p.Specifications})
		changed = true
	}
	found = false
	for _, pr := range r.Profiles {
		if pr.Key == p.Key {
			found = true
			if !same(pr.Config, prof) {
				return false, exists("profile " + p.Key)
			}
		}
	}
	if !found {
		r.Profiles = append(r.Profiles, settings.QualityProfile{Record: settings.Record{Key: p.Key}, Config: prof, Origin: p.Origin})
		changed = true
	}
	return changed, nil
}

func sameSpecs(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		var x, y any
		if json.Unmarshal(v, &x) != nil || json.Unmarshal(b[k], &y) != nil {
			return false
		}
		jx, _ := json.Marshal(x)
		jy, _ := json.Marshal(y)
		if !bytes.Equal(jx, jy) {
			return false
		}
	}
	return true
}

func mutateVersion(r *settings.Records, p *Payload) (bool, error) {
	var v policy.VersionPolicy
	if err := decodeConfig(p.Config, &v); err != nil {
		return false, &refusal{"version config: " + err.Error()}
	}
	// A migrated version starts in shadow with grabbing off, whatever the stored plan says.
	if v.Mode != policy.ModeShadow || v.Grab || v.Follows != "" {
		return false, &refusal{"refused: a migrated version is created in shadow mode, with grab off and no follows"}
	}
	var naming policy.Naming
	if len(p.Naming) > 0 {
		if err := decodeConfig(p.Naming, &naming); err != nil {
			return false, &refusal{"naming config: " + err.Error()}
		}
	}
	for _, ex := range r.Versions {
		if ex.Key == p.Key {
			if same(ex.Config, v) {
				return false, nil
			}
			return false, exists("version " + p.Key)
		}
	}
	r.Versions = append(r.Versions, settings.Version{Record: settings.Record{Key: p.Key}, Config: v})
	for i, n := range r.Naming {
		if n.Key == p.Key {
			r.Naming[i].Config = naming
			return true, nil
		}
	}
	r.Naming = append(r.Naming, settings.VersionNaming{Record: settings.Record{Key: p.Key}, Config: naming})
	return true, nil
}

func mutateView(r *settings.Records, p *Payload) (bool, error) {
	for i := range r.Views {
		v := &r.Views[i]
		if v.Key != p.Key {
			continue
		}
		changed := false
		for _, k := range p.Versions {
			if !slices.Contains(v.Versions, k) {
				v.Versions = append(slices.Clone(v.Versions), k)
				changed = true
			}
		}
		profiles := map[string][]string{}
		for name, list := range v.Profiles {
			profiles[name] = slices.Clone(list)
		}
		names := make([]string, 0, len(p.Profiles))
		for name := range p.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			for _, k := range p.Profiles[name] {
				if !slices.Contains(profiles[name], k) {
					profiles[name] = append(profiles[name], k)
					changed = true
				}
			}
		}
		v.Profiles = profiles
		return changed, nil
	}
	return false, &refusal{"view " + p.Key + " is not in the settings (add it in " + uipages.Apps + ")"}
}
