// "Add to other providers" (docs/design/router.md "Copies to other providers"): the action's button with its answer,
// and the "Other providers" line that says how the copies ended where "Cached on" does not show them.
import { CopyPlus, Loader2 } from "lucide-react";
import type { UseMutationResult } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { errText } from "@/components/ui/confirm-dialog";
import { pausedWords } from "@/components/version-status";
import { copyReasons, otherProvidersLine, queuedWords, type ProviderCopy } from "@/lib/provider-copies";
import type { CopiesResult } from "@/api/provider-copies";

/** The "Other providers" line, or nothing when Cached on says it all. */
export function OtherProviders({ copies, shown }: { copies?: readonly ProviderCopy[] | null; shown?: readonly string[] | null }) {
  const line = otherProvidersLine(copies, shown);
  if (!line) return null;
  return <span className="text-xs text-[var(--ink-2)]" title={copyReasons(copies, shown)} data-testid="other-providers">{line}</span>;
}

/** The action. paused: Pause's state ("off" or "locked"), which disables it with the Pause words. current: the copies
 *  as the page shows them now, which the action's words follow. */
export function AddToProviders<V>({ m, vars, paused, current, testid }: { m: UseMutationResult<CopiesResult, Error, V>; vars: V; paused?: "locked" | "off" | "unknown"; current?: readonly ProviderCopy[] | null; testid?: string }) {
  const why = paused ? pausedWords(paused) : undefined;
  const mine = m.variables === vars || JSON.stringify(m.variables) === JSON.stringify(vars);
  return (
    <span className="flex flex-col items-start gap-1">
      {/* A disabled button shows no tooltip: the wrapper carries the reason. */}
      <span title={why ?? "Add this torrent to every other provider that has it cached; an add that is not cached is deleted again"} className="inline-flex">
        <Button size="sm" variant="outline" disabled={!!why || m.isPending} onClick={() => m.mutate(vars)} data-testid={testid ?? "add-to-providers"}>
          {m.isPending && mine ? <Loader2 className="animate-spin" /> : <CopyPlus />} Add to other providers
        </Button>
      </span>
      {mine && m.data && queuedWords(m.data.copies, current) ? <span className="text-xs text-[var(--accent-ink)]" data-testid="add-to-providers-result">{queuedWords(m.data.copies, current)}</span> : null}
      {mine && m.error ? <span className="text-xs text-[var(--bad-ink)]" role="alert">{errText(m.error)}</span> : null}
    </span>
  );
}
