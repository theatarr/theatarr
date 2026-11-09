// Removal on the title page (docs/design/ui.md "Recovery actions"), for managers: Remove a copy (in the copy's Details),
// Unlink one of its library files, and Remove the whole title (About). Each confirmation spells out the scope the
// server reports (copies, links, downloads in progress, an arr that still manages a copy) and what never happens:
// a debrid torrent or a file on the mount deleted. An unlink moves only Theatarr's own symlink to the trash.
import { FileX2, Loader2, Trash2 } from "lucide-react";
import { useState } from "react";
import type { Version } from "@/api/library";
import { useRemovalScope, useRemove, useUnlink } from "@/api/ops";
import { Mono } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Modal } from "@/components/ui/modal";
import { removalLinks, removalScope, unlinkable, unlinkScope } from "@/lib/ops";

/** The removal confirmation: the server's scope for versions (none: the whole title), with the unlink choice. */
function RemoveDialog({ titleId, versions, onClose }: { titleId: number; versions?: string[]; onClose: () => void }) {
  const q = useRemovalScope(titleId, versions, true);
  const remove = useRemove(titleId);
  const [unlink, setUnlink] = useState(false);
  if (!q.data) {
    return (
      <Modal label="Remove" onClose={onClose} testid="remove-dialog" className="z-[85] flex items-center justify-center p-6"
        backdropClassName="absolute inset-0 bg-[var(--scrim)]"
        panelClassName="relative rounded-[8px] border border-[var(--line)] bg-[var(--surface)] p-4 text-sm">
        {q.error ? <p className="text-[var(--bad-ink)]">{(q.error as Error).message}</p> : <p className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Checking what this removes…</p>}
        <div className="mt-3 flex justify-end"><Button size="sm" variant="ghost" onClick={onClose}>Close</Button></div>
      </Modal>
    );
  }
  const s = q.data;
  const links = removalLinks(s);
  // Nothing left to hide: only the links of copies removed before, which a removal with unlink moves.
  const leftoverOnly = s.copies.length === 0;
  const r = remove.data;
  const listed = (label: string, ls: { path: string; reason: string }[]) =>
    ls.length ? ` ${label}: ${ls.map((k) => `${k.path} (${k.reason})`).join("; ")}` : "";
  const done = r ? r.message + listed("Kept", r.kept) + listed("Failed", r.failed ?? []) : undefined;
  return (
    <ConfirmDialog
      scope={removalScope(s, unlink || leftoverOnly)}
      subject={s.name}
      busy={remove.isPending}
      error={remove.error}
      done={done}
      onClose={onClose}
      onConfirm={() =>
        remove.mutate({
          versions: s.title ? [] : [...s.copies, ...(s.leftover ?? [])].map((c) => c.key),
          unlink: unlink || leftoverOnly,
        })
      }
      testid="remove-dialog"
    >
      {links > 0 && !r && !leftoverOnly ? (
        <Choice checked={unlink} onChange={setUnlink} testid="remove-unlink">
          Also move the {links === 1 ? "library link" : `${links} library links`} to Theatarr's trash (the files on the mount stay)
        </Choice>
      ) : null}
    </ConfirmDialog>
  );
}

/** Remove this library, and unlink its files one by one: shown in the copy's Details. */
export function CopyActions({ v, label }: { v: Version; label: string }) {
  const titleId = v.title_id ?? 0;
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState<{ id: number; path: string } | null>(null);
  const unlink = useUnlink(titleId);
  const links = unlinkable(v.links);
  if (!titleId || !v.key) return null;
  return (
    <div className="space-y-2" data-testid="copy-actions">
      {links.length > 0 ? (
        <div>
          <div className="mb-2 text-xs font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Library links</div>
          <ul className="space-y-1.5">
            {links.map((l) => (
              <li key={l.id} className="flex items-start gap-2">
                <Mono className="min-w-0 flex-1 break-all text-muted-foreground">{l.path}</Mono>
                <Button size="xs" variant="ghost" onClick={() => setPath({ id: l.id ?? 0, path: l.path ?? "" })} data-testid="link-unlink">
                  <FileX2 /> Unlink
                </Button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <Button size="xs" variant="outline" onClick={() => setOpen(true)} data-testid={`copy-remove-${v.key}`}>
        <Trash2 /> Remove this library
      </Button>
      {open ? <RemoveDialog titleId={titleId} versions={[v.key]} onClose={() => setOpen(false)} /> : null}
      {path ? (
        <ConfirmDialog
          scope={unlinkScope(path.path, label)}
          subject={label}
          busy={unlink.isPending}
          error={unlink.error}
          done={unlink.data?.message}
          onClose={() => {
            setPath(null);
            unlink.reset();
          }}
          onConfirm={() => unlink.mutate(path.id)}
          testid="unlink-dialog"
        />
      ) : null}
    </div>
  );
}

/** Remove the title from the library: About, managers only (the caller checks). */
export function TitleRemove({ titleId }: { titleId: number }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)} data-testid="title-remove">
        <Trash2 /> Remove title
      </Button>
      {open ? <RemoveDialog titleId={titleId} onClose={() => setOpen(false)} /> : null}
    </>
  );
}
