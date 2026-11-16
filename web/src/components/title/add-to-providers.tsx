// "Add to other providers" (docs/design/torrents.md, docs/design/ui.md): the button on a file Theatarr holds whose
// torrent is not on every provider with a key (addTo, from the server), on the title page's movie file, in the episode
// window and on an Admin › Torrents row. No confirmation: nothing is lost by it. While the copy runs the button spins;
// then one line under it says how each provider ended, until the page is left. The file's provider tags follow once it
// ends (the title, episode and torrents queries are asked again).
import { CopyPlus, Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useCopyState, useStartCopy } from "@/api/torrents";
import { qk } from "@/api/query-keys";
import { Button } from "@/components/ui/button";
import { errText } from "@/components/ui/confirm-dialog";
import { addToTooltip, copyLine, copyResults } from "@/lib/add-to-providers";

export function AddToProviders({ hash, addTo, pack, testid = "add-to-providers" }: { hash?: string | null; addTo?: readonly string[] | null; pack?: boolean | "maybe"; testid?: string }) {
  const [since, setSince] = useState<string | null>(null);
  const start = useStartCopy();
  const state = useCopyState(hash ?? "", !!hash && !!since);
  const qc = useQueryClient();
  const running = start.isPending || (!!since && (!state.data || state.data.running));
  const done = !!since && !!state.data && !state.data.running;
  useEffect(() => {
    if (!done) return;
    void qc.invalidateQueries({ queryKey: ["title"] });
    void qc.invalidateQueries({ queryKey: qk.torrents.report });
  }, [done, qc]);
  if (!hash || (!addTo?.length && !since)) return null;
  const line = done ? copyLine(copyResults(state.data, since)) : "";
  return (
    <span className="flex flex-col items-start gap-1">
      <Button
        size="sm"
        variant="outline"
        disabled={running}
        title={addToTooltip(pack)}
        onClick={() =>
          start.mutate(hash, {
            onSuccess: (r) => {
              // A copy before this one left its answer cached: ask afresh.
              qc.removeQueries({ queryKey: qk.torrents.copy(hash) });
              setSince(r.since);
            },
          })
        }
        data-testid={testid}
      >
        {running ? <Loader2 className="animate-spin" /> : <CopyPlus />} Add to other providers
      </Button>
      {line ? <span className="text-xs text-[var(--ink-2)] [overflow-wrap:anywhere]" data-testid={`${testid}-result`}>{line}</span> : null}
      {start.error ? <span className="text-xs text-[var(--bad-ink)]" role="alert">{errText(start.error)}</span> : null}
      {state.error ? <span className="text-xs text-[var(--bad-ink)]" role="alert">{errText(state.error)}</span> : null}
    </span>
  );
}
