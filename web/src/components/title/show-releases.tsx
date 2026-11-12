// "Last search" for the title page's releases route (managers; docs/design/ui.md "Releases"): the last search the
// decision engine decided for each library, read-only: every release ranked, with quality, custom formats, score,
// every rejection and what came of it (release-grid.tsx). Nothing here searches the indexers or grabs: a library that
// may search offers Interactive search (interactive-search.tsx), which does both. Mounted by /title/{id}/releases as
// <ShowReleases titleId />. Admins also get "Add by magnet or hash" under the list, for the shown library
// (release-grab.tsx, AddByHash).
import { ChevronDown, Loader2, Search } from "lucide-react";
import { useState } from "react";
import { useReleases, type Releases } from "@/api/add";
import { usePerms } from "@/api/library";
import { VersionStatus } from "@/components/version-status";
import { Empty, ErrorState, VersionTag } from "@/components/page";
import { InteractiveSearchButton } from "@/components/title/interactive-search";
import { AddByHash } from "@/components/title/release-grab";
import { ReleaseGrid } from "@/components/title/release-grid";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { fmtAgo } from "@/lib/format";
import { cn } from "@/lib/utils";

const SOURCE: Record<string, string> = {
  live: "Live search",
  journal: "Last decided search",
  none: "Nothing stored",
};

export function Summary({ r }: { r: Releases }) {
  const rej = Object.entries(r.rejections ?? {}).sort((a, b) => b[1] - a[1]);
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge tone={r.source === "live" ? "info" : r.source === "none" ? "neutral" : "accent"} data-testid="releases-source">{SOURCE[r.source] ?? r.source}</Badge>
        {r.at ? <span className="text-muted-foreground">{fmtAgo(r.at)}</span> : null}
        {r.target && r.target !== "movie" ? <span className="text-muted-foreground">· {r.target}</span> : null}
        <span className="text-muted-foreground">· {r.results} results, {r.accepted} accepted</span>
      </div>
      {r.message ? <p className="text-xs text-[var(--ink-3)]" data-testid="releases-message">{r.message}</p> : null}
      {r.grabbed ? <p className="text-sm font-medium text-[var(--ink)]" data-testid="releases-grabbed">{r.grabbed}</p> : null}
      {rej.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {rej.map(([k, n]) => (
            <span key={k} className="rounded-md border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-1.5 py-0.5 text-[11px] text-[var(--bad-ink)]">
              {k} <span className="tabular-nums opacity-80">×{n}</span>
            </span>
          ))}
        </div>
      ) : null}
      {r.notes ? <p className="text-[11.5px] text-[var(--ink-3)]">Indexers: {r.notes}</p> : null}
    </div>
  );
}

/** The panel. Collapsed until opened (a closed panel loads nothing). */
export function ShowReleases({ titleId, kind = "movie", defaultOpen = false, initialVersion, focus }: { titleId: number; kind?: "movie" | "series"; defaultOpen?: boolean; initialVersion?: string; focus?: string }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <Card className="p-0" data-testid="show-releases">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-3 px-4 py-3 text-left"
      >
        <Search className="size-4 text-primary" />
        <span className="flex-1 font-semibold">Last search</span>
        <span className="hidden text-xs text-muted-foreground sm:inline">the last search Theatarr decided for each library</span>
        <ChevronDown className={cn("size-4 transition-transform", !open && "-rotate-90")} />
      </button>
      {open ? <Panel titleId={titleId} kind={kind} initialVersion={initialVersion} focus={focus} /> : null}
    </Card>
  );
}

function Panel({ titleId, kind, initialVersion, focus }: { titleId: number; kind: "movie" | "series"; initialVersion?: string; focus?: string }) {
  const perms = usePerms();
  const [version, setVersion] = useState<string | undefined>(initialVersion);
  const q = useReleases(titleId, version, false);
  const r: Releases | undefined = q.data;
  const versions = r?.versions ?? [];
  const items = r?.candidates ?? [];
  // The followed release is picked out in the library it was followed for only.
  const shownFocus = focus && r?.version === initialVersion ? focus : undefined;
  return (
    <div className="space-y-4 border-t border-[var(--line)] p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Library">
          {versions.map((k) => (
            <button
              key={k}
              type="button"
              role="tab"
              aria-selected={r?.version === k}
              onClick={() => setVersion(k)}
              className={cn("rounded-md p-0.5 ring-offset-0 transition", r?.version === k ? "ring-2 ring-[var(--line-focus)]" : "opacity-70 hover:opacity-100")}
            >
              <VersionTag k={k} />
            </button>
          ))}
          {r ? <VersionStatus state={r.grab_state} showMode settingsKey={r.version} canManage={perms.admin} /> : null}
        </div>
        {r?.live_allowed && perms.manage ? (
          // A series is searched per season or per episode, never as a whole.
          kind === "movie" ? <InteractiveSearchButton titleId={titleId} version={r.version} variant="outline" /> : null
        ) : r ? (
          <span className="text-xs text-[var(--ink-3)]" data-testid="releases-no-live">Watch only: no live indexer search</span>
        ) : null}
      </div>
      {q.isPending ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Loading…</div>
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : r ? (
        <>
          <Summary r={r} />
          {shownFocus && !items.some((c) => c.info_hash?.toLowerCase() === shownFocus.toLowerCase()) ? (
            <p className="text-xs text-[var(--ink-3)]" data-testid="releases-focus-missing">The release you followed is not in this list: only the newest search's releases are kept here.</p>
          ) : null}
          {items.length === 0 ? <Empty>No releases to show for {r.version}.</Empty> : <ReleaseGrid items={items} kind={kind} outcomes={r.outcomes} focus={shownFocus} />}
        </>
      ) : null}
      {r && perms.admin ? <AddByHash key={r.version} titleId={titleId} version={r.version} /> : null}
    </div>
  );
}
