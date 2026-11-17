// Activity's recovery actions (docs/design/ui.md "Recovery actions"), for managers: on a Queue row, Retry (route the
// failed release again, or import a finished download now) and Remove, whose confirmation can also blocklist the
// release and search again; on a history row about a failed download, Retry; on a blocked release, Unblock
// (optionally searching again). Removing ends Theatarr's record only: the torrent at the debrid provider is shared
// and stays.
import { RotateCcw, Undo2 } from "lucide-react";
import { useState } from "react";
import type { ActivityBlocked, ActivityDownload, ActivityEvent } from "@/api/activity";
import { usePerms } from "@/api/library";
import { useRemoveDownload, useRetryDownload, useUnblock } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { blockedRelease, endedByRemoval, failedDownloadId, removeDownloadScope, retryKind, retryScope, unblockGrabScope, unblockScope, type RemoveMode } from "@/lib/ops";
import { useLibraryName } from "@/lib/library-names";

type Open = "remove" | "retry" | null;

/** A Queue row's actions after Details: Retry or Import now when it applies, and Remove (Dismiss for a failed one). */
export function DownloadActions({ d }: { d: ActivityDownload }) {
  const perms = usePerms();
  const [open, setOpen] = useState<Open>(null);
  const [block, setBlock] = useState(false);
  const remove = useRemoveDownload();
  const retry = useRetryDownload();
  const nameOf = useLibraryName();
  if (!perms.manage) return null;
  const close = () => {
    setOpen(null);
    setBlock(false);
    remove.reset();
    retry.reset();
  };
  const mode: RemoveMode = block ? "block" : "remove";
  const subject = <span className="font-mono">{d.release || d.title.name}</span>;
  const kind = retryKind(d.state);
  // A 409 with blocked: only the library's blocklist refuses the retry; the next confirm takes it off.
  const retryBlocked = blockedRelease(retry.error);
  return (
    <span className="inline-flex gap-1" data-testid="download-actions">
      {kind ? (
        <Button size="xs" variant="outline" onClick={() => setOpen("retry")} data-testid="download-retry" className="max-sm:h-9 max-sm:px-3">
          {kind === "import" ? "Import now" : "Retry"}
        </Button>
      ) : null}
      <Button size="xs" variant="outline" onClick={() => setOpen("remove")} data-testid="download-remove" className="max-sm:h-9 max-sm:px-3">
        {d.state === "failed" ? "Dismiss" : "Remove"}
      </Button>
      {open === "remove" ? (
        <ConfirmDialog
          scope={removeDownloadScope(d, mode, nameOf(d.version))}
          subject={subject}
          busy={remove.isPending}
          error={remove.error}
          done={remove.data?.message}
          onClose={close}
          onConfirm={() => remove.mutate({ id: d.id, titleId: d.title.id, blocklist: block, search: block })}
          testid="remove-download-dialog"
        >
          {remove.data ? null : (
            <Choice checked={block} onChange={setBlock} disabled={!d.info_hash} testid="download-block">
              Also blocklist this release and search again
            </Choice>
          )}
        </ConfirmDialog>
      ) : null}
      {open === "retry" ? (
        <ConfirmDialog
          scope={retryBlocked ? unblockGrabScope(retryBlocked, "Retry") : retryScope(d.state, nameOf(d.version))}
          subject={subject}
          danger={!!retryBlocked}
          busy={retry.isPending}
          error={retryBlocked ? undefined : retry.error}
          done={retry.data?.message}
          doneWarning={importFellShort(retry.data?.outcome)}
          onClose={close}
          onConfirm={() => retry.mutate({ id: d.id, titleId: d.title.id, unblock: !!retryBlocked })}
          testid="retry-download-dialog"
        />
      ) : null}
    </span>
  );
}

/** An import now that did not link every file: its answer is shown as a warning, not a success. */
function importFellShort(outcome?: string): boolean {
  return !!outcome && outcome !== "imported";
}

/** Retry on a history row about a failed download (journal id dl:<id>:fail); none when a removal ended it. */
export function HistoryRetry({ e }: { e: ActivityEvent }) {
  const perms = usePerms();
  const [open, setOpen] = useState(false);
  const retry = useRetryDownload();
  const id = failedDownloadId(e.id);
  const nameOf = useLibraryName();
  if (!perms.manage || !id || endedByRemoval(e.reason)) return null;
  const copy = e.version ? nameOf(e.version) : "this";
  const blocked = blockedRelease(retry.error);
  return (
    <>
      <Button size="xs" variant="ghost" onClick={() => setOpen(true)} data-testid="history-retry">
        <RotateCcw /> Retry
      </Button>
      {open ? (
        <ConfirmDialog
          scope={blocked ? unblockGrabScope(blocked, "Retry") : retryScope("failed", copy)}
          subject={<span className="font-mono">{e.release || e.title.name}</span>}
          danger={!!blocked}
          busy={retry.isPending}
          error={blocked ? undefined : retry.error}
          done={retry.data?.message}
          doneWarning={importFellShort(retry.data?.outcome)}
          onClose={() => {
            setOpen(false);
            retry.reset();
          }}
          onConfirm={() => retry.mutate({ id, titleId: e.title.id, unblock: !!blocked })}
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
  const nameOf = useLibraryName();
  if (!perms.manage) return null;
  return (
    <>
      <Button size="xs" variant="outline" onClick={() => setOpen(true)} data-testid="blocked-unblock">
        <Undo2 /> Unblock
      </Button>
      {open ? (
        <ConfirmDialog
          scope={unblockScope(b.release, nameOf(b.version), search)}
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
              Search for the {nameOf(b.version)} library again now
            </Choice>
          )}
        </ConfirmDialog>
      ) : null}
    </>
  );
}
