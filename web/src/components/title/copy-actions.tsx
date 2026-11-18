// Removal on the title page (docs/design/ui.md "Recovery actions"), for managers: Remove from this library (in the
// library row's Details), Unlink one of its library files, Replace a movie's file (replace-file.tsx) or edit its facts (edit-facts.tsx), Delete the files a library the title was removed from kept
// (the line under the Libraries table, and the kept-files line of a title in no library), and Remove the whole title
// (the action bar). Each confirmation spells out the scope the server reports (copies, links, downloads in progress)
// and what stays. The removal's two choices are on by default: "Delete the files" (Theatarr's own symlinks go to its
// trash) and "Delete it from the debrid providers" (after a backup, the torrents no other title or library in
// Theatarr uses; only with the files deleted). Removing the whole title also offers "Also keep lists from adding it to
// any other library" (off by default: the title goes on the list exclusions; lists never restore the removed libraries
// either way). An unlink moves only Theatarr's own symlink to the trash.
import { FileX2, Loader2, Trash2 } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router";
import type { Version } from "@/api/library";
import { useRemovalScope, useRemove, useUnlink } from "@/api/ops";
import { Mono } from "@/components/page";
import { EditFacts } from "@/components/title/edit-facts";
import { ReplaceFile } from "@/components/title/replace-file";
import { toast } from "@/components/toaster";
import { Button } from "@/components/ui/button";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Modal } from "@/components/ui/modal";
import { listToReturnTo } from "@/lib/last-list";
import { leftoverLinks, leftoverOnly, REMOVAL_DEBRID_NEEDS_FILES, REMOVAL_EXCLUDE, removalScope, unlinkable, unlinkScope } from "@/lib/ops";

/** The removal confirmation: the server's scope for versions (none: the whole title), with the files choice. Once
 *  done, the outcome is a notice that opens the title's History (the Removed event, with the debrid clean-up's outcome
 *  once it ran); a removal that leaves the title in no library goes back to the list the person came from (the last
 *  list opened, else the list of the title's kind), any other closes the dialog. gone: the title is in no library (its
 *  kept files' Delete them). */
export function RemoveDialog({ titleId, versions, onClose, kind, anime, gone = false }: { titleId: number; versions?: string[]; onClose: () => void; kind?: string; anime?: boolean; gone?: boolean }) {
  const remove = useRemove(titleId);
  const navigate = useNavigate();
  // The scope the dialog was opened with stays: once the removal runs, the title's refresh must not read it again
  // (it would describe what is left, or fail when nothing is).
  // Set in the confirm click itself (a discrete event renders before the request answers), so no refresh refetches it.
  const [started, setStarted] = useState(false);
  const q = useRemovalScope(titleId, versions, !started);
  const [files, setFiles] = useState(true);
  const [debrid, setDebrid] = useState(true);
  const [exclude, setExclude] = useState(false);
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
  // Deleting the kept files of removed libraries is that dialog's point, not a choice: only the debrid box is offered.
  const only = leftoverOnly(s);
  // A removal that takes the title's last copies (Remove title, or a library whose libraries in sync go with it)
  // offers to keep lists from adding it again; one that leaves a copy does not.
  const whole = s.title && !only;
  return (
    <ConfirmDialog
      scope={removalScope(s, files, debrid, gone)}
      subject={s.name}
      busy={remove.isPending}
      error={remove.error}
      onClose={onClose}
      onConfirm={() => {
        setStarted(true);
        remove.mutate(
          {
            // A removal asked for named libraries (a row, a leftover line) stays scoped to them even when they are the
            // title's last; none (the action bar) is the whole title, with the files of libraries removed earlier.
            versions: versions ?? [],
            unlink: files,
            debrid: files && debrid,
            ...(whole && exclude ? { exclude: true } : {}),
          },
          {
            onSuccess: (r) => {
              // The first reason a file stayed; History lists the outcome per library.
              const first = [...(r.failed ?? []), ...r.kept][0];
              toast({
                tone: first ? "warn" : "ok",
                title: r.title ? `Removed ${s.name}` : s.name,
                body: `${r.message}${first ? ` Left in place because: ${first.reason}.` : ""} Open its History for the details.`,
                to: `/title/${titleId}?tab=history`,
                // Long enough to read it and follow the link.
                duration: 10_000,
              });
              onClose();
              if (r.title) navigate(listToReturnTo(kind, anime));
            },
          },
        );
      }}
      testid="remove-dialog"
    >
      {only ? null : (
        <Choice checked={files} onChange={setFiles} testid="remove-files">
          Delete the files
        </Choice>
      )}
      <Choice checked={files && debrid} onChange={setDebrid} disabled={!files} testid="remove-debrid">
        Delete it from the debrid providers
      </Choice>
      {!files ? <p className="pl-6 text-xs text-[var(--ink-3)]" data-testid="remove-debrid-why">{REMOVAL_DEBRID_NEEDS_FILES}.</p> : null}
      {whole ? (
        <Choice checked={exclude} onChange={setExclude} testid="remove-exclude">
          {REMOVAL_EXCLUDE}
        </Choice>
      ) : null}
    </ConfirmDialog>
  );
}

/** Unlink the library's files one by one, and Remove from this library: the library row's Details. onRemove opens the
 *  removal dialog outside the row (the Libraries table), so its result stays on screen after the row goes. */
/** Links grouped by their folder (a series' season folder), in path order; one group without a name when they share
 *  a single folder. */
export function linkGroups<T extends { path?: string }>(links: readonly T[]): { folder: string; links: T[] }[] {
  const groups = new Map<string, T[]>();
  for (const l of [...links].sort((a, b) => (a.path ?? "").localeCompare(b.path ?? ""))) {
    const parts = (l.path ?? "").split("/");
    const folder = parts.length > 1 ? parts[parts.length - 2] : "";
    groups.set(folder, [...(groups.get(folder) ?? []), l]);
  }
  const out = [...groups].map(([folder, ls]) => ({ folder, links: ls }));
  return out.length === 1 ? [{ folder: "", links: out[0].links }] : out;
}

export function CopyActions({ v, label, onRemove, movie = false }: { v: Version; label: string; onRemove: () => void; movie?: boolean }) {
  const titleId = v.title_id ?? 0;
  const [path, setPath] = useState<{ id: number; path: string } | null>(null);
  const unlink = useUnlink(titleId);
  const links = unlinkable(v.links);
  if (!titleId || !v.key) return null;
  return (
    <div className="space-y-2" data-testid="copy-actions">
      {links.length > 0 ? (
        // Folded, by folder (a series' season): each file's name, then its full path, selectable.
        <details className="group" data-testid="library-files">
          <summary className="cursor-pointer text-xs font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Library files ({links.length})</summary>
          <div className="mt-2 space-y-2">
            {linkGroups(links).map((g) => (
              <div key={g.folder}>
                {g.folder ? <div className="mb-1 text-xs font-medium text-[var(--ink-2)] [overflow-wrap:anywhere]">{g.folder}</div> : null}
                <ul className="space-y-1">
                  {g.links.map((l) => (
                    <li key={l.id} className="flex items-start gap-2">
                      <span className="min-w-0 flex-1">
                        <Mono className="block text-[var(--ink)] [overflow-wrap:anywhere]">{(l.path ?? "").split("/").pop()}</Mono>
                        <span className="block select-text" data-testid="library-file-path"><Mono className="text-[11.5px] text-muted-foreground [overflow-wrap:anywhere]">{l.path}</Mono></span>
                        {movie ? (
                          <span className="mt-1 flex flex-wrap items-center gap-1">
                            <ReplaceFile titleId={titleId} linkId={l.id ?? 0} path={l.path ?? ""} scope={l.replace_scope} />
                            <EditFacts titleId={titleId} linkId={l.id ?? 0} path={l.path ?? ""} />
                          </span>
                        ) : null}
                      </span>
                      <Button size="xs" variant="ghost" onClick={() => setPath({ id: l.id ?? 0, path: l.path ?? "" })} data-testid="link-unlink">
                        <FileX2 /> Unlink
                      </Button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        </details>
      ) : null}
      <Button size="xs" variant="outline" onClick={onRemove} data-testid={`copy-remove-${v.key}`}>
        <Trash2 /> Remove from this library
      </Button>
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

/** Delete files…: the links libraries the title was removed from still have, deleted through the removal dialog (the
 *  server's leftover case), their debrid torrents cleaned up with the same guard. label is the link's words (Delete
 *  files…, or Delete them). gone: the title is in no library. */
export function LeftoverLinks({ titleId, versions, label = "Delete files…", gone = false }: { titleId: number; versions: string[]; label?: string; gone?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className="font-medium text-primary hover:underline" onClick={() => setOpen(true)} data-testid={`leftover-remove-${versions.join(",")}`}>
        {label}
      </button>
      {open ? <RemoveDialog titleId={titleId} versions={versions} gone={gone} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

/** A title with no library copy left whose removed libraries still have library links: "N files kept from before it
 *  was removed", and for managers Delete them (LeftoverLinks for those libraries). */
export function KeptFiles({ titleId, versions, manage }: { titleId: number; versions: Version[]; manage: boolean }) {
  const kept = versions.filter((v) => v.removed && v.key && leftoverLinks(v.links).length > 0);
  // Each file once: two libraries kept in sync can share one.
  const n = new Set(kept.flatMap((v) => leftoverLinks(v.links).map((l) => l.id))).size;
  if (n === 0) return null;
  return (
    <div className="space-y-1 text-sm text-[var(--ink-2)]">
      <p data-testid="kept-files">
        {n === 1 ? "1 file" : `${n} files`} kept from before it was removed
        {manage ? <>: <LeftoverLinks titleId={titleId} versions={kept.map((v) => v.key ?? "")} label={n === 1 ? "Delete it" : "Delete them"} gone /></> : "."}
      </p>
    </div>
  );
}
