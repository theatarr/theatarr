package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// GlobalSwitches stores the requested state; emergency overrides only affect the runtime policy.
type GlobalSwitches struct {
	RouterEnabled  bool `json:"router_enabled"`
	LibraryEnabled bool `json:"library_enabled"`
	NotifyEnabled  bool `json:"notify_enabled"`
}

func switchesFrom(p *policy.Policy) GlobalSwitches {
	return GlobalSwitches{RouterEnabled: p.Router != nil && p.Router.Enabled,
		LibraryEnabled: p.Library != nil && p.Library.Enabled, NotifyEnabled: p.Notify != nil && p.Notify.Enabled}
}

func (s GlobalSwitches) apply(p *policy.Policy) {
	if p.Router == nil && s.RouterEnabled {
		p.Router = &policy.Router{}
	}
	if p.Library == nil && s.LibraryEnabled {
		p.Library = &policy.Library{}
	}
	if p.Notify == nil && s.NotifyEnabled {
		p.Notify = &policy.Notify{}
	}
	if p.Router != nil {
		p.Router.Enabled = s.RouterEnabled
	}
	if p.Library != nil {
		p.Library.Enabled = s.LibraryEnabled
	}
	if p.Notify != nil {
		p.Notify.Enabled = s.NotifyEnabled
	}
}

// ForceOffVars are the emergency override's names.
var ForceOffVars = []string{"THEATARR_FORCE_OFF"}

// ApplyForceOff accepts only off overrides: router, library and notify named in the variable are forced off.
// Other names are recorded in p.ForceOffUnknown for the startup warning and preflight; they change nothing.
func ApplyForceOff(p *policy.Policy) {
	for _, name := range ForceOffVars {
		for _, key := range strings.Split(os.Getenv(name), ",") {
			key = strings.TrimSpace(key)
			switch key {
			case "":
				continue
			case "router":
				if p.Router != nil {
					p.Router.Enabled = false
				}
			case "library":
				if p.Library != nil {
					p.Library.Enabled = false
				}
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
		log.Warn("FORCE_OFF names an unknown switch; it forces nothing off (known: router, library, notify)", "entry", unknown)
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
		if log != nil {
			yaml, stored := switchesFrom(base), *current.Records.Switches
			for _, entry := range []struct {
				key       string
				different bool
			}{
				{"router.enabled", yaml.RouterEnabled != stored.RouterEnabled},
				{"library.enabled", yaml.LibraryEnabled != stored.LibraryEnabled},
				{"notify.enabled", yaml.NotifyEnabled != stored.NotifyEnabled},
			} {
				if entry.different {
					log.Warn("YAML setting ignored; database owns key", "key", entry.key)
				}
			}
		}
		return current, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, dbError(err)
	}
	records := current.Records
	value := switchesFrom(base)
	records.Switches = &value
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

func globalSwitchEffects(ctx context.Context, q rootQueryer, previous, next *Snapshot) ([]ConfirmationEffect, error) {
	if next.Records.Switches == nil {
		return nil, nil
	}
	before := switchesFrom(previous.Policy)
	if previous.Records.Switches != nil {
		before = *previous.Records.Switches
	}
	after := *next.Records.Switches
	var effects []ConfirmationEffect
	if !before.RouterEnabled && after.RouterEnabled {
		var versions []string
		for _, key := range sortedPolicyKeys(next.Policy.Versions) {
			v := next.Policy.Versions[key]
			if v.Mode != policy.ModeActive || !v.Grab || v.Storage != policy.StorageDebrid {
				continue
			}
			if v.ViaClient() {
				versions = append(versions, fmt.Sprintf("%s (grab on): client %s", key, v.Client))
				continue
			}
			var providers []string
			for _, provider := range v.Providers {
				for _, r := range next.Records.Providers {
					if r.Key == provider && r.Enabled {
						providers = append(providers, provider)
					}
				}
			}
			versions = append(versions, fmt.Sprintf("%s (grab on): providers %s", key, providerList(providers)))
		}
		effect := "Enable router.enabled: new torrent adds to debrid are allowed for active libraries " + switchList(versions) + "."
		if next.Policy.Library == nil || !next.Policy.Library.Enabled {
			effect += " Warning: library.enabled is off; debrid adds can happen, but files will not be linked into the library or reach Plex."
		}
		effects = append(effects, ConfirmationEffect{Effect: effect})
	}
	if !before.LibraryEnabled && after.LibraryEnabled {
		var versions []string
		for _, key := range sortedPolicyKeys(next.Policy.Versions) {
			v := next.Policy.Versions[key]
			if v.Mode == policy.ModeActive && len(next.Policy.Followers(key)) == 0 {
				versions = append(versions, key+": roots "+switchList(configuredVersionRoots(next.Policy, key)))
			}
		}
		effects = append(effects, ConfirmationEffect{Effect: "Enable library.enabled: links may be written by active libraries " + switchList(versions) + "."})
	}
	if !before.NotifyEnabled && after.NotifyEnabled {
		var targets []string
		if next.Policy.Notify != nil {
			targets = sortedPolicyKeys(next.Policy.Notify.Targets)
		}
		// Installed arr clients are destinations too. Names and IDs suffice; webhook URLs may contain secrets.
		if q != nil {
			rows, err := q.QueryContext(ctx, "SELECT id, view, name FROM arrview_notification ORDER BY id")
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var view, name string
				if err := rows.Scan(&id, &view, &name); err != nil {
					return nil, err
				}
				targets = append(targets, fmt.Sprintf("%s/%s (notification %d)", view, name, id))
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		effects = append(effects, ConfirmationEffect{Effect: "Enable notify.enabled: messages may be sent to targets " + switchList(targets) + "."})
	}
	for i := range effects {
		if len(next.Policy.ForcedOff) > 0 {
			effects[i].Effect += " Environment overrides remain in force: " + strings.Join(next.Policy.ForcedOff, ", ") + "."
		}
	}
	return effects, nil
}

func switchList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, "; ")
}
