// "Show releases" for the title page (admin; docs/design/ui.md, title actions): the decision engine's results for one
// version, ranked, with quality, custom formats, score and every rejection. For a version in mode active the panel can
// search the indexers now; for a shadow version it shows the last stored decision (the journal's, or the shadow
// runner's search for an arr grab) and sends no indexer traffic. A live answer's releases can be grabbed by managers
// (release-grab.tsx); so can a stored answer's while the version may search, the grab running that search first. Mounted by the title page as <ShowReleases titleId />.
// Table look adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web interactive search results @
// 2ef7757c, GPL-3.0 (ranked rows, score column, rejection chips).
import { CheckCircle2, ChevronDown, Loader2, RefreshCw, Search, XCircle } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useReleases, type Candidate, type ReleaseOutcome, type Releases } from "@/api/add";
import { usePerms } from "@/api/library";
import { qk } from "@/api/query-keys";
import { VersionStatus } from "@/components/version-status";
import { Empty, ErrorState, VersionTag } from "@/components/page";
import { GrabButton, GrabDialog, type GrabTarget } from "@/components/title/release-grab";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtBytes } from "@/lib/format";
import { cn } from "@/lib/utils";

const SOURCE: Record<string, string> = {
  live: "Live search",
  journal: "Last decided search",
  shadow: "Shadow runner's search",
  none: "Nothing stored",
};

function Rejections({ c }: { c: Candidate }) {
  if (c.accepted) {
    return (
      <span className="inline-flex items-center gap-1 text-xs font-medium text-[var(--ok-ink)]">
        <CheckCircle2 className="size-3.5" /> Accepted{c.upgrade ? " · upgrade" : ""}
      </span>
    );
  }
  return (
    <span className="flex flex-col gap-0.5">
      {(c.rejections ?? []).map((r, i) => (
        <span key={i} className="inline-flex items-start gap-1 text-xs text-[var(--bad-ink)]" title={r.message}>
          <XCircle className="mt-0.5 size-3 shrink-0" />
          <span className="[overflow-wrap:anywhere]">{r.message || r.reason}</span>
        </span>
      ))}
    </span>
  );
}

/** What came of a release for this library, by its hash (Releases.outcomes): grabbed, imported, current, failed or
 *  replaced. */
export function Outcome({ o }: { o?: ReleaseOutcome }) {
  if (!o) return null;
  switch (o.outcome) {
    case "grabbed":
      return <Badge tone="info" data-testid="release-outcome">Grabbed{o.provider ? ` · ${o.provider}` : ""}</Badge>;
    case "imported":
      return <Badge tone="ok" data-testid="release-outcome">Imported</Badge>;
    case "current":
      return <Badge tone="ok" data-testid="release-outcome">Current file</Badge>;
    case "failed":
      return <Badge tone="bad" className="max-w-full" title={o.reason} data-testid="release-outcome"><span className="truncate">Failed{o.reason ? ` · ${o.reason}` : ""}</span></Badge>;
    case "replaced":
      return (
        <span className="inline-flex flex-wrap items-baseline gap-1">
          <Badge tone="neutral" data-testid="release-outcome">Replaced</Badge>
          {o.replaced_by ? <span className="text-[11px] text-[var(--ink-3)] [overflow-wrap:anywhere]">by {o.replaced_by}</span> : null}
        </span>
      );
  }
  return null;
}

function Formats({ c }: { c: Candidate }) {
  const fs = c.formats ?? [];
  if (fs.length === 0) return null;
  return (
    <span className="mt-1 flex flex-wrap gap-1">
      {fs.map((f, i) => (
        <span key={i} className="rounded border border-[var(--line)] px-1 text-[10.5px] text-[var(--ink-3)]">
          {f.name}
          {f.score ? <span className={cn("ml-1 tabular-nums", (f.score ?? 0) < 0 ? "text-[var(--bad-ink)]" : "text-[var(--ok-ink)]")}>{(f.score ?? 0) > 0 ? "+" : ""}{f.score}</span> : null}
        </span>
      ))}
    </span>
  );
}

function Meta({ c }: { c: Candidate }) {
  const bits = [c.indexer, c.size ? fmtBytes(c.size) : "", c.seeders ? `${c.seeders} seeders` : "", c.cached_on?.length ? `cached on ${c.cached_on.join(", ")}` : ""].filter(Boolean);
  return <span className="text-[11.5px] text-[var(--ink-3)]">{bits.join(" · ")}</span>;
}

/** The grab target for a live answer the server marked grabbable, when the caller manages the library. */
export function useGrabTarget(r: Releases | undefined, titleId: number): GrabTarget | undefined {
  const perms = usePerms();
  return r?.grab && r.source === "live" && perms.manage ? { titleId, version: r.version } : undefined;
}

const outcomeOf = (c: Candidate, outcomes?: Releases["outcomes"]) => (c.info_hash ? outcomes?.[c.info_hash.toLowerCase()] : undefined);

/** The releases, ranked. outcomes badges each row by what came of it; focus (a hash) picks one row out and scrolls
 *  to it. */
export function ReleaseTable({ items, grab, outcomes, focus }: { items: Candidate[]; grab?: GrabTarget; outcomes?: Releases["outcomes"]; focus?: string }) {
  const picked = (c: Candidate) => !!focus && !!c.info_hash && c.info_hash.toLowerCase() === focus.toLowerCase();
  useEffect(() => {
    if (focus && /^[0-9a-f]+$/i.test(focus)) document.querySelector(`[data-release-hash="${focus.toLowerCase()}"]`)?.scrollIntoView?.({ block: "center" });
  }, [focus, items]);
  return (
    <>
      <div className="hidden md:block">
        <Table density="dense" data-testid="releases-table">
          <TableHeader>
            <TableRow>
              <TableHead className="w-10">#</TableHead>
              <TableHead>Release</TableHead>
              <TableHead>Quality</TableHead>
              <TableHead className="text-right">Score</TableHead>
              <TableHead className="w-[30%]">Decision</TableHead>
              {grab ? <TableHead className="w-16"><span className="sr-only">Grab</span></TableHead> : null}
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((c, i) => (
              <TableRow key={`${c.rank}-${i}`} data-release-hash={c.info_hash?.toLowerCase()} data-focus={picked(c) || undefined} className={cn(!c.accepted && "opacity-80", picked(c) && "bg-[var(--accent-soft)] opacity-100")}>
                <TableCell className="align-top tabular-nums text-muted-foreground">{c.rank}</TableCell>
                <TableCell className="align-top">
                  <div className="font-mono text-[12px] text-[var(--ink)] [overflow-wrap:anywhere]">{c.release}</div>
                  <Meta c={c} />
                  <Formats c={c} />
                </TableCell>
                <TableCell className="align-top whitespace-nowrap">{c.quality || "—"}</TableCell>
                <TableCell className="align-top text-right font-semibold tabular-nums">{c.score ?? 0}</TableCell>
                <TableCell className="align-top">
                  <div className="flex flex-col items-start gap-1">
                    <Rejections c={c} />
                    <Outcome o={outcomeOf(c, outcomes)} />
                  </div>
                </TableCell>
                {grab ? <TableCell className="align-top text-right"><GrabButton c={c} target={grab} /></TableCell> : null}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <ul className="space-y-2 md:hidden" data-testid="releases-list">
        {items.map((c, i) => (
          <li key={`${c.rank}-${i}`} className={cn("rounded-[5px] border border-[var(--line)] bg-[var(--surface)] p-3", picked(c) && "border-[var(--accent)] bg-[var(--accent-soft)]")}>
            <div className="flex items-start justify-between gap-3">
              <span className="font-mono text-[12px] text-[var(--ink)] [overflow-wrap:anywhere]">
                <span className="mr-1.5 text-muted-foreground">{c.rank}.</span>
                {c.release}
              </span>
              <span className="shrink-0 font-semibold tabular-nums">{c.score ?? 0}</span>
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-x-2 text-xs">
              <span>{c.quality || "—"}</span>
              <Meta c={c} />
            </div>
            <Formats c={c} />
            <div className="mt-1.5 flex flex-col items-start gap-1"><Rejections c={c} /><Outcome o={outcomeOf(c, outcomes)} /></div>
            {grab ? <div className="mt-2"><GrabButton c={c} target={grab} /></div> : null}
          </li>
        ))}
      </ul>
    </>
  );
}

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
export function ShowReleases({ titleId, defaultOpen = false, initialVersion, focus }: { titleId: number; defaultOpen?: boolean; initialVersion?: string; focus?: string }) {
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
        <span className="flex-1 font-semibold">Releases</span>
        <span className="hidden text-xs text-muted-foreground sm:inline">what the decision engine sees, with scores and rejections</span>
        <ChevronDown className={cn("size-4 transition-transform", !open && "-rotate-90")} />
      </button>
      {open ? <Panel titleId={titleId} initialVersion={initialVersion} focus={focus} /> : null}
    </Card>
  );
}

function Panel({ titleId, initialVersion, focus }: { titleId: number; initialVersion?: string; focus?: string }) {
  const perms = usePerms();
  const qc = useQueryClient();
  const [version, setVersion] = useState<string | undefined>(initialVersion);
  const [live, setLive] = useState(false);
  // A stored row picked for a grab (searched first), and the one the search no longer offered.
  const [picked, setPicked] = useState<{ c: Candidate; version: string }>();
  const [gone, setGone] = useState<Candidate>();
  const q = useReleases(titleId, version, live);
  const r = q.data;
  const versions = r?.versions ?? [];
  const items = r?.candidates ?? [];
  const liveGrab = useGrabTarget(r, titleId);
  const storedGrab = r && r.source !== "live" && r.source !== "none" && r.live_allowed && perms.manage
    ? { titleId, version: r.version, pick: (c: Candidate) => setPicked({ c, version: r.version }) }
    : undefined;
  const grab = liveGrab ?? storedGrab;
  const search = () => {
    setGone(undefined);
    if (live) void q.refetch();
    else {
      // Never an older live answer: the grab is from what the indexers offer now.
      qc.removeQueries({ queryKey: qk.releases(titleId, version ?? "", true) });
      setLive(true);
    }
  };
  // A blocked or failed live search answers with the stored decision and says why (Summary's message).
  const searched = live && !q.isFetching && (q.isSuccess || q.isError);
  const fresh = searched && r?.source === "live" ? r : undefined;
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
              onClick={() => { setVersion(k); setLive(false); setGone(undefined); }}
              className={cn("rounded-md p-0.5 ring-offset-0 transition", r?.version === k ? "ring-2 ring-[var(--line-focus)]" : "opacity-70 hover:opacity-100")}
            >
              <VersionTag k={k} />
            </button>
          ))}
          {r ? <VersionStatus state={r.grab_state} showMode settingsKey={r.version} canManage={perms.admin} /> : null}
        </div>
        {r?.live_allowed ? (
          <Button size="sm" variant="outline" onClick={() => (live ? (setGone(undefined), void q.refetch()) : setLive(true))} disabled={q.isFetching} data-testid="releases-live">
            {q.isFetching && live ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            Search indexers now
          </Button>
        ) : r ? (
          <span className="text-xs text-[var(--ink-3)]" data-testid="releases-no-live">Shadow mode: no live indexer search</span>
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
          {gone && r.source === "live" ? (
            <p className="text-xs text-[var(--ink-3)]" data-testid="releases-gone">No longer offered by the indexer: <span className="font-mono">{gone.release}</span>. Nothing was grabbed.</p>
          ) : null}
          {items.length === 0 ? <Empty>No releases to show for {r.version}.</Empty> : <ReleaseTable items={items} grab={grab} outcomes={r.outcomes} focus={shownFocus} />}
        </>
      ) : null}
      {picked ? (
        <GrabDialog
          c={picked.c}
          target={{ titleId, version: picked.version }}
          onClose={() => setPicked(undefined)}
          fresh={{ search, answer: fresh, failed: searched && !fresh, at: live ? Math.max(q.dataUpdatedAt, q.errorUpdatedAt) : 0, gone: setGone }}
        />
      ) : null}
    </div>
  );
}
