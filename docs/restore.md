# Backup and restore

What it takes to bring Theatarr back on a new host or after a lost volume: the **database**, the **policy directory**
and the **secrets** together, then a **reconcile** of the library links against the mount. Code:
`cmd/theatarr/backup.go`, the nightly job in `cmd/theatarr/serve.go`, and the health check `BackupCheck`
(`internal/health`).

The commands below assume the [example compose file](../examples/compose.yaml): service `theatarr`, `./data` on the
host mounted at `/data`. Adjust them to your setup.

## What a backup set holds

`theatarr serve` writes one set every night at `-backup-hour` (default 03:00 local time) into `-backup-dir`, by
default `backups/` next to the database (`/data/backups` in the container), and keeps the newest `-backup-keep` sets
(default 14; env `THEATARR_BACKUP_KEEP`). `-backup-dir off` turns backups off, which `BackupCheck` reports as a
warning. `theatarr backup now` writes a set at once, with the same code.

| File | What |
|---|---|
| `theatarr-<UTC stamp>.db` | A consistent snapshot of the database (`VACUUM INTO`, taken while the service writes) |
| `theatarr-<UTC stamp>.policy.tar` | The policy directory: every regular file, `overrides/` included. Left out when the directory does not exist |
| `theatarr-<UTC stamp>.manifest.json` | The Theatarr version, schema version, size and SHA-256 of both files, the policy revision (of the archived files, and the one the service ran with) and `policy.state`, and the **names** of the environment variables the instance reads secrets from, each with whether it was set |

The manifest is written last: a set without one is incomplete and `theatarr backup restore` refuses it. A backup that
fails part-way removes what it wrote, so no incomplete set is left behind. A failed nightly run still prunes old
sets and finished jobs, and retries. A migration checkpoint (written by migration apply) is the same set plus
`theatarr-<UTC stamp>.settings.yaml`, the settings export; pruning removes it with its set. A set written before a database
upgrade is the database alone ([Upgrades and image rollback](#upgrades-and-image-rollback)).

**Database-only installs.** Theatarr runs without any YAML: everything set in Settings lives in the database. The
manifest says what the policy directory held (`policy.state`, manifest format 2):

| `state` | Policy directory | In the set |
|---|---|---|
| `files` | has `.yaml`/`.yml` files | the archive |
| `empty` | exists, no policy files (the settings are in the database) | the archive, possibly with no files |
| `absent` | does not exist | no archive |

Format 1 manifests are read as `files`.

**Secrets are in the backup.** The database holds, in plaintext, every credential set in Settings or imported from
YAML (debrid provider keys, indexer keys and Cardigann logins), plus the web signing secret, password hashes, API key
hashes and Plex account ids. Only values still read from environment variables (for example a Plex token, webhook
auth headers, the TVDB PIN and any `*_env` credential never stored in Settings) stay out; the manifest lists their
names. The database, its `-wal`/`-shm` files and every backup `.db` are written mode 0600, and a data or backup
directory Theatarr creates is 0700 (an existing database is tightened to 0600 at open; existing directories are left
as they are). Keep backup copies as private as the data directory, never on a share.

Off-host copies are not Theatarr's job: copy `data/backups` with whatever backs up the host. Keep SQLite on local
storage; never point `-db` at a network share.

## Restore

### Restore from the web UI (Admin › System › Backup)

In **Admin › System › Backup**, the **Theatarr backups** card lists the backup sets and the nightly schedule. **Back
up now** writes a set immediately. Select **Restore…** beside a complete, compatible set and type its name exactly.
Incomplete sets (missing the database, manifest or declared policy archive) are marked unavailable. Sets with policy
files but no settings in the database must be restored from the command line, so the archived policy can be
installed too. Everything changed in Theatarr since that set is replaced, including users and credentials; you may
need to sign in again.

Theatarr first writes a fresh safety set of the current state, then saves `restore-request.json` beside the database.
After answering the browser it stops gracefully, as on SIGTERM. **The container must restart Theatarr automatically
(`restart: unless-stopped` in Docker).** On startup, before opening the database, Theatarr renames the request to
`restore-request.taken.json` and runs the same checked restore as the command line below. A claimed request is never
replayed after a crash. Hashes, integrity and configuration are checked before anything is replaced; the old
database is kept as `theatarr.db.pre-restore-<stamp>` and put back if a later restore step fails. If startup finds a
taken request from an interrupted restore, it restores the newest pre-restore database when the database is missing,
records a failed restore, and continues; if the database is present and matches the requested backup, the restore
is recorded as successful.

The browser reconnects and reloads when Theatarr is ready. `restore-result.json` records success or failure; the
card shows it for seven days, with the safety set to use to go back. A failed restore starts on the old database and
is also logged as an error (System › Logs). The restarted service keeps its configured policy directory: the restored
settings are checked against that directory, so a relative `metadata.overrides_dir` must exist there. Archived policy
files go to `policy-restored-<UTC stamp>` beside the database for reference only. A set whose stored settings do not
load with the configured directory is refused before anything is replaced and needs the command-line restore below.
Such a refusal shows after the restart, on the card and in the logs.

After a successful restore, Theatarr enqueues one provider sync. **Reconcile the library links yourself** (step 6
below); the card has the commands to copy. Library links and provider files are not part of the backup. Backups
contain plaintext secrets, so the UI deliberately offers no download.

### Restore from the command line

Use the image that wrote the backup, or a newer one. Theatarr refuses to open or migrate a database whose schema is
newer than the newest migration it knows; the error names both versions. Restore also refuses a newer manifest
schema before staging or replacing anything. `backup fingerprint` is a read-only diagnostic that works without
migrations.

1. **Pick the set and image.** If Theatarr is still running, list the sets:

   ```sh
   docker compose exec theatarr theatarr backup list
   ```

   Otherwise list them with a one-off container (`docker compose run --rm theatarr backup list`). Note the full set
   name and its schema.
2. **Stop the service** and keep it stopped until the restore and the policy are in place:
   `docker compose stop theatarr`.
3. **Restore the database and policy** in a one-off container with the same volumes. Replace `SET` and `STAMP` with
   the set name (for example `theatarr-20260101-030000`) and a new stamp:

   ```sh
   docker compose run --rm theatarr \
     backup restore -from /data/backups/SET -db /data/theatarr.db \
     -policy-out /data/policy-restored-STAMP -force
   ```

   `-policy-out` must be under the writable `/data`. The command checks the files against the manifest's hashes,
   stages the policy beside its target, copies the database to `theatarr.db.partial` and runs
   `PRAGMA integrity_check`. It then validates the configuration `serve` would start with on a scratch copy
   (`theatarr.db.validate`, migrated as `serve` would migrate it, then removed): the stored settings are loaded from
   the database, with the staged policy directory, so a relative `overrides_dir` resolves against the restored
   files. A set whose stored settings load is a normal set. A set with policy files but no stored settings, or with
   settings that do not load, is restored only if the settings import that completes it previews cleanly on the
   scratch copy (see [Policy-only sets](#policy-only-sets)). That import reads the credentials its policy names and
   the TRaSH formats from the container's environment, which `docker compose run` provides.

   Only then are the live files touched. `-force` keeps the existing database as `theatarr.db.pre-restore-<stamp>`
   with its `-wal` and `-shm`; if a step after the swap fails, the old database is put back. It never starts the
   service. A set with `state: absent` unpacks nothing and needs no `-policy-out`.
4. **Install the restored policy and secrets while stopped.** Skip this when you don't bind a policy directory (the
   example compose file doesn't). Otherwise move the current policy directory aside and replace it with the restored
   one, rather than copying old YAML over it:

   ```sh
   mv /path/to/policy /path/to/policy.pre-restore-STAMP
   cp -a data/policy-restored-STAMP /path/to/policy
   ```

   Restore prints each secret's environment variable with `set` or `MISSING` for the one-off container's environment;
   credentials stored in Settings are already restored. Set the missing ones in the service's environment before you
   start it. `-env-file FILE` checks the names against an env file instead (names only).
5. **Start the service** (`docker compose up -d`) and wait until it is healthy (`/readyz`, the container's
   healthcheck). Then check the schema and the health checks:

   ```sh
   docker compose exec theatarr theatarr migrate status
   docker compose exec theatarr theatarr health
   ```

   Expect every migration applied. `BackupCheck` warns if the newest set is old.
6. **Reconcile the library links.** The database's links are as of the backup; the symlinks and mount are as of now.
   Run these in the running container (`docker compose exec theatarr theatarr ...`):
   - `library verify` scans the library roots: counts, dangling links and naming differences.
   - `library dangling -scan` writes the dangling-link report. **System › Status** lists the same links under
     **Show the N broken links**.
   - `library relink -dry-run`, then `library relink`: repoints dangling links by info hash, through the library
     gate (Pause off, a library, the library's roots).
   - `providers sync` refreshes the content registry from the debrid accounts.

   Links made after the backup belong to downloads the restored database does not know; Theatarr leaves those files
   alone. The next wanted search looks for what the database still counts as missing. The journal and history show
   the gap between the backup and the restore.

## Upgrades and image rollback

The backup before an upgrade is automatic. When `theatarr serve` starts (or `theatarr migrate up` runs) on a
database that has a schema and migrations are pending, it first writes a database-only set into the backup folder,
then migrates. Its manifest says `"kind": "before-upgrade"` with the schema it came from and the one it migrates to;
it has no policy archive and no secrets list (the settings are not loaded yet). System › Backup lists it as
"Before upgrade to schema N", and `backup list` as "before upgrade to schema N". The last 3 such sets are kept; the
nightly pruning neither counts nor deletes them. If the copy cannot be written (no space, folder not writable) the
start stops with one line naming the cause and the folder, and nothing is migrated. With `--backup-dir off` no copy
is made and the log says so. A fresh database, or a start with nothing pending, writes nothing.

Keep the old image (`docker tag theatarr:local theatarr:previous` when built from source; a released image keeps its
version tag) and the old policy directory if you bind one. Then set `THEATARR_VERSION` to the new release and restart
(`docker compose up -d`; built from source: `docker compose up -d --build`), wait for `/readyz`, and run `migrate
status` and `errors list -status open` as in step 5, then `theatarr preflight`. Read the startup logs and
**System › Status** before you go on. A ready image is no reason to turn grabbing on.

To roll back: stop the service and restore the before-upgrade set **with the new image** (the one that wrote it), in
a one-off container as in step 3, giving `-policy-out` your existing policy directory:
`theatarr backup restore -from /data/backups/theatarr-<stamp> -db /data/theatarr.db -policy-out /config/policy -force`
(`/config/policy` is the policy directory the service runs with; read-only is fine). The database goes back to the
old schema; the policy files are checked against it and left as they are. Then start the
old image (`THEATARR_VERSION` back at the old release, or `image: theatarr:previous` when built from source) with
`THEATARR_FORCE_OFF=router,library,notify` until you have reconciled. Never start the old image against the migrated
database or try to downgrade it in place: startup and restore refuse that downgrade.

If a migration itself fails, the restore with the new image can refuse too: it migrates a scratch copy of the restored
database to check the configuration, and the same migration fails there. When that restore refuses, or the new image
does not start at all, roll back by hand; no Theatarr command is needed. Stop the container; in the data volume copy
the set's database over the live one (`cp /data/backups/theatarr-<stamp>.db /data/theatarr.db`); delete
`/data/theatarr.db-wal` and `/data/theatarr.db-shm` if they are there; then start the old image as above, with
`THEATARR_FORCE_OFF=router,library,notify` until you have reconciled. The policy directory is left as it is.

Sets written by the new image after the upgrade use the newer schema, which the old image cannot restore. Restoring
the before-upgrade set loses the Theatarr records and settings written since; provider files and library links stay
on disk and need the reconciliation in step 6.

## Restore drill

`scripts/restore-drill.sh` runs these steps against a demo server ([demo.md](demo.md)) on an internal-only Docker
network: it backs up the running server, fingerprints the set (`theatarr backup fingerprint`: counts and hashes of
titles, libraries, links and users), restores it into a fresh volume, starts the copy beside the original, and fails
unless the copy's fingerprint matches. It removes the copy afterwards (`KEEP=1` keeps it).

## Policy-only sets

A set whose settings were never fully stored in the database, or that holds policy files and no settings, cannot be
restored from the web UI. The command-line restore previews the settings import on its staged copy, with the
container's credentials and `THEATARR_TRASH` (an `-env-file` is read for names only). Only if that preview succeeds
does it restore, and it names the import in its `next:` lines; otherwise it refuses with the import's error before
the database is replaced, even with `-force`.

```
next: the set has no complete settings generation: import its policy before starting serve:
  theatarr settings import -policy RESTORED_DIR -db FILE   (review the preview, then repeat with -confirm)
  then start theatarr serve with -db FILE -policy RESTORED_DIR and the secrets above, wait for /readyz, then
  theatarr library dangling -scan; theatarr library relink -dry-run   (docs/restore.md)
```

Run that import with the service stopped, review the preview and repeat with `-confirm`. Startup never runs the
import itself. Keep override data at the path the stored settings name; a policy archive is also a backup of those
files.
