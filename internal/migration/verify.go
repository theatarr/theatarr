package migration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

// Verifier runs the read-only checks verify reports. Any func may be nil: its check then reads "not checked".
type Verifier struct {
	// Policy is the policy Theatarr runs with now.
	Policy func(ctx context.Context) (*policy.Policy, error)
	// Golive is theatarr golive check for one version.
	Golive func(ctx context.Context, version string) (*golive.Report, error)
	// Adopt is theatarr library adopt -dry-run for a parallel version.
	Adopt func(ctx context.Context, version string) (*library.AdoptReport, error)
	// Refresh is theatarr library adopt -refresh -dry-run for a parallel version.
	Refresh func(ctx context.Context, version string) (*library.RefreshReport, error)
}

// VerifyReport says per version whether it is ready to hand over, and how.
type VerifyReport struct {
	SchemaVersion int                `json:"schema_version"`
	PlanID        int64              `json:"plan_id"`
	At            time.Time          `json:"at"`
	Versions      []VersionReadiness `json:"versions"`
	// Pending are the plan's automatic steps that are not done.
	Pending []string `json:"pending"`
	// Rollback is the last checkpoint apply took, if any.
	Rollback *Checkpoint `json:"rollback,omitempty"`
}

// VersionReadiness is one version's verdict.
type VersionReadiness struct {
	Key      string `json:"key"`
	Instance string `json:"instance"`
	// Ready means no check failed: the version may be handed over by the cutover steps.
	Ready    bool           `json:"ready"`
	Checks   []Check        `json:"checks"`
	Golive   *golive.Report `json:"golive,omitempty"`
	Cutover  []string       `json:"cutover"`
	Rollback []string       `json:"rollback"`
}

// Check is one line of a verdict, in golive's statuses.
type Check struct {
	Name    string        `json:"name"`
	Status  golive.Status `json:"status"`
	Message string        `json:"message"`
	Fix     string        `json:"fix,omitempty"`
}

// Verify checks every version of the plan. It writes nothing.
func Verify(ctx context.Context, st *store.Store, id int64, v Verifier, now time.Time) (*VerifyReport, error) {
	p, inv, err := Load(ctx, st.Reader(), id)
	if err != nil {
		return nil, err
	}
	var pol *policy.Policy
	var polErr error
	if v.Policy != nil {
		pol, polErr = v.Policy(ctx)
	}
	rep := &VerifyReport{SchemaVersion: SchemaVersion, PlanID: id, At: now.UTC(), Versions: []VersionReadiness{},
		Pending: []string{}}
	for _, s := range p.Steps {
		if s.Automatic && s.Status != StatusDone {
			rep.Pending = append(rep.Pending, s.ID)
		}
	}
	if n := len(p.Checkpoints); n > 0 {
		rep.Rollback = &p.Checkpoints[n-1]
	}
	for _, vp := range p.Versions() {
		vr := VersionReadiness{Key: vp.Key, Instance: vp.Instance, Checks: []Check{}}
		add := func(name string, st golive.Status, fix, format string, args ...any) {
			vr.Checks = append(vr.Checks, Check{Name: name, Status: st, Message: fmt.Sprintf(format, args...), Fix: fix})
		}
		var waiting []string
		for _, s := range p.Steps {
			if s.Automatic && s.Status != StatusDone && stepFor(s, vp) {
				waiting = append(waiting, s.ID+" ("+s.Status+")")
			}
		}
		if len(waiting) > 0 {
			add("plan", golive.Fail, "run theatarr migrate apply, or fix what blocks the steps and plan again",
				"steps not done: %s", strings.Join(waiting, ", "))
		} else {
			add("plan", golive.Pass, "", "every automatic step for the version is done")
		}
		switch {
		case polErr != nil:
			add("policy", golive.Fail, "", "cannot read Theatarr's policy: %v", polErr)
		case pol == nil:
			add("policy", golive.Warn, "", "not checked")
		default:
			verifyPolicy(pol, p, inv, vp, add)
		}
		if v.Golive != nil && pol != nil && pol.Versions[vp.Key] != nil {
			r, err := v.Golive(ctx, vp.Key)
			if err != nil {
				add("golive", golive.Fail, "", "golive check: %v", err)
			} else {
				vr.Golive = r
				for _, sec := range []string{golive.SecPolicy, golive.SecSecrets, golive.SecHealth, golive.SecShadow,
					golive.SecLibrary, golive.SecHandover} {
					worst, n := sectionStatus(r, sec)
					if n == 0 {
						continue
					}
					add("golive/"+sec, worst, "see the golive lines of this section", "%d checks, worst %s", n, worst)
				}
			}
		} else if v.Golive == nil {
			add("golive", golive.Warn, "", "not checked")
		}
		if pol != nil {
			for _, k := range sortedKeys(pol.Versions) {
				if pol.Versions[k].Follows != vp.Key {
					continue
				}
				fix := "theatarr library adopt -version " + k + " -dry-run -json"
				if v.Adopt != nil {
					r, err := v.Adopt(ctx, k)
					switch {
					case err != nil:
						add("adopt/"+k, golive.Warn, fix, "dry run failed: %v", err)
					case len(r.Problems) > 0:
						add("adopt/"+k, golive.Warn, fix, "%d files looked at, %d links to make, %d problems", r.Files,
							r.Linked, len(r.Problems))
					default:
						add("adopt/"+k, golive.Pass, "", "%d files looked at, %d links to make, %d existing", r.Files,
							r.Linked, r.Existing)
					}
				}
				if v.Refresh != nil {
					fix := "theatarr library adopt -version " + k + " -refresh -dry-run -json"
					r, err := v.Refresh(ctx, k)
					switch {
					case err != nil:
						add("refresh/"+k, golive.Warn, fix, "dry run failed: %v", err)
					case r.Failed > 0 || len(r.Problems) > 0:
						add("refresh/"+k, golive.Warn, fix, "%d links, %d to move, %d problems", r.Links, r.Swapped,
							len(r.Problems))
					default:
						add("refresh/"+k, golive.Pass, "", "%d links, %d current, %d to move", r.Links, r.Current, r.Swapped)
					}
				}
			}
		}
		vr.Ready = true
		for _, c := range vr.Checks {
			if c.Status == golive.Fail {
				vr.Ready = false
			}
		}
		vr.Cutover, vr.Rollback = cutover(p, inv, vp)
		rep.Versions = append(rep.Versions, vr)
	}
	return rep, nil
}

// stepFor reports whether a step builds the version.
func stepFor(s Step, vp VersionPlan) bool {
	if s.ID == "version/"+vp.Key || strings.HasSuffix(s.ID, "/"+vp.Key) && s.Kind == KindView {
		return true
	}
	return s.Kind == KindQuality && s.Payload != nil && s.Payload.Key == vp.Profile
}

func sectionStatus(r *golive.Report, sec string) (golive.Status, int) {
	rank := map[golive.Status]int{golive.Pass: 0, golive.Info: 1, golive.Warn: 2, golive.Fail: 3}
	worst, n := golive.Pass, 0
	for _, x := range r.Results {
		if x.Section != sec {
			continue
		}
		n++
		if rank[x.Status] > rank[worst] {
			worst = x.Status
		}
	}
	return worst, n
}

func verifyPolicy(pol *policy.Policy, p *Plan, inv *Inventory, vp VersionPlan,
	add func(string, golive.Status, string, string, ...any)) {
	v := pol.Versions[vp.Key]
	if v == nil {
		add("version", golive.Fail, "apply the plan's version step", "Theatarr has no version %s", vp.Key)
		return
	}
	add("version", golive.Pass, "", "version %s: mode %s, grab %t", vp.Key, v.Mode, v.Grab)
	fed := ""
	if pol.Import != nil {
		for inst, src := range pol.Import.Arr {
			if src.Version == vp.Key {
				fed = inst
			}
		}
	}
	if fed == "" {
		add("import", golive.Fail, "do the plan's import/"+vp.Instance+" step", "no import.arr instance feeds %s: no "+
			"titles, files or shadow comparison", vp.Key)
	} else {
		add("import", golive.Pass, "", "import.arr %s feeds %s", fed, vp.Key)
	}
	if vp.View == "" {
		if inv.Seerr != nil && len(vp.Seerr) > 0 {
			add("view", golive.Fail, "add a view in "+uipages.Apps+" and plan again", "Seerr uses %s but no view "+
				"serves %s", vp.Instance, vp.Key)
		}
		return
	}
	view := pol.Views[vp.View]
	if view == nil {
		add("view", golive.Fail, "", "view %s is gone from the policy", vp.View)
		return
	}
	if !slices.Contains(view.Versions, vp.Key) {
		add("view", golive.Fail, "apply the plan's view step", "view %s (%s) does not serve %s", vp.View, view.Path, vp.Key)
		return
	}
	var missing []string
	if inv.Seerr != nil {
		for _, srv := range append(append([]SeerrServer(nil), inv.Seerr.Radarr...), inv.Seerr.Sonarr...) {
			if srv.Instance != vp.Instance {
				continue
			}
			for _, name := range []string{srv.ActiveProfileName, srv.ActiveAnimeProfileName} {
				if name != "" && !slices.Contains(view.Profiles[name], vp.Key) {
					missing = append(missing, name)
				}
			}
		}
	}
	if len(missing) > 0 {
		add("view", golive.Fail, "apply the plan's view step", "view %s lists no profile %s for %s: Seerr's requests "+
			"would not reach the version", vp.View, strings.Join(missing, ", "), vp.Key)
		return
	}
	add("view", golive.Pass, "", "view %s (%s) serves %s with the profiles Seerr uses", vp.View, view.Path, vp.Key)
}

// cutover is the manual handover and its rollback for one version.
func cutover(p *Plan, inv *Inventory, vp VersionPlan) ([]string, []string) {
	url := ""
	if a := inv.Instance(vp.Instance); a != nil {
		url = a.URL
	}
	steps := []string{
		fmt.Sprintf("Drain %s (%s): wait for its queue to empty, then turn off RSS and automatic search on its indexers "+
			"(or disable its download client) so it stops grabbing; keep it running read-only", vp.Instance, url),
	}
	var seerr []string
	for _, s := range p.Steps {
		if s.Kind == KindManual && strings.HasPrefix(s.ID, "seerr/") && slices.Contains(s.DependsOn, "view/"+vp.View+"/"+vp.Key) {
			seerr = append(seerr, strings.TrimPrefix(s.What, "at cutover: "))
		}
	}
	if len(seerr) > 0 {
		steps = append(steps, "Switch Seerr: "+strings.Join(seerr, "; "))
	} else {
		steps = append(steps, "No Seerr server points at "+vp.Instance+": nothing to switch")
	}
	steps = append(steps,
		fmt.Sprintf("Enable grabbing for %s only: "+uipages.Libraries+" › %s, mode active, then grab on; each is a "+
			"confirmed change listing its effects. The global router switch is not part of the migration: it stays "+
			"as the administrator set it", vp.Key, vp.Key),
		fmt.Sprintf("Watch the first grabs: theatarr golive check -version %s, Activity and the journal", vp.Key))
	back := []string{
		fmt.Sprintf("Turn grab off for %s ("+uipages.Libraries+"; turning it off needs no confirmation), and mode back "+
			"to shadow", vp.Key),
		fmt.Sprintf("Point Seerr back at %s and turn %s's RSS and automatic search on again", url, vp.Instance),
		"Titles requested while Theatarr served them are in Theatarr only: add them to the arr (the version's title " +
			"list in Library shows them)",
	}
	if n := len(p.Checkpoints); n > 0 {
		back = append(back, fmt.Sprintf("Undo the whole migration: stop Theatarr and run theatarr backup restore -from %s "+
			"(settings generation %d before apply)", p.Checkpoints[n-1].Backup, p.Checkpoints[n-1].Generation))
	}
	return steps, back
}
