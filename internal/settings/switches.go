package settings

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// GlobalSwitches stores Pause. The runtime switches router.enabled and library.enabled are both derived from it
// (both are !Paused); emergency overrides only affect the runtime policy. Notifications are the notify section's own
// enabled field.
type GlobalSwitches struct {
	Paused bool `json:"paused"`
}

func (s GlobalSwitches) apply(p *policy.Policy) {
	if !s.Paused {
		if p.Router == nil {
			p.Router = &policy.Router{}
		}
		if p.Library == nil {
			p.Library = &policy.Library{}
		}
	}
	if p.Router != nil {
		p.Router.Enabled = !s.Paused
	}
	if p.Library != nil {
		p.Library.Enabled = !s.Paused
	}
}

// ForceOffVars are the emergency override's names.
var ForceOffVars = []string{"THEATARR_FORCE_OFF"}

// ApplyForceOff accepts only off overrides: pause, router and library named in the variable force Pause (router and
// library are both closed, recorded as "pause"); notify forces notifications off. Other names are recorded in
// p.ForceOffUnknown for the startup warning and preflight; they change nothing.
func ApplyForceOff(p *policy.Policy) {
	for _, name := range ForceOffVars {
		for _, key := range strings.Split(os.Getenv(name), ",") {
			key = strings.TrimSpace(key)
			switch key {
			case "":
				continue
			case "pause", "router", "library":
				if p.Router != nil {
					p.Router.Enabled = false
				}
				if p.Library != nil {
					p.Library.Enabled = false
				}
				key = "pause"
			case "notify":
				if p.Notify != nil {
					p.Notify.Enabled = false
				}
			default:
				if unknown := name + ": " + key; !slices.Contains(p.ForceOffUnknown, unknown) {
					p.ForceOffUnknown = append(p.ForceOffUnknown, unknown)
				}
				continue
			}
			if !slices.Contains(p.ForcedOff, key) {
				p.ForcedOff = append(p.ForcedOff, key)
			}
		}
	}
}

// WarnForceOffUnknown logs each name in the override variables that is not a switch: it forces nothing off.
func WarnForceOffUnknown(p *policy.Policy, log *slog.Logger) {
	for _, unknown := range p.ForceOffUnknown {
		log.Warn("FORCE_OFF names an unknown switch; it forces nothing off (known: pause, notify)", "entry", unknown)
	}
}

// BootstrapSwitches copies YAML once, including on an existing DB. The record and marker commit together.
func (s *Store) BootstrapSwitches(ctx context.Context, base *policy.Policy, actor string, log *slog.Logger) (*Snapshot, error) {
	current, err := s.Load(ctx, base)
	if err != nil {
		return nil, err
	}
	var marker int64
	err = s.db.Reader().QueryRowContext(ctx, "SELECT generation FROM settings_switches_bootstrap WHERE id = 1").Scan(&marker)
	if err == nil {
		if current.Records.Switches == nil {
			return nil, errors.New("settings: global switches record missing after bootstrap")
		}
		return current, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, dbError(err)
	}
	records := current.Records
	records.Switches = &GlobalSwitches{Paused: true}
	next, err := s.saveWithHook(ctx, SaveRequest{ExpectedGeneration: current.Generation, Actor: actor, Base: base,
		Records: records, bootstrap: true, bootstrapSections: "switches"}, "bootstrap-import", func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO settings_switches_bootstrap (id, generation) VALUES (1, ?)", current.Generation+1)
		return err
	})
	if errors.Is(err, ErrConflict) {
		return s.BootstrapSwitches(ctx, base, actor, log)
	}
	return next, err
}

// globalSwitchEffects is the one confirmation of the switches: turning Pause off. Pause on asks nothing.
func globalSwitchEffects(previous, next *Snapshot) []ConfirmationEffect {
	if next.Records.Switches == nil || next.Records.Switches.Paused {
		return nil
	}
	wasPaused := previous.Policy == nil || previous.Policy.Router == nil || !previous.Policy.Router.Enabled
	if previous.Records.Switches != nil {
		wasPaused = previous.Records.Switches.Paused
	}
	if !wasPaused {
		return nil
	}
	var versions []string
	for _, key := range sortedPolicyKeys(next.Policy.Versions) {
		if next.Policy.Versions[key].Mode == policy.ModeActive {
			versions = append(versions, key)
		}
	}
	effect := "Turn Pause off: Live libraries search, download and link again: " + switchList(versions) +
		". Waiting downloads are imported."
	if len(next.Policy.ForcedOff) > 0 {
		effect += " Environment overrides remain in force: " + strings.Join(next.Policy.ForcedOff, ", ") + "."
	}
	return []ConfirmationEffect{{Effect: effect}}
}

func switchList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, "; ")
}
