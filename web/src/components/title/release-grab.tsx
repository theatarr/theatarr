// Grab for the releases panel and the episode overview's releases (docs/design/ui.md "Recovery actions"): a chosen
// release of a live search goes to the router for the copy. The confirmation names the release and the copy; the
// server decides the release again, and a release it rejects (then or now) is grabbed only after "Grab anyway".
// Managers only: callers pass grab only for a live answer the server marked grabbable (Releases.grab). A row of a
// stored answer has no live search behind it: its Grab hands the row to the panel (pick), whose GrabDialog runs the
// search first and grabs the row's hash from that answer, asking again when that answer rejects it. The dialog
// cannot close while its grab is on its way.
//
// AddByHash is the panel's other way in, for admins: a magnet link or info hash no search of ours found, with the
// release name the rules judge it by. The server decides it and routes it as a grab; a release it rejects is added
// only after "Add anyway". Nothing is sent before Add is pressed.
import { Download, Loader2, Plus } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import type { Candidate, Releases } from "@/api/add";
import { ApiError } from "@/api/client";
import { useAddRelease, useGrab } from "@/api/ops";
import { VersionTag } from "@/components/page";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { grabScope } from "@/lib/ops";

export type GrabTarget = { titleId: number; version: string; label?: string; pick?: (c: Candidate) => void };

export function GrabButton({ c, target }: { c: Candidate; target: GrabTarget }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="xs" variant={c.accepted ? "outline" : "ghost"} onClick={() => (target.pick ? target.pick(c) : setOpen(true))} disabled={!c.info_hash} title={c.info_hash ? "Send this release to the router" : "No info hash"} data-testid="release-grab">
        <Download /> Grab
      </Button>
      {open ? <GrabDialog c={c} target={target} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

/** A search to run before the grab, for a release from a stored answer: search starts it, answer is its result once
 *  in, failed says it was blocked or failed (the panel shows why), at when the latest result came in (only one newer
 *  than the confirm counts). A release the answer lacks goes to gone, ungrabbed. */
export type FreshSearch = { search: () => void; answer?: Releases; failed: boolean; at: number; gone: (c: Candidate) => void };

export function GrabDialog({ c, target, onClose, fresh }: { c: Candidate; target: GrabTarget; onClose: () => void; fresh?: FreshSearch }) {
  const grab = useGrab(target.titleId);
  // A search running for the grab, and the result time it waits to pass.
  const [armed, setArmed] = useState<{ after: number }>();
  // The release as the fresh search decided it, when that rejects it: it is confirmed again with those reasons.
  const [now, setNow] = useState<Candidate>();
  const shown = now ?? c;
  // A 409 with rejections: the server's second look rejects it; the next confirm overrides.
  const late = grab.error instanceof ApiError && grab.error.status === 409 && grab.error.detail?.length ? grab.error.detail : undefined;
  const scope = grabScope(shown, target.label ?? target.version, late);
  const override = !shown.accepted || !!late;
  const send = (o: boolean) => grab.mutate({ version: target.version, info_hash: c.info_hash ?? "", override: o });
  const close = () => {
    // A grab on its way is not left behind: the dialog stays until it settles.
    if (grab.isPending) return;
    grab.reset();
    onClose();
  };
  // Once the search is in: grab the hash if the answer still offers it.
  const answer = fresh?.answer;
  const failed = !!fresh?.failed;
  const at = fresh?.at ?? 0;
  useEffect(() => {
    if (!armed || !fresh || fresh.at <= armed.after) return;
    if (fresh.failed) {
      setArmed(undefined);
      onClose();
    } else if (fresh.answer) {
      setArmed(undefined);
      const hash = c.info_hash?.toLowerCase();
      const fc = fresh.answer.candidates?.find((x) => x.info_hash?.toLowerCase() === hash);
      if (!fc) {
        fresh.gone(c);
        onClose();
      } else if (fc.accepted) send(false);
      // Rejected now: shown with the fresh reasons, grabbed only on "Grab anyway".
      else setNow(fc);
    }
    // Only the search's result moves this on; the callbacks are fresh each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [armed, answer, failed, at]);
  return (
    <ConfirmDialog
      scope={scope}
      subject={<span className="font-mono">{shown.release}</span>}
      danger={override}
      busy={grab.isPending || !!armed}
      error={late ? undefined : grab.error}
      done={grab.data?.message}
      onClose={close}
      onConfirm={() => {
        if (fresh && !fresh.answer) {
          setArmed({ after: fresh.at });
          fresh.search();
        } else send(override);
      }}
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
export function AddByHash({ titleId, version }: { titleId: number; version: string }) {
  const add = useAddRelease(titleId);
  const [link, setLink] = useState("");
  const [name, setName] = useState("");
  // The name follows the magnet's dn until the person types one.
  const [typed, setTyped] = useState(false);
  const source = link.trim();
  // A 409 with rejections to a request that did not override: the next one may. One that did override was refused
  // for another reason (no episode of the title matches the name), which is shown as it is.
  const rejected = add.error instanceof ApiError && add.error.status === 409 && add.error.detail?.length && !add.variables?.override ? add.error.detail : undefined;
  const send = (override: boolean) => {
    const magnet = /^magnet:/i.test(source);
    add.mutate(
      { version, name: name.trim(), override, ...(magnet ? { magnet: source } : { info_hash: source }) },
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
        Add by magnet or hash <VersionTag k={version} />
      </div>
      <p className="text-xs text-[var(--ink-3)]">
        For a release the search does not find. It is judged by its name like any release and goes to the router like a grab; its size and seeders are not known.
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
          <p className="text-[var(--bad-ink)]">Theatarr rejects this release for {version}:</p>
          <ul className="list-disc pl-5 text-[var(--bad-ink)]">{rejected.map((r, i) => <li key={i}>{r}</li>)}</ul>
          <Button type="button" size="sm" variant="danger" disabled={add.isPending} onClick={() => send(true)} data-testid="add-by-hash-anyway">Add anyway</Button>
        </div>
      ) : add.error ? (
        <p className="text-xs text-[var(--bad-ink)]" role="alert" data-testid="add-by-hash-error">{errText(add.error)}</p>
      ) : add.data ? (
        <p className="text-xs text-[var(--ok-ink)]" role="status" data-testid="add-by-hash-done">{add.data.release}: {add.data.message}</p>
      ) : null}
    </form>
  );
}
