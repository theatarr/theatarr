// Grab for interactive search (docs/design/ui.md "Interactive search" and "Recovery actions"): the confirmation for a
// release of a live search that asks first (release-grid.tsx: a rejected release, one that would replace a
// better-scored file, or one the server rejected at grab time). It names the release and the library; the server
// decides the release again, and a release it rejects (then or now) is grabbed only after "Grab anyway". A grab the
// server refused late opens this with those reasons (late), so one confirmation sends it with the override. A grab
// the server refused only for the library's blocklist (a 409 with blocked, then or given) asks once more, and that
// confirmation sends it with unblock. The dialog cannot close while its grab is on its way.
//
// AddByHash is the "Last search" page's other way in, for admins: a magnet link or info hash no search of ours found,
// with the release name the rules judge it by. The server decides it and routes it as a grab; a release it rejects is
// added only after "Add anyway", and one on the library's blocklist the same way, with unblock. Nothing is sent
// before Add is pressed.
import { Loader2, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import type { Candidate } from "@/api/add";
import { ApiError } from "@/api/client";
import { useAddRelease, useGrab } from "@/api/ops";
import { VersionTag } from "@/components/page";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { blockedRelease, unblockGrabScope, type BlockedRelease } from "@/lib/ops";
import { blockedGrabScope, grabConfirmScope, needsConfirm } from "@/lib/release-columns";
import { useLibraryName } from "@/lib/library-names";

/** Where a grab goes: the title, the library (its key) and the library's name for the words; whole for a release of
 *  the whole-series window (decided again as that search's result). */
export type GrabTarget = { titleId: number; version: string; label?: string; whole?: boolean };

/** The 409 rejections of a grab the server now rejects, if that is how it failed. */
export function lateRejections(e: unknown): string[] | undefined {
  return e instanceof ApiError && e.status === 409 && e.detail?.length ? e.detail : undefined;
}

export function GrabDialog({ c, target, onClose, late: given, blocked: givenBlock, onSent }: { c: Candidate; target: GrabTarget; onClose: () => void; late?: string[]; blocked?: BlockedRelease; onSent?: () => void }) {
  const grab = useGrab(target.titleId);
  // A 409 with rejections: the server's second look rejects it; the next confirm overrides. One refused before the
  // dialog opened (given) counts the same. An override once sent stays on.
  const late = lateRejections(grab.error) ?? given;
  // A 409 with blocked: only the library's blocklist refuses it; the next confirm takes it off (unblock).
  const blocked = blockedRelease(grab.error) ?? (grab.error ? undefined : givenBlock);
  const scope = blocked ? blockedGrabScope(c, blocked, late) : grabConfirmScope(c, target.label ?? target.version, late);
  const override = !c.accepted || !!late || !!grab.variables?.override;
  const send = () =>
    grab.mutate({ version: target.version, info_hash: c.info_hash ?? "", override, ...(blocked ? { unblock: true } : {}), ...(target.whole ? { whole: true } : {}) }, { onSuccess: () => onSent?.() });
  const close = () => {
    // A grab on its way is not left behind: the dialog stays until it settles.
    if (grab.isPending) return;
    grab.reset();
    onClose();
  };
  return (
    <ConfirmDialog
      scope={scope}
      subject={<span className="font-mono">{c.release}</span>}
      danger={override || !!blocked || needsConfirm(c)}
      busy={grab.isPending}
      error={lateRejections(grab.error) || blocked ? undefined : grab.error}
      done={grab.data?.message}
      onClose={close}
      onConfirm={send}
      testid="grab-dialog"
    />
  );
}

/** A magnet link's display name (dn), "" when it has none. */
export function magnetName(m: string): string {
  const q = m.trim().split("?")[1];
  if (!q) return "";
  for (const part of q.split(/[&#]/)) {
    const i = part.indexOf("=");
    if (i < 0 || part.slice(0, i).toLowerCase() !== "dn") continue;
    const v = part.slice(i + 1).replace(/\+/g, " ");
    try {
      return decodeURIComponent(v).trim();
    } catch {
      return v.trim();
    }
  }
  return "";
}

/** "Add by magnet or hash" for one library of a title (admins): the link or hash, the release name (taken from the
 *  magnet's dn until typed), and Add. A rejection lists the reasons and offers "Add anyway". */
export function AddByHash({ titleId, version, label }: { titleId: number; version: string; label?: string }) {
  const nameOf = useLibraryName();
  const add = useAddRelease(titleId);
  const [link, setLink] = useState("");
  const [name, setName] = useState("");
  // The name follows the magnet's dn until the person types one.
  const [typed, setTyped] = useState(false);
  const source = link.trim();
  // A 409 with rejections to a request that did not override: the next one may. One that did override was refused
  // for another reason (the title has no copy for the library), which is shown as it is. A name that matches no
  // episode is refused at the first request, with no rejections to override.
  const rejected = add.error instanceof ApiError && add.error.status === 409 && add.error.detail?.length && !add.variables?.override ? add.error.detail : undefined;
  // A 409 with blocked: only the library's blocklist refuses it; "Add anyway" sends it again with unblock (and the
  // override it was sent with).
  const blocked = blockedRelease(add.error);
  const send = (override: boolean, unblock = false) => {
    const magnet = /^magnet:/i.test(source);
    add.mutate(
      { version, name: name.trim(), override, ...(unblock ? { unblock: true } : {}), ...(magnet ? { magnet: source } : { info_hash: source }) },
      { onSuccess: () => { setLink(""); setName(""); setTyped(false); } },
    );
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    send(false);
  };
  const edit = (change: () => void) => {
    // A changed form is a new question: the last answer no longer stands.
    if (add.isError || add.isSuccess) add.reset();
    change();
  };
  return (
    <form onSubmit={submit} className="space-y-2 border-t border-[var(--line)] pt-4" data-testid="add-by-hash">
      <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
        Add by magnet or hash <VersionTag k={version} label={label} />
      </div>
      <p className="text-xs text-[var(--ink-3)]">
        For a release the search does not find. It is judged by its name like any release and grabbed like one; its size and seeders are not known.
      </p>
      <div className="flex flex-wrap items-end gap-2">
        <label className="min-w-56 flex-1 text-xs text-[var(--ink-2)]">
          Magnet link or info hash
          <Input required value={link} disabled={add.isPending} data-testid="add-by-hash-link" onChange={(e) => edit(() => { setLink(e.target.value); if (!typed) setName(magnetName(e.target.value)); })} />
        </label>
        <label className="min-w-56 flex-1 text-xs text-[var(--ink-2)]">
          Release name
          <Input required value={name} disabled={add.isPending} placeholder="Title.2024.2160p.WEB-DL-GROUP" data-testid="add-by-hash-name" onChange={(e) => edit(() => { setName(e.target.value); setTyped(true); })} />
        </label>
        <Button type="submit" size="sm" variant="outline" disabled={add.isPending || !source || !name.trim()} data-testid="add-by-hash-add">
          {add.isPending ? <Loader2 className="animate-spin" /> : <Plus />} Add
        </Button>
      </div>
      {rejected ? (
        <div className="space-y-1.5 text-xs" role="alert" data-testid="add-by-hash-rejected">
          <p className="text-[var(--bad-ink)]">Theatarr rejects this release for {label || nameOf(version)}:</p>
          <ul className="list-disc pl-5 text-[var(--bad-ink)]">{rejected.map((r, i) => <li key={i}>{r}</li>)}</ul>
          <Button type="button" size="sm" variant="danger" disabled={add.isPending} onClick={() => send(true)} data-testid="add-by-hash-anyway">Add anyway</Button>
        </div>
      ) : blocked ? (
        <div className="space-y-1.5 text-xs" role="alert" data-testid="add-by-hash-blocked">
          <p className="text-[var(--bad-ink)]">{unblockGrabScope(blocked, "Add").does[0]}</p>
          <Button type="button" size="sm" variant="danger" disabled={add.isPending} onClick={() => send(!!add.variables?.override, true)} data-testid="add-by-hash-unblock">Add anyway</Button>
        </div>
      ) : add.error ? (
        <p className="text-xs text-[var(--bad-ink)]" role="alert" data-testid="add-by-hash-error">{errText(add.error)}</p>
      ) : add.data ? (
        <p className="text-xs text-[var(--ok-ink)]" role="status" data-testid="add-by-hash-done">{add.data.release}: {add.data.message}</p>
      ) : null}
    </form>
  );
}
