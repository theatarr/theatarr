// Grab for the releases panel and the episode overview's releases (docs/design/ui.md "Recovery actions"): a chosen
// release of a live search goes to the router for the copy. The confirmation names the release and the copy; the
// server decides the release again, and a release it rejects (then or now) is grabbed only after "Grab anyway".
// Managers only: callers pass grab only for a live answer the server marked grabbable (Releases.grab). A row of a
// stored answer has no live search behind it: its Grab hands the row to the panel (pick), whose GrabDialog runs the
// search first and grabs the row's hash from that answer, asking again when that answer rejects it. The dialog
// cannot close while its grab is on its way.
import { Download } from "lucide-react";
import { useEffect, useState } from "react";
import type { Candidate, Releases } from "@/api/add";
import { ApiError } from "@/api/client";
import { useGrab } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
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
