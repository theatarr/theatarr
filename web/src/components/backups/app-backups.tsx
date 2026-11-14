import { useEffect, useState } from "react";
import { useAppBackups, useAppBackupActions, type AppBackupSet } from "@/api/backups";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetLayout } from "@/components/ui/sheet";
import { fmtAgo, fmtBytes, fmtTime } from "@/lib/format";

/** A backup's age in whole days: "1 day", "3 days". */
const commands = ["library verify", "library dangling -scan", "library relink -dry-run", "library relink"];

function Restarting() {
  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setInterval(() => {
      void fetch("/readyz", { cache: "no-store", signal: controller.signal })
        .then((r) => { if (r.ok) window.location.reload(); })
        .catch(() => { /* The service is stopping or starting. Keep reconnecting. */ });
    }, 3000);
    return () => { window.clearInterval(timer); controller.abort(); };
  }, []);
  return <div role="status" className="w-full rounded-lg border p-6">
    <h3 className="font-semibold">Restarting…</h3>
    <p>Theatarr is restoring your backup. This page reconnects when it is back. You may need to sign in again.</p>
  </div>;
}

export function AppBackups() {
  const query = useAppBackups();
  const { backupNow, restore } = useAppBackupActions();
  const [selected, setSelected] = useState<AppBackupSet | null>(null);
  const [confirm, setConfirm] = useState("");
  const [copied, setCopied] = useState("");
  const data = query.data;
  const result = data?.last_restore;
  const recent = result && Date.now() - Date.parse(result.finished_at) < 7 * 86400_000;
  const close = () => { if (!restore.isPending) setSelected(null); };
  return <Card>
    <CardHeader><CardTitle>Theatarr backups</CardTitle></CardHeader>
    <CardContent>
      {recent && (result.ok ? <div role="status" className="rounded-lg border p-3 space-y-2">
        <p>Restored {result.set} on {fmtTime(result.finished_at)}. Your previous state is saved as {result.safety_set}.</p>
        <h3 className="font-semibold">Finish up</h3>
        <ol className="list-decimal pl-5 space-y-2">{commands.map((command) => {
          const text = `docker exec theatarr theatarr ${command}`;
          return <li key={command}><code className="break-all select-all">{text}</code>{" "}<Button size="sm" variant="outline" onClick={() => {
            void navigator.clipboard.writeText(text).then(() => setCopied(command)).catch(() => setCopied("Copy failed; select the command text."));
          }}>Copy {command}</Button></li>;
        })}</ol>
        {copied && <p role="status">{copied.startsWith("Copy failed") ? copied : `Copied ${copied}`}</p>}
        <p>Provider sync was started. Review the dry run before running library relink.</p>
        {result.policy_out && <p>{result.message} Policy archive: <code>{result.policy_out}</code></p>}
      </div> : <p role="alert">Restore of {result.set} failed: {result.error}. Theatarr kept running on the database it had before.</p>)}
      <p>A backup holds Theatarr&apos;s whole database: settings, library records, users and stored credentials. Restoring replaces everything with the backup&apos;s copy.</p>
      {query.isPending && <p>Loading backups…</p>}
      {query.error && <p role="alert">{query.error.message}</p>}
      {data && <>
        <p>{data.enabled ? `Nightly at ${String(data.hour).padStart(2, "0")}:00, keeping ${data.keep}.` : "Backups are off. Set --backup-dir to enable them."}</p>
        {restore.isSuccess ? <Restarting /> : <>
          <Button disabled={!data.enabled || backupNow.isPending || restore.isPending} onClick={() => backupNow.mutate()}>Back up now</Button>
          {backupNow.isSuccess && <p role="status">Backup created: {backupNow.data.name}</p>}
          {backupNow.error && <p role="alert">{backupNow.error.message}</p>}
          <div className="overflow-x-auto"><table className="w-full text-left text-sm">
            <thead><tr><th className="p-2">Date / name</th><th className="p-2">Age</th><th className="p-2">Size</th><th className="p-2">Restore</th></tr></thead>
            <tbody>{data.sets.map((set) => <tr key={set.name} className="border-t">
              <td className="p-2">{fmtTime(set.created_at)}<div className="font-mono text-xs">{set.name}</div></td>
              <td className="p-2">{fmtAgo(set.created_at)}</td>
              <td className="p-2">{fmtBytes(set.bytes)}</td>
              <td className="p-2"><Button variant="outline" disabled={!set.restorable || !data.enabled || backupNow.isPending} onClick={() => { setSelected(set); setConfirm(""); restore.reset(); }}>Restore…</Button>
                {set.why_not && <p className="text-xs">{set.why_not}</p>}</td>
            </tr>)}</tbody>
          </table></div>
          {!data.sets.length && <p>No backup sets yet.</p>}
        </>}
      </>}
      {selected && !restore.isSuccess && <Sheet label="Restore backup" onClose={close}>
        <SheetLayout title="Restore backup" onClose={close} footer={<>
          <Button variant="outline" disabled={restore.isPending} onClick={close}>Cancel</Button>
          <Button disabled={confirm !== selected.name || restore.isPending} onClick={() => restore.mutate({ name: selected.name, confirm })}>{restore.isPending ? "Preparing safety backup…" : "Restore"}</Button>
        </>}>
          <div className="space-y-4">
            <p>Everything changed since {fmtTime(selected.created_at)} will be lost: settings, library records, users and stored credentials.</p>
            <p>A safety backup of your current state is made first.</p>
            <p>Theatarr restarts and you may need to sign in again.</p>
            <p>Your container must restart Theatarr automatically (Docker restart: unless-stopped).</p>
            <label className="block">Type <code>{selected.name}</code> exactly to confirm.
              <Input aria-label="Backup name confirmation" value={confirm} disabled={restore.isPending} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
            </label>
            {restore.error && <p role="alert">{restore.error.message}</p>}
          </div>
        </SheetLayout>
      </Sheet>}
    </CardContent>
  </Card>;
}
