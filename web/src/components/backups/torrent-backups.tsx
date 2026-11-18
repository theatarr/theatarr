import { Link } from "react-router";
import { useRef, useState, type ChangeEvent } from "react";
import { backupURL, useTorrentActions, useTorrentBackups, useTorrentReport } from "@/api/torrents";
import { toast } from "@/components/toaster";
import { Empty, ErrorState, Loading, Section } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtBytes, fmtNumber, fmtTime } from "@/lib/format";
import { restoreScope, type RestorePlan } from "@/lib/torrents";
import { settingHref } from "@/lib/settings-map";
import { ProviderTag } from "@/components/provider-pills";

/** A backup taken while no provider could be read: it holds nothing to restore. One with no provider at all, or with an
 * account listed (even with zero torrents), is good. */
const backupFailed = (b: { providers: { ok: boolean }[] }) => b.providers.length > 0 && b.providers.every((p) => !p.ok);
const FAILED = "Failed: no provider could be read";

export function TorrentBackups() {
  const q = useTorrentBackups();
  const report = useTorrentReport();
  const actions = useTorrentActions();
  const file = useRef<HTMLInputElement>(null);
  const [target, setTarget] = useState("");
  const [restore, setRestore] = useState<{ id: string; plan: RestorePlan } | null>(null);
  const [restored, setRestored] = useState<string | undefined>();
  const providers = (report.data?.providers ?? []).map((p) => p.provider);

  const onFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f) return;
    let data: unknown;
    try {
      data = JSON.parse(await f.text());
    } catch {
      toast({ tone: "bad", title: "Import failed", body: "The file is not JSON" });
      return;
    }
    actions.importBackup.mutate(
      { data, provider: target },
      {
        onSuccess: (b) => toast({ tone: "ok", title: `Imported ${b.torrents} torrents as backup ${b.id}` }),
        onError: (err) => toast({ tone: "bad", title: "Import failed", body: errText(err) }),
      },
    );
  };
  const openRestore = (id: string) => {
    setRestored(undefined);
    actions.restore.reset();
    actions.restorePreview.mutate({ backup_id: id, target: target || undefined }, {
      onSuccess: (plan) => setRestore({ id, plan }),
      onError: (err) => toast({ tone: "bad", title: "Restore preview failed", body: errText(err) }),
    });
  };
  const confirmRestore = () => {
    if (!restore) return;
    actions.restore.mutate(
      { backup_id: restore.id, target: target || undefined, confirmation_token: restore.plan.confirmation_token },
      { onSuccess: (r) => setRestored(`Restore queued as job #${r.job}.`) },
    );
  };
  const every = q.data?.every_s ?? 0;
  return (
    <Section
      title="Torrent-list backups"
      aside={
        <div className="flex flex-wrap items-center gap-2">
          <select className="h-8 rounded-md border border-border bg-transparent px-2 text-sm" value={target} onChange={(e) => setTarget(e.target.value)} aria-label="Restore or import to">
            <option value="">Same provider</option>
            {providers.map((p) => <option key={p} value={p}>to {p}</option>)}
          </select>
          <Button size="sm" variant="outline" onClick={() => file.current?.click()} disabled={actions.importBackup.isPending}>Import…</Button>
          <input ref={file} type="file" accept="application/json,.json" className="hidden" onChange={onFile} data-testid="import-file" />
          <Button
            size="sm"
            onClick={() => actions.backupNow.mutate(undefined, { onSuccess: (r) => toast({ tone: "ok", title: `Backup queued as job #${r.job}` }), onError: (err) => toast({ tone: "bad", title: "Backup failed", body: errText(err) }) })}
            disabled={actions.backupNow.isPending}
            data-testid="backup-now"
          >
            Back up now
          </Button>
        </div>
      }
    >
      <p className="mb-2 text-sm text-muted-foreground">Saved lists of your debrid torrents. Restoring re-adds them to a provider; it does not touch Theatarr’s settings.</p>
      {q.data ? <p className="mb-2 text-sm text-muted-foreground">
        <span data-testid="torrent-backup-schedule">{every > 0 ? `Scheduled every ${Math.round(every / 3600)} h` : "Scheduled backups are off"} · keeps the newest {q.data.keep} good backups and every backup from the last week.</span>{" "}
        {/* The schedule is a setting: read here, changed in Settings › Download clients › Torrents. */}
        <Link className="text-primary hover:underline" to={settingHref("torrents", "backup") ?? "/admin/settings"} data-testid="torrent-backup-edit">Change the schedule</Link>.
        {" "}A Debrid Media Manager backup imports with a provider chosen above.
      </p> : null}
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : (q.data?.backups ?? []).length === 0 ? <Card><Empty>No backup yet.</Empty></Card> : (
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Taken</TableHead>
                <TableHead className="hidden sm:table-cell">Reason</TableHead>
                <TableHead className="hidden text-right sm:table-cell">Torrents</TableHead>
                <TableHead className="hidden md:table-cell">Providers</TableHead>
                <TableHead><span className="sr-only">Actions</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(q.data?.backups ?? []).map((b) => { const failed = backupFailed(b); const unreadable = b.providers.filter((p) => !p.ok); return (
                <TableRow key={b.id} data-testid="torrent-backup-row">
                  <TableCell title={b.id}>
                    <div className="whitespace-nowrap">{fmtTime(b.created_at)}</div>
                    {/* On a phone the reason and count go under the date, the actions stack in their column. */}
                    <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground sm:hidden"><Badge tone={b.reason === "pre-delete" ? "warning" : "neutral"}>{b.reason}</Badge>{failed ? <span className="text-destructive" data-testid="torrent-backup-failed">{FAILED}</span> : <>{fmtNumber(b.torrents)} · {fmtBytes(b.bytes)}</>}</div>
                    {/* The unreadable providers, which the Providers column shows from md up. */}
                    {!failed && unreadable.length ? <div className="mt-0.5 text-xs text-[var(--warn-ink)] md:hidden" data-testid="torrent-backup-unreadable">{unreadable.map((p, i) => <span key={p.provider}>{i > 0 ? ", " : ""}<ProviderTag account={p.provider} /> unreadable</span>)}</div> : null}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell"><Badge tone={b.reason === "pre-delete" ? "warning" : "neutral"}>{b.reason}</Badge></TableCell>
                  <TableCell className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{failed ? <span className="text-destructive">{FAILED}</span> : <>{fmtNumber(b.torrents)} · {fmtBytes(b.bytes)}</>}</TableCell>
                  <TableCell className="hidden text-xs md:table-cell">{b.providers.map((p, i) => <span key={p.provider}>{i > 0 ? ", " : ""}<ProviderTag account={p.provider} /> {p.ok ? p.torrents : "unreadable"}</span>)}</TableCell>
                  <TableCell className="text-right">
                    <span className="inline-flex flex-col items-end gap-1 sm:flex-row sm:items-center sm:gap-2">
                      <a className="text-sm hover:text-primary" href={backupURL(b.id)} download>Download</a>
                      <a className="text-sm hover:text-primary" href={backupURL(b.id, "dmm")} download title="Download in Debrid Media Manager's format">DMM</a>
                      {failed ? null : <Button size="xs" variant="outline" onClick={() => openRestore(b.id)} disabled={actions.restorePreview.isPending}>Restore…</Button>}
                    </span>
                  </TableCell>
                </TableRow>
              ); })}
            </TableBody>
          </Table>
        </Card>
      )}
      {restore ? (
        <ConfirmDialog
          scope={restoreScope(restore.plan)}
          subject={`Backup ${restore.id}`}
          danger={false}
          onConfirm={confirmRestore}
          onClose={() => setRestore(null)}
          busy={actions.restore.isPending}
          error={actions.restore.error}
          done={restored}
          testid="torrent-restore"
        >
          {restored ? <p><Link to="/admin/torrents">Torrents</Link> shows each re-add as it happens.</p> : null}
          <ul className="max-h-60 space-y-1 overflow-y-auto text-xs">
            {restore.plan.items.filter((it) => it.action !== "present").map((it) => (
              <li key={`${it.target}:${it.hash}`} className="[overflow-wrap:anywhere]"><Badge tone={it.action === "readd" ? "info" : "neutral"}>{it.action}</Badge> {it.name || it.hash}{it.reason ? `: ${it.reason}` : ""}</li>
            ))}
          </ul>
        </ConfirmDialog>
      ) : null}
    </Section>
  );
}

