// Interactive search (docs/design/ui.md "Interactive search"): a wide window that searches the indexers the moment it
// opens, for one library of a title (tabs for the title's libraries that may search), of the whole title, one season
// or one episode, and shows every release in the release grid (release-grid.tsx) with Grab for managers. Each opening
// searches afresh; a library already answered in this opening is not searched again until Search again. Closing the
// window or switching library cancels a search still running. A search the server refused or that failed answers with
// the stored decision and says why: those rows cannot be grabbed. Opened by InteractiveSearchButton, which the title
// page, the season headers, the episode rows and overview, and Wanted mount.
import { Loader2, RefreshCw, UserSearch, X } from "lucide-react";
import { useEffect, useState, type KeyboardEvent, type MouseEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLiveSearch } from "@/api/add";
import { qk } from "@/api/query-keys";
import { usePerms, useTitle } from "@/api/library";
import { useRemovalScope } from "@/api/ops";
import { ErrorState, Loading } from "@/components/page";
import { ReleaseGrid } from "@/components/title/release-grid";
import { Summary } from "@/components/title/show-releases";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { cn } from "@/lib/utils";

export type SearchScope = { titleId: number; version?: string; season?: number; episode?: { id: number; code: string } };

/** The title's libraries interactive search may search: current copies (not removed: the removal scope's copies;
 *  the title data still lists removed ones) that search the indexers live. */
export function useLiveLibraries(titleId: number, enabled = true) {
  const t = useTitle(String(titleId), enabled);
  const scope = useRemovalScope(titleId, undefined, enabled);
  const current = new Set((scope.data?.copies ?? []).map((c) => c.key));
  const libs = (t.data?.versions ?? []).filter((v) => v.control?.live_releases && v.key && current.has(v.key));
  return { libs, title: t, pending: t.isPending || scope.isPending, error: t.error ?? scope.error };
}

/** A library tab's face: the library's colour and name; the settings key is the tooltip. */
export function LibraryTabLabel({ k, name }: { k: string; name?: string }) {
  return (
    <span
      data-vkey={k}
      title={k}
      className="inline-flex h-5 items-center gap-1.5 whitespace-nowrap rounded-[3px] px-1.5 text-[12px] font-medium leading-none text-[var(--ink-2)] shadow-[inset_0_0_0_1px_var(--line-2)]"
    >
      <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
      {name || k}
    </span>
  );
}

let sessions = 0;
const nextSession = () => ++sessions;

/** The counter while the indexers answer: seconds since this mounted. */
function Waiting({ library }: { library: string }) {
  const [n, setN] = useState(0);
  useEffect(() => {
    const t = window.setInterval(() => setN((x) => x + 1), 1000);
    return () => window.clearInterval(t);
  }, []);
  return (
    <div className="flex flex-col items-center gap-2 py-12 text-center" role="status" data-testid="interactive-waiting">
      <Loader2 className="size-6 animate-spin text-[var(--accent)]" />
      <p className="text-sm font-medium text-[var(--ink)]">
        Searching the indexers for {library}… <span className="tabular-nums">{n} s</span>
      </p>
      <p className="max-w-md text-xs text-[var(--ink-3)]">Most indexers answer within a minute; a slow one can take longer. Closing this window stops the search.</p>
    </div>
  );
}

export function InteractiveSearch({ titleId, version, season, episode, onClose }: SearchScope & { onClose: () => void }) {
  const perms = usePerms();
  const { libs, title: t, pending, error } = useLiveLibraries(titleId);
  const d = t.data;
  const [chosen, setChosen] = useState(version);
  const lib = libs.find((v) => v.key === chosen) ?? libs[0];
  const [session] = useState(nextSession);
  const qc = useQueryClient();
  // This opening's answers go with the window (a search still running is cancelled).
  useEffect(() => () => qc.removeQueries({ queryKey: qk.interactive(titleId, session) }), [qc, titleId, session]);
  const q = useLiveSearch({ titleId, version: lib?.key ?? "", season, episode: episode?.id, session, enabled: !!lib });
  const r = q.data;
  const library = lib ? lib.label || lib.key || "" : "";
  const kind = d?.kind === "series" ? "series" : "movie";
  const scope = episode ? episode.code : season === undefined ? "" : season === 0 ? "Specials" : `Season ${season}`;
  const live = r?.source === "live";
  const items = r?.candidates ?? [];
  const existing = items.find((c) => c.existing)?.existing;
  const grab = r && live && r.grab && perms.manage && lib ? { titleId, version: r.version, label: library } : undefined;
  return (
    <Modal
      label="Interactive search"
      onClose={onClose}
      testid="interactive-search"
      className="z-[75] flex items-end justify-center md:items-center md:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex h-[100dvh] w-full flex-col border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 md:h-[90dvh] md:max-w-[1600px] md:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <h2 className="text-lg font-semibold leading-tight text-[var(--ink)]">Interactive search</h2>
        <p className="mt-0.5 text-sm text-muted-foreground" data-testid="interactive-subject">
          {d ? `${d.name ?? `Title ${titleId}`}${d.year ? ` (${d.year})` : ""}` : "…"}
          {scope ? <span className="ml-2 font-medium text-[var(--ink-2)]">{scope}</span> : null}
        </p>
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        {pending ? (
          <Loading />
        ) : error ? (
          <ErrorState error={error} />
        ) : !lib ? (
          <p className="text-sm text-muted-foreground" data-testid="interactive-none">None of this title's libraries searches the indexers: they are Watch only.</p>
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Library">
                {libs.map((v) => (
                  <button
                    key={v.key}
                    type="button"
                    role="tab"
                    aria-selected={v.key === lib.key}
                    onClick={() => setChosen(v.key)}
                    className={cn("rounded-md p-0.5 ring-offset-0 transition", v.key === lib.key ? "ring-2 ring-[var(--line-focus)]" : "opacity-70 hover:opacity-100")}
                  >
                    <LibraryTabLabel k={v.key ?? ""} name={v.label} />
                  </button>
                ))}
              </div>
              <Button size="sm" variant="outline" onClick={() => void q.refetch()} disabled={q.isFetching} data-testid="interactive-again">
                <RefreshCw /> Search again
              </Button>
            </div>
            {q.isFetching ? (
              <Waiting key={lib.key} library={library} />
            ) : q.error ? (
              <ErrorState error={q.error} />
            ) : r ? (
              <>
                {live ? (
                  <Summary r={r} />
                ) : (
                  <>
                    <div className="space-y-1 rounded-[5px] border border-[var(--th-warning-border)] bg-[var(--warn-soft)] p-3 text-sm text-[var(--warn-ink)] [overflow-wrap:anywhere]" role="alert" data-testid="interactive-fallback">
                      {r.message ? <p>{r.message}</p> : null}
                      <p>{r.source === "none" ? "" : "These are the last stored results and cannot be grabbed. "}Try Search again.</p>
                    </div>
                    <Summary r={{ ...r, message: undefined }} />
                  </>
                )}
                {existing ? (
                  <p className="text-sm text-[var(--ink-2)]" data-testid="interactive-current">
                    Current file: {existing.quality || "unknown quality"}, score {existing.score ?? 0}
                  </p>
                ) : null}
                {items.length === 0 ? (
                  <p className="py-4 text-center text-sm text-muted-foreground">No releases found.</p>
                ) : (
                  <ReleaseGrid items={items} outcomes={r.outcomes} kind={kind} grab={grab} capped={r.results > items.length ? r.results : undefined} />
                )}
              </>
            ) : null}
          </>
        )}
      </div>
      <div className="flex justify-end border-t border-[var(--line)] p-3">
        <Button size="sm" variant="outline" onClick={onClose} data-testid="interactive-close">Close</Button>
      </div>
    </Modal>
  );
}

// Keys a dialog needs at the document (Modal closes on Escape and keeps Tab inside); every other key and every click
// stays inside, so a clickable row the button sits in (an episode row) does not act on them.
const keep = (e: KeyboardEvent) => {
  if (e.key !== "Escape" && e.key !== "Tab") e.stopPropagation();
};
const stop = (e: MouseEvent) => e.stopPropagation();

/** The button that opens interactive search, with the window's scope. iconOnly: an icon with the episode code as its
 *  name (episode rows). */
export function InteractiveSearchButton({
  variant = "outline",
  iconOnly = false,
  size = "sm",
  ...scope
}: SearchScope & { variant?: "outline" | "ghost" | "secondary" | "default"; iconOnly?: boolean; size?: "xs" | "sm" }) {
  const [open, setOpen] = useState(false);
  return (
    <span className="contents" onClick={stop} onKeyDown={keep}>
      <Button
        size={iconOnly ? (size === "xs" ? "icon-xs" : "icon-sm") : size}
        variant={variant}
        onClick={() => setOpen(true)}
        aria-label={iconOnly ? `Interactive search for ${scope.episode?.code ?? "this title"}` : undefined}
        title={iconOnly ? "Interactive search" : undefined}
        data-testid="interactive-search-open"
      >
        <UserSearch />
        {iconOnly ? null : "Interactive search"}
      </Button>
      {open ? <InteractiveSearch {...scope} onClose={() => setOpen(false)} /> : null}
    </span>
  );
}
