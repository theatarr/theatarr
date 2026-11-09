// Grab for the releases panel and the episode overview's releases (docs/design/ui.md "Recovery actions"): a chosen
// release of a live search goes to the router for the copy. The confirmation names the release and the copy; the
// server decides the release again, and a release it rejects (then or now) is grabbed only after "Grab anyway".
// Managers only: callers pass grab only for a live answer the server marked grabbable (Releases.grab).
import { Download } from "lucide-react";
import { useState } from "react";
import type { Candidate } from "@/api/add";
import { ApiError } from "@/api/client";
import { useGrab } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { grabScope } from "@/lib/ops";

export type GrabTarget = { titleId: number; version: string; label?: string };

export function GrabButton({ c, target }: { c: Candidate; target: GrabTarget }) {
  const [open, setOpen] = useState(false);
  const grab = useGrab(target.titleId);
  // A 409 with rejections: the server's second look rejects it; the next confirm overrides.
  const late = grab.error instanceof ApiError && grab.error.status === 409 && grab.error.detail?.length ? grab.error.detail : undefined;
  const scope = grabScope(c, target.label ?? target.version, late);
  const override = !c.accepted || !!late;
  const close = () => {
    setOpen(false);
    grab.reset();
  };
  return (
    <>
      <Button size="xs" variant={c.accepted ? "outline" : "ghost"} onClick={() => setOpen(true)} disabled={!c.info_hash} title={c.info_hash ? "Send this release to the router" : "No info hash"} data-testid="release-grab">
        <Download /> Grab
      </Button>
      {open ? (
        <ConfirmDialog
          scope={scope}
          subject={<span className="font-mono">{c.release}</span>}
          danger={override}
          busy={grab.isPending}
          error={late ? undefined : grab.error}
          done={grab.data?.message}
          onClose={close}
          onConfirm={() => grab.mutate({ version: target.version, info_hash: c.info_hash ?? "", override })}
          testid="grab-dialog"
        />
      ) : null}
    </>
  );
}
