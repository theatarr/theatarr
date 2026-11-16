// "Last search" for the title page's releases route (managers; docs/design/ui.md "Releases"): the last search the
// decision engine decided for each library, read-only: every release ranked, with quality, custom formats, score,
// every rejection and what came of it (release-grid.tsx). Nothing here searches the indexers or grabs: a library that
// may search offers Interactive search (interactive-search.tsx), which does both. Mounted by /title/{id}/releases as
// <ShowReleases titleId />. Admins also get "Add by magnet or hash" under the list, for the shown library
// (release-grab.tsx, AddByHash).
import { ChevronDown, ChevronRight, Loader2, Search } from "lucide-react";
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
import { fmtAgo, fmtNumber, plural } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useLibraryName } from "@/lib/library-names";
import { rejectionWords } from "@/lib/rejection-words";

const SOURCE: Record<string, string> = {
  live: "Live search",
  journal: "Last decided search",
  none: "Nothing stored",
};

// The notes a search stores (internal/router/search.go and the indexers' Notes) that are information, not trouble:
// kept/dropped/added counts, the page cap, a pack-size caveat, a name-only query that did answer.
const INFO = [
  /^dropped \d+ /, /^ignored the expanded records/, /^title search added \d+/, /^season search: kept/, /^page_cap \d+ reached/,
  /^no episode counts for this series$/, /^pack sizes are file sizes$/, /^no AniList id maps to this title/,
  /only a plain keyword query$/, /^no episode criteria$/, /^\d+ results from \d+ indexers$/,
];
// Every other note is trouble, in these words (the first that matches); anything else "had an error".
const TROUBLE: [RegExp, string][] = [
  [/budget|rate.?limit|too many requests|\b429\b|next token/i, "was rate-limited"],
  [/deadline exceeded|timed? ?out|waited too long/i, "did not answer in time"],
  [/\b40[13]\b|unauthori[sz]ed|forbidden/i, "refused the sign-in"],
  [/\b5\d\d\b|service unavailable|bad gateway/i, "was unavailable"],
  [/partial answer/i, "answered only in part"],
];
// Unprefixed failure notes only one indexer writes.
const OWNER: [RegExp, string][] = [[/^title search failed, IMDb results only/, "zilean"], [/^episode counts: /, "torrentio"]];

/** One line for the indexers that did not answer fully, from the search's notes (joined by "; "): "Search incomplete:
 *  therarbg was rate-limited, nyaa did not answer in time." Empty when every note is information. A note is named by
 *  the "indexer:x" a gateway note carries, the "indexer x:" inside it, its "x:" prefix, or its known owner; an unnamed piece
 *  right after a named failure is the rest of that failure. The raw notes stay under Technical details. */
export function searchIncomplete(notes: string): string {
  const words = new Map<string, string>();
  let last = "";
  for (const raw of notes.split("; ")) {
    const note = raw.trim();
    if (!note) continue;
    const prefixed = /^(?:indexer )?([\w.-]+): (.+)$/.exec(note);
    const body = prefixed ? prefixed[2] : note;
    if (INFO.some((re) => re.test(body) || re.test(note))) { last = ""; continue; }
    const name = /indexer:([\w.-]+)/.exec(note)?.[1] ?? /\bindexer ([\w.-]+): /.exec(note)?.[1] ?? (prefixed && prefixed[1] !== "gateway" ? prefixed[1] : undefined)
      ?? OWNER.find(([re]) => re.test(note))?.[1] ?? (last || "an indexer");
    const word = TROUBLE.find(([re]) => re.test(note))?.[1] ?? "had an error";
    const prev = words.get(name);
    // One phrase per indexer: a named cause wins over "answered only in part" and "had an error".
    const weak = (w?: string) => w === undefined || w === "had an error" || w === "answered only in part";
    if (prev === undefined || (weak(prev) && !weak(word))) words.set(name, word);
    last = name;
  }
  if (words.size === 0) return "";
  return `Search incomplete: ${[...words].map(([name, w]) => `${name} ${w}`).join(", ")}.`;
}

/** The search's facts and rejection counts. With onReason (interactive search) each count is a toggle that keeps
 *  the list to the releases rejected for it. */
export function Summary({ r, reason, onReason }: { r: Releases; reason?: string; onReason?: (code: string | undefined) => void }) {
  const rej = Object.entries(r.rejections ?? {}).sort((a, b) => b[1] - a[1]);
  const incomplete = searchIncomplete(r.notes ?? "");
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge tone={r.source === "live" ? "info" : r.source === "none" ? "neutral" : "accent"} data-testid="releases-source">{SOURCE[r.source] ?? r.source}</Badge>
        {/* One text run, so a wrapped line never starts with a separator; what the search covered is in the message. */}
        <span className="text-muted-foreground" data-testid="releases-facts">
          {[r.at ? fmtAgo(r.at) : "", `${plural(r.results ?? 0, "result")}, ${fmtNumber(r.accepted ?? 0)} accepted`].filter(Boolean).join(" · ")}
        </span>
      </div>
      {r.message ? <p className="text-xs text-[var(--ink-3)]" data-testid="releases-message">{r.message}</p> : null}
      {r.grabbed ? <p className="text-sm font-medium text-[var(--ink)]" data-testid="releases-grabbed">{r.grabbed}</p> : null}
      {rej.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {rej.map(([k, n]) => {
            const chip = "rounded-md border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-1.5 py-0.5 text-[11px] text-[var(--bad-ink)]";
            const words = <>{rejectionWords(k)} <span className="tabular-nums opacity-80">×{n}</span></>;
            return onReason ? (
              <button key={k} type="button" title={k} aria-pressed={reason === k} onClick={() => onReason(reason === k ? undefined : k)}
                className={cn(chip, "hover:border-[var(--bad-ink)]", reason === k && "ring-2 ring-[var(--line-focus)]")} data-testid="releases-reason">
                {words}
              </button>
            ) : (
              <span key={k} title={k} className={chip}>{words}</span>
            );
          })}
        </div>
      ) : null}
      {incomplete ? <p className="text-xs text-[var(--ink-2)]" data-testid="releases-incomplete">{incomplete}</p> : null}
      {r.notes ? (
        <details className="group text-[11.5px] text-[var(--ink-3)]">
          <summary className="flex cursor-pointer list-none items-center gap-1 [&::-webkit-details-marker]:hidden">
            <ChevronRight className="size-3 transition-transform group-open:rotate-90" aria-hidden />
            Technical details
          </summary>
          <p className="mt-1 break-words" data-testid="releases-notes">Indexers: {r.notes.replace(/\b1 ([\w-]+ )?entries\b/g, "1 $1entry")}</p>
        </details>
      ) : null}
    </div>
  );
}

/** The panel. Collapsed until opened (a closed panel loads nothing). */
export function ShowReleases({
  titleId,
  kind = "movie",
  defaultOpen = false,
  initialVersion,
  focus,
  labels,
}: {
  titleId: number;
  kind?: "movie" | "series";
  defaultOpen?: boolean;
  initialVersion?: string;
  focus?: string;
  /** Library names by settings key (from the title), for the tabs. */
  labels?: Record<string, string>;
}) {
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
        {/* The Last search page says this in its header already. */}
        {defaultOpen ? null : <span className="hidden text-xs text-muted-foreground sm:inline">the last search Theatarr decided for each library</span>}
        <ChevronDown className={cn("size-4 transition-transform", !open && "-rotate-90")} />
      </button>
      {open ? <Panel titleId={titleId} kind={kind} initialVersion={initialVersion} focus={focus} labels={labels} /> : null}
    </Card>
  );
}

function Panel({ titleId, kind, initialVersion, focus, labels }: { titleId: number; kind: "movie" | "series"; initialVersion?: string; focus?: string; labels?: Record<string, string> }) {
  const perms = usePerms();
  const nameOf = useLibraryName();
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
              <VersionTag k={k} label={labels?.[k]} className="text-[12px]" />
            </button>
          ))}
          {r ? <VersionStatus state={r.grab_state} settingsKey={r.version} canManage={perms.admin} quiet /> : null}
        </div>
        {r?.live_allowed && perms.manage ? (
          // A series is searched per season or per episode, never as a whole.
          kind === "movie" ? <InteractiveSearchButton titleId={titleId} version={r.version} variant="outline" /> : null
        ) : r ? (
          <span className="text-xs text-[var(--ink-3)]" data-testid="releases-no-live">The acquirer is not running</span>
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
          {items.length === 0 ? <Empty>No releases to show for {labels?.[r.version] || nameOf(r.version)}.</Empty> : <ReleaseGrid key={r.version} items={items} kind={kind} outcomes={r.outcomes} focus={shownFocus} />}
        </>
      ) : null}
      {r && perms.admin ? <AddByHash key={r.version} titleId={titleId} version={r.version} label={labels?.[r.version]} /> : null}
    </div>
  );
}
