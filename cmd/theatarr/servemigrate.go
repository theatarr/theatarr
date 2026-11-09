package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

// serveMigrationConfig is what the /migrate hooks read (docs/migration.md).
type serveMigrationConfig struct {
	st   *store.Store
	base *policy.Policy // the policy serve started with: its directory goes into the backup set
	// policyNow, when set, gives the policy the set records (directory and secret names) at checkpoint time; else base.
	policyNow func(context.Context) *policy.Policy
	manager   *settings.RuntimeCoordinator
	backupDir string
	lib       *library.Library
	health    *health.Checker
	trash     string
	log       *slog.Logger
	now       func() time.Time
}

// serveMigration builds the /migrate hooks: apply's rollback checkpoint and verify's read-only checks.
func serveMigration(c serveMigrationConfig) *web.Migration {
	if c.now == nil {
		c.now = time.Now
	}
	current := func(ctx context.Context) (*policy.Policy, error) {
		head, err := c.manager.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return head.Policy, nil
	}
	v := migration.Verifier{Policy: current, Golive: func(ctx context.Context, version string) (*golive.Report, error) {
		head, err := c.manager.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return golive.Run(ctx, serveGoliveOptions(ctx, head, c, version))
	}}
	if c.lib != nil {
		v.Adopt = func(ctx context.Context, version string) (*library.AdoptReport, error) {
			return c.lib.Adopt(ctx, library.AdoptOptions{Version: version, DryRun: true})
		}
		v.Refresh = func(ctx context.Context, version string) (*library.RefreshReport, error) {
			return c.lib.Refresh(ctx, library.RefreshOptions{Version: version, DryRun: true})
		}
	}
	return &web.Migration{Checkpoint: func(ctx context.Context) (migration.Checkpoint, error) {
		return migrationCheckpoint(ctx, c)
	}, Verifier: v}
}

// migrationCheckpoint is theatarr backup now (database, policy files, manifest) plus the settings export beside it.
func migrationCheckpoint(ctx context.Context, c serveMigrationConfig) (migration.Checkpoint, error) {
	if c.backupDir == "" {
		return migration.Checkpoint{}, errors.New("backups are off (-backup-dir off): apply needs a backup set to roll back to")
	}
	head, err := c.manager.Snapshot(ctx)
	if err != nil {
		return migration.Checkpoint{}, err
	}
	now := c.now()
	pol := c.base
	if c.policyNow != nil {
		pol = c.policyNow(ctx)
	}
	base, _, err := writeBackupSet(ctx, c.st, pol, c.backupDir, now)
	if err != nil {
		return migration.Checkpoint{}, err
	}
	b, err := head.Export()
	if err != nil {
		return migration.Checkpoint{}, err
	}
	if err := writeAtomic(base+".settings.yaml", b); err != nil {
		return migration.Checkpoint{}, err
	}
	c.log.Info("migration checkpoint", "backup", base, "generation", head.Generation)
	return migration.Checkpoint{At: now.UTC(), Backup: base, Settings: base + ".settings.yaml", Generation: head.Generation}, nil
}

// serveGoliveOptions is theatarr golive check's options inside serve: the stored head policy and its credentials
// (stored in the settings database or named env variables), serve's own health results and database, the arrs
// import.arr names (GET) and the engine's profiles. The service state (VPN egress from /metrics) is not read: the
// CLI check covers it.
func serveGoliveOptions(ctx context.Context, head *settings.Snapshot, c serveMigrationConfig, version string) golive.Options {
	pol := head.Policy
	creds := settings.Credentials{Policy: pol, Snap: head, Lookup: os.LookupEnv}
	o := golive.Options{Policy: pol, Version: version, Env: os.Getenv, Credential: creds.Resolves, FS: golive.OSFS{}, DB: c.st.Reader(),
		PublicURL: envOr("THEATARR_PUBLIC_URL", ""), Days: 14, MinGrabs: 20}
	if c.health != nil {
		o.Health = c.health.Results(ctx)
	}
	if c.lib != nil {
		o.Scan = c.lib.Verify
	}
	o.Arr = goliveArr(pol)
	e, berr := decide.New(pol, decide.Options{TrashRoot: c.trash, Arr: liveArr(pol)})
	o.Profile = func(name string, kind policy.Kind) error {
		fl := format.Radarr
		if kind == policy.KindSeries {
			fl = format.Sonarr
		}
		if e != nil && e.Profile(name, fl) != nil {
			return nil
		}
		if berr != nil && strings.Contains(berr.Error(), name) {
			return profileError(berr, name)
		}
		return errors.New("not built")
	}
	return o
}
