// Replace file… on the title page (docs/design/ui.md "Recovery actions"), for managers: the file rows of a movie's
// library row and of the episode window. The same operation, refusal and confirmation scope as Requests › Problems
// (replaceScope), without a report: the file is unlinked to the trash, its release is blocklisted in every library
// that loses it (a pack as a whole) and those libraries are searched again. Offered only when that search can run;
// otherwise the server's reason shows in its place.
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import type { Schemas } from "@/api/client";
import { useReplaceLink } from "@/api/ops";
import { replaceScope } from "@/api/reports";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";

export function ReplaceFile({ titleId, linkId, path, scope }: { titleId: number; linkId: number; path: string; scope?: Schemas["ReplaceScope"] }) {
  const [open, setOpen] = useState(false);
  const replace = useReplaceLink(titleId);
  if (!scope) return null;
  const name = path.split("/").pop() ?? path;
  if (!scope.replace) {
    return scope.why ? <p className="text-xs text-[var(--ink-3)]" data-testid="replace-why">Replace file: {scope.why}.</p> : null;
  }
  return (
    <>
      <Button size="xs" variant="ghost" onClick={() => setOpen(true)} data-testid="link-replace">
        <RefreshCw /> Replace file…
      </Button>
      {open ? (
        <ConfirmDialog
          testid="replace-confirm"
          subject={name}
          scope={replaceScope(scope, false)}
          busy={replace.isPending}
          error={replace.error}
          done={replace.data?.message}
          doneWarning={replace.data ? !replace.data.search?.started : false}
          onClose={() => {
            setOpen(false);
            replace.reset();
          }}
          onConfirm={() => replace.mutate(linkId)}
        />
      ) : null}
    </>
  );
}
