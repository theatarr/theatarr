// Activity's recovery actions (docs/design/ui.md "Recovery actions"), for managers: in a download's Inspect/Details,
// Remove, Remove + blocklist + search again, and Retry (route the failed release again, or import a finished
// download now); on a history row about a failed download, Retry; on a blocked release, Unblock (optionally
// searching again). Removing ends Theatarr's record only: the torrent at the debrid provider is shared and stays.
import { RotateCcw, ShieldOff, Trash2, Undo2 } from "lucide-react";
import { useState } from "react";
import type { ActivityBlocked, ActivityDownload, ActivityEvent } from "@/api/activity";
import { usePerms } from "@/api/library";
import { useRemoveDownload, useRetryDownload, useUnblock } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { failedDownloadId, removeDownloadScope, retryKind, retryScope, unblockScope, type RemoveMode } from "@/lib/ops";

type Open = RemoveMode | "retry" | null;

/** The actions row in a download's open panel. */
export function DownloadActions({ d }: { d: ActivityDownload }) {
  const perms = usePerms();
  const [open, setOpen] = useState<Open>(null);
  const remove = useRemoveDownload();
  const retry = useRetryDownload();
  if (!perms.manage) return null;
  const close = () => {
    setOpen(null);
    remove.reset();
    retry.reset();
  };
  const subject = <span className="font-mono">{d.release || d.title.name}</span>;
  const kind = retryKind(d.state);
  return (
    <div className="mt-3 flex flex-wrap gap-2" data-testid="download-actions">
      {kind ? (
        <Button size="xs" variant="outline" onClick={() => setOpen("retry")} data-testid="download-retry">
          <RotateCcw /> {kind === "import" ? "Import now" : "Retry"}
        </Button>
      ) : null}
      <Button size="xs" variant="outline" onClick={() => setOpen("remove")} data-testid="download-remove">
        <Trash2 /> {d.state === "failed" ? "Dismiss" : "Remove"}
      </Button>
      <Button size="xs" variant="outline" onClick={() => setOpen("block")} disabled={!d.info_hash} data-testid="download-block">
        <ShieldOff /> Remove, blocklist and search
      </Button>
      {open === "remove" || open === "block" ? (
        <ConfirmDialog
          scope={removeDownloadScope(d, open, d.version)}
          subject={subject}
          busy={remove.isPending}
          error={remove.error}
          done={remove.data?.message}
          onClose={close}
          onConfirm={() => remove.mutate({ id: d.id, titleId: d.title.id, blocklist: open === "block", search: open === "block" })}
          testid="remove-download-dialog"
        />
      ) : null}
      {open === "retry" ? (
        <ConfirmDialog
          scope={retryScope(d.state, d.version)}
          subject={subject}
          danger={false}
          busy={retry.isPending}
          error={retry.error}
          done={retry.data?.message}
          doneWarning={importFellShort(retry.data?.outcome)}
          onClose={close}
          onConfirm={() => retry.mutate({ id: d.id, titleId: d.title.id })}
          testid="retry-download-dialog"
        />
      ) : null}
    </div>
  );
}

/** An import now that did not link every file: its answer is shown as a warning, not a success. */
function importFellShort(outcome?: string): boolean {
  return !!outcome && outcome !== "imported";
}

/** Retry on a history row about a failed download (journal id dl:<id>:fail). */
export function HistoryRetry({ e }: { e: ActivityEvent }) {
  const perms = usePerms();
  const [open, setOpen] = useState(false);
  const retry = useRetryDownload();
  const id = failedDownloadId(e.id);
  if (!perms.manage || !id) return null;
  const copy = e.version ?? "this";
  return (
    <>
      <Button size="xs" variant="ghost" onClick={() => setOpen(true)} data-testid="history-retry">
        <RotateCcw /> Retry
      </Button>
      {open ? (
        <ConfirmDialog
          scope={retryScope("failed", copy)}
          subject={<span className="font-mono">{e.release || e.title.name}</span>}
          danger={false}
          busy={retry.isPending}
          error={retry.error}
          done={retry.data?.message}
          doneWarning={importFellShort(retry.data?.outcome)}
          onClose={() => {
            setOpen(false);
            retry.reset();
          }}
          onConfirm={() => retry.mutate({ id, titleId: e.title.id })}
          testid="retry-download-dialog"
        />
      ) : null}
    </>
  );
}

/** Unblock on a blocked release, with the choice to search the copy again. */
export function UnblockAction({ b }: { b: ActivityBlocked }) {
  const perms = usePerms();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState(false);
  const unblock = useUnblock();
  if (!perms.manage) return null;
  return (
    <div className="mt-2">
      <Button size="xs" variant="outline" onClick={() => setOpen(true)} data-testid="blocked-unblock">
        <Undo2 /> Unblock
      </Button>
      {open ? (
        <ConfirmDialog
          scope={unblockScope(b.release, b.version, search)}
          subject={<span className="font-mono">{b.release}</span>}
          danger={false}
          busy={unblock.isPending}
          error={unblock.error}
          done={unblock.data?.message}
          onClose={() => {
            setOpen(false);
            setSearch(false);
            unblock.reset();
          }}
          onConfirm={() => unblock.mutate({ version: b.version, info_hash: b.info_hash, search, titleId: b.title.id })}
          testid="unblock-dialog"
        >
          {unblock.data ? null : (
            <Choice checked={search} onChange={setSearch} testid="unblock-search">
              Search for the {b.version} library again now
            </Choice>
          )}
        </ConfirmDialog>
      ) : null}
    </div>
  );
}
