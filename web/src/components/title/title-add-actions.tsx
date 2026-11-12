// The copies section's header (docs/design/ui.md, title actions): "Add to another library" (the add sheet), Interactive
// search (managers, when a library of the title searches live) and search now for every copy. Turning a copy on or off lives on its card (versions.tsx), the one place a copy is managed. Search
// now is offered only when it would run: a monitored version in mode active while router.enabled is true; otherwise
// the header says why nothing would be searched, except on the title page, where each library's card says it (as the
// Episodes tab names the reason per library). A failed search now says so, with Retry; a new try clears the last
// outcome first. "Add to another library" opens the add sheet for this title by its id (the add posts title_id, so a series
// known only by its TVDB id works). Mounted by the title page and the releases page as <TitleAddActions titleId
// name kind />.
import { AlertTriangle, Loader2, Plus, RotateCcw, Search } from "lucide-react";
import { useAddOptions, useSearchNow, type AddTarget } from "@/api/add";
import { ApiError } from "@/api/client";
import { usePerms, useTitle } from "@/api/library";
import { useSearchUI } from "@/components/search/search-context";
import { Button } from "@/components/ui/button";
import { InteractiveSearchButton } from "@/components/title/interactive-search";
import { DownloadsOffNotice } from "@/components/version-status";
import { cn } from "@/lib/utils";
import { globalDownloadsOff, verdict } from "@/lib/version-grab";

export function TitleAddActions({ titleId, name, year, kind, poster, anime, add = true, cardNotes = false }: {
  titleId: number;
  name: string;
  year?: number;
  kind: "movie" | "series";
  poster?: string;
  anime?: boolean;
  /** Offer "Add to another library" (the add permission). */
  add?: boolean;
  /** Each library's card below states its own download block (the copies tab), so the header does not repeat it. */
  cardNotes?: boolean;
}) {
  const target: AddTarget = { kind, name, year, poster, title_id: titleId, anime };
  const opts = useAddOptions(target, Boolean(anime));
  const searchNow = useSearchNow(titleId);
  const { openAdd } = useSearchUI();
  const perms = usePerms();
  // Interactive search for managers when a library of the title searches the indexers live (not Watch only).
  const title = useTitle(String(titleId), perms.manage);
  const live = perms.manage && (title.data?.versions ?? []).some((v) => v.control?.live_releases);
  // The mutation holds the last try: its answer, its error, or nothing while a new one runs (mutate resets both).
  const outcome = searchNow.data ?? null;
  const runSearch = () => searchNow.mutate(undefined);
  const o = opts.data;
  const searchable = perms.manage ? (o?.versions ?? []).filter((v) => v.on && v.searchable).map((v) => v.key) : [];
  // Why nothing is searched, in the shared verdict words: the global state once, else each monitored library's verdict.
  const on = (o?.versions ?? []).filter((v) => v.on);
  const globalOff = globalDownloadsOff(on.map((v) => v.grab_state));
  const why = !o || !perms.manage || cardNotes && on.length
    ? ""
    : !on.length
      ? "No library is monitored."
      : globalOff
        ? <DownloadsOffNotice off={globalOff} canManage={perms.admin} />
        : <>Nothing is searched now: {on.map((v) => `${v.label || v.key}: ${v.grab_state ? verdict(v.grab_state).word : "Downloads off"}`).join(" · ")}</>;
  return (
    <div data-testid="title-add-actions">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-lg font-semibold text-[var(--ink)]">Libraries</h2>
        <div className="flex flex-wrap gap-2">
          {live ? <InteractiveSearchButton titleId={titleId} /> : null}
          {searchable.length > 0 ? (
            <Button size="sm" variant="outline" onClick={runSearch} disabled={searchNow.isPending} data-testid="title-search-now">
              {searchNow.isPending ? <Loader2 className="animate-spin" /> : <Search />} Search now
            </Button>
          ) : null}
          {add ? (
            <Button size="sm" variant="outline" onClick={() => openAdd(target)} data-testid="title-add-versions">
              <Plus /> Add to another library
            </Button>
          ) : null}
        </div>
      </div>
      {searchNow.error ? (
        <div role="alert" className="mt-2 flex flex-wrap items-center gap-2 rounded-[5px] border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-3 py-2 text-xs" data-testid="title-search-error">
          <AlertTriangle className="size-3.5 shrink-0 text-[var(--bad-ink)]" />
          <span className="min-w-0 flex-1 text-[var(--bad-ink)] [overflow-wrap:anywhere]">
            Search now failed: {searchNow.error instanceof ApiError ? searchNow.error.message : "could not reach the server"}
          </span>
          <Button size="xs" variant="outline" onClick={runSearch} disabled={searchNow.isPending} data-testid="title-search-retry">
            {searchNow.isPending ? <Loader2 className="animate-spin" /> : <RotateCcw />} Retry
          </Button>
        </div>
      ) : outcome ? (
        <p className={cn("mt-2 rounded-[5px] border px-3 py-2 text-xs", outcome.started ? "border-[var(--th-info-border)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line)] text-[var(--ink-3)]")} data-testid="title-search-outcome">
          {outcome.message}
        </p>
      ) : searchable.length === 0 && why && !opts.isPending ? (
        <div className="mt-1 text-xs text-[var(--ink-3)]" data-testid="title-no-search">{why}</div>
      ) : null}
    </div>
  );
}
