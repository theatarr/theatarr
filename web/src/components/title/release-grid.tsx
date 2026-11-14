// The release grid (docs/design/ui.md "Interactive search"): one row per release of a search, as interactive search
// shows them live and "Last search" shows the stored decision. Columns sort, filter and can be hidden and reordered
// (lib/release-columns.ts; the choice and the sort are kept per user in this browser); filters are not kept. With grab
// (interactive search, managers) each row has Grab: an accepted release goes to the router at once; a rejected one, or
// one that would replace a better-scored file, asks first (release-grab.tsx GrabDialog), as does one the server
// rejects at grab time. A table from md up, one card per release below it.
// The status chips and score column follow show-releases' earlier table, adapted from Scryer
// (github.com/scryer-media/scryer) apps/scryer-web interactive search results @ 2ef7757c, GPL-3.0.
import { ArrowDown, ArrowUp, Check, CheckCircle2, Columns3, Download, Loader2, XCircle } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { Candidate, ReleaseOutcome, Releases } from "@/api/add";
import { useAuth } from "@/api/auth";
import { useGrab } from "@/api/ops";
import { ColumnPicker } from "@/components/library/view-options";
import { ProviderPills, ProviderTag } from "@/components/provider-pills";
import { GrabDialog, lateRejections, type GrabTarget } from "@/components/title/release-grab";
import { toast } from "@/components/toaster";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { readPref, writePref } from "@/lib/copies";
import { fmtBytes } from "@/lib/format";
import {
  FLAG_LABEL,
  NO_FILTER,
  SOURCE_LABEL,
  columnSortKey,
  episodesText,
  filterReleases,
  fmtAge,
  indexerWords,
  naturalDir,
  needsConfirm,
  normalizeReleaseColumns,
  peersText,
  releaseColumnsFor,
  sortReleases,
  type ReleaseColumnId,
  type ReleaseColumnPrefs,
  type ReleaseFilter,
  type ReleaseSortKey,
} from "@/lib/release-columns";
import { cn } from "@/lib/utils";

/** What came of a release for this library, by its hash (Releases.outcomes): grabbed, imported, current, failed or
 *  replaced. */
export function Outcome({ o }: { o?: ReleaseOutcome }) {
  if (!o) return null;
  switch (o.outcome) {
    case "grabbed":
      return <Badge tone="info" data-testid="release-outcome">Grabbed{o.provider ? <> · <ProviderTag account={o.provider} /></> : null}</Badge>;
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

const outcomeOf = (c: Candidate, outcomes?: Releases["outcomes"]) => (c.info_hash ? outcomes?.[c.info_hash.toLowerCase()] : undefined);

const signed = (n: number) => (n > 0 ? `+${n}` : String(n));

function Status({ c }: { c: Candidate }) {
  if (c.accepted) {
    return (
      <span className="inline-flex items-center gap-1 text-xs font-medium text-[var(--ok-ink)]">
        <CheckCircle2 className="size-3.5" /> Accepted{c.upgrade ? " · upgrade" : ""}
      </span>
    );
  }
  const all = (c.rejections ?? []).map((r) => r.message || r.reason || "").filter(Boolean);
  return (
    <span className="flex flex-col gap-0.5" title={all.join("\n")} data-testid="release-status">
      {all.slice(0, 2).map((m, i) => (
        <span key={i} className="inline-flex items-start gap-1 text-xs text-[var(--bad-ink)]">
          <XCircle className="mt-0.5 size-3 shrink-0" />
          <span className="[overflow-wrap:anywhere]">{m}</span>
        </span>
      ))}
      {all.length > 2 ? <span className="pl-4 text-[11px] text-[var(--ink-3)]">+{all.length - 2} more</span> : null}
      {all.length === 0 ? <span className="text-xs text-[var(--bad-ink)]">Rejected</span> : null}
    </span>
  );
}

function Score({ c }: { c: Candidate }) {
  const s = c.score ?? 0;
  const title = (c.formats ?? []).map((f) => `${f.name} ${signed(f.score ?? 0)}`).join("\n");
  return (
    <span className={cn("font-semibold tabular-nums", s > 0 ? "text-[var(--ok-ink)]" : s < 0 ? "text-[var(--bad-ink)]" : "text-[var(--ink-2)]")} title={title || undefined} data-testid="release-score">
      {signed(s)}
    </span>
  );
}

function Cached({ c }: { c: Candidate }) {
  return <ProviderPills codes={c.cached_on} testid="release-cached" empty={<span className="text-[var(--ink-3)]" title="No cache evidence (not the same as uncached)">—</span>} />;
}

function Quality({ c }: { c: Candidate }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1 whitespace-nowrap">
      {c.quality || "—"}
      {(c.revision ?? 0) >= 2 ? <Badge tone="info" data-testid="release-proper">Proper</Badge> : null}
    </span>
  );
}

function Languages({ c }: { c: Candidate }) {
  const ls = c.languages ?? [];
  if (ls.length === 0) return <span className="text-[var(--ink-3)]">—</span>;
  return <span title={ls.join(", ")}>{ls.slice(0, 2).join(", ")}{ls.length > 2 ? ` +${ls.length - 2}` : ""}</span>;
}

function Formats({ c }: { c: Candidate }) {
  const fs = c.formats ?? [];
  if (fs.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {fs.map((f, i) => (
        <span key={i} className="rounded border border-[var(--line)] px-1 text-[10.5px] text-[var(--ink-3)]">
          {f.name}
          {f.score ? <span className={cn("ml-1 tabular-nums", (f.score ?? 0) < 0 ? "text-[var(--bad-ink)]" : "text-[var(--ok-ink)]")}>{signed(f.score ?? 0)}</span> : null}
        </span>
      ))}
    </span>
  );
}

function Cell({ id, c, outcomes }: { id: ReleaseColumnId; c: Candidate; outcomes?: Releases["outcomes"] }) {
  switch (id) {
    case "source":
      return (
        <span className="inline-flex flex-wrap items-center gap-1 whitespace-nowrap">
          {c.source ? SOURCE_LABEL[c.source] ?? c.source : "—"}
          {c.privacy === "private" || c.privacy === "semi-private" ? <Badge tone="warn" title={c.privacy === "private" ? "Private tracker" : "Semi-private tracker"}>Private</Badge> : null}
        </span>
      );
    case "age":
      return <span className="whitespace-nowrap" title={c.published}>{fmtAge(c.published)}</span>;
    case "indexer":
      return <span className="whitespace-nowrap" title={c.indexer}>{indexerWords(c.indexer) || "—"}</span>;
    case "history":
      return <Outcome o={outcomeOf(c, outcomes)} />;
    case "size":
      return <span className="whitespace-nowrap">{c.size ? fmtBytes(c.size) : "—"}</span>;
    case "peers":
      return <span className="whitespace-nowrap" data-testid="release-peers">{peersText(c)}</span>;
    case "cached":
      return <Cached c={c} />;
    case "episodes":
      return <span className="whitespace-nowrap font-mono text-[12px]">{episodesText(c.episodes)}</span>;
    case "language":
      return <Languages c={c} />;
    case "quality":
      return <Quality c={c} />;
    case "score":
      return <Score c={c} />;
    case "formats":
      return <Formats c={c} />;
    case "flags":
      return (c.flags ?? []).length ? (
        <span className="flex flex-wrap gap-1">{(c.flags ?? []).map((f) => <Badge key={f} tone="accent">{FLAG_LABEL[f] ?? f}</Badge>)}</span>
      ) : null;
    case "status":
      return <Status c={c} />;
  }
}

/** Grab for one row. An accepted release is sent at once; one that asks first (needsConfirm) opens the confirmation,
 *  as does a send the server refuses with its reasons (then one confirmation overrides). */
function GrabCell({ c, target, sent, onSent }: { c: Candidate; target: GrabTarget; sent: boolean; onSent: () => void }) {
  const grab = useGrab(target.titleId);
  const [dialog, setDialog] = useState<{ late?: string[] }>();
  const library = target.label ?? target.version;
  const ask = needsConfirm(c);
  const click = () => {
    if (ask) {
      setDialog({});
      return;
    }
    grab.mutate(
      { version: target.version, info_hash: c.info_hash ?? "", override: false },
      {
        onSuccess: () => {
          onSent();
          toast({ tone: "ok", title: "Sent to the router", body: `${c.release ?? "The release"} for ${library}` });
        },
        onError: (e) => {
          const late = lateRejections(e);
          if (late) setDialog({ late });
          else toast({ tone: "bad", title: "Not sent", body: errText(e) });
        },
      },
    );
  };
  const failed = grab.isError && !lateRejections(grab.error);
  return (
    <>
      {sent ? (
        <span className="inline-flex items-center gap-1 text-xs font-medium text-[var(--ok-ink)]" data-testid="release-sent">
          <Check className="size-3.5" /> Sent
        </span>
      ) : (
        <Button
          size="xs"
          variant={c.accepted ? "outline" : "ghost"}
          onClick={click}
          disabled={!c.info_hash || grab.isPending}
          title={!c.info_hash ? "No info hash" : failed ? errText(grab.error) : ask ? "Asks first" : "Send this release to the router"}
          data-testid="release-grab"
        >
          {grab.isPending ? <Loader2 className="animate-spin" /> : <Download />} {failed ? "Not sent" : ask ? "Grab…" : "Grab"}
        </Button>
      )}
      {dialog ? (
        <GrabDialog
          c={c}
          target={target}
          late={dialog.late}
          onSent={onSent}
          onClose={() => {
            grab.reset();
            setDialog(undefined);
          }}
        />
      ) : null}
    </>
  );
}

function Toggle({ on, onChange, children, testid }: { on: boolean; onChange: (v: boolean) => void; children: React.ReactNode; testid: string }) {
  return (
    <Button size="sm" variant={on ? "secondary" : "outline"} className={cn(on && "border-[var(--accent)] text-[var(--accent-ink)]")} aria-pressed={on} onClick={() => onChange(!on)} data-testid={testid}>
      {children}
    </Button>
  );
}

export function ReleaseGrid({
  items,
  outcomes,
  kind,
  grab,
  focus,
  capped,
}: {
  items: Candidate[];
  outcomes?: Releases["outcomes"];
  kind: "movie" | "series";
  /** Grab for each row (interactive search, managers); none: no Grab column. */
  grab?: GrabTarget;
  /** A hash to pick out and scroll to. */
  focus?: string;
  /** The search's result count, when the answer holds only the best of them. */
  capped?: number;
}) {
  const uid = useAuth().data?.id;
  const [saved, setSaved] = useState<ReleaseColumnPrefs | null>(null);
  const prefs = saved ?? readPref("release-columns", uid, normalizeReleaseColumns);
  const save = (p: ReleaseColumnPrefs) => {
    setSaved(p);
    writePref("release-columns", uid, p);
  };
  const [filter, setFilter] = useState<ReleaseFilter>(NO_FILTER);
  const [picker, setPicker] = useState(false);
  const [sent, setSent] = useState<ReadonlySet<string>>(new Set());
  const columns = releaseColumnsFor(kind);
  const shown = prefs.order.filter((id) => prefs.shown.includes(id) && columns.some((c) => c.id === id));
  // Title follows the leading Source and Age columns, as in Radarr; the rest come after it.
  let lead = 0;
  while (lead < shown.length && (shown[lead] === "source" || shown[lead] === "age")) lead++;
  const before = shown.slice(0, lead);
  const after = shown.slice(lead);
  const rows = useMemo(() => sortReleases(filterReleases(items, filter), prefs.sort), [items, filter, prefs.sort]);
  const picked = (c: Candidate) => !!focus && !!c.info_hash && c.info_hash.toLowerCase() === focus.toLowerCase();
  useEffect(() => {
    if (!focus || !/^[0-9a-f]+$/i.test(focus)) return;
    // The table from md up, the cards below it: scroll the one on screen.
    const wide = typeof window.matchMedia === "function" ? window.matchMedia("(min-width: 768px)").matches : true;
    const view = wide ? "releases-table" : "releases-list";
    document.querySelector(`[data-testid="${view}"] [data-release-hash="${focus.toLowerCase()}"]`)?.scrollIntoView?.({ block: "center" });
  }, [focus, items]);
  const sortBy = (key: ReleaseSortKey) =>
    save({ ...prefs, sort: prefs.sort.key === key ? { key, dir: prefs.sort.dir === "asc" ? "desc" : "asc" } : { key, dir: naturalDir(key) } });
  // Sent is per title, library and hash: the grid stays mounted while the window switches library.
  const sentKey = (c: Candidate) => `${grab?.titleId}:${grab?.version}:${(c.info_hash ?? "").toLowerCase()}`;
  const markSent = (c: Candidate) => () => setSent((s) => new Set(s).add(sentKey(c)));
  const isSent = (c: Candidate) => !!c.info_hash && sent.has(sentKey(c));
  const label = (id: ReleaseColumnId | "title") => (id === "title" ? "Title" : columns.find((c) => c.id === id)?.label ?? id);
  const head = (id: ReleaseColumnId | "title") => {
    const key = columnSortKey(id);
    if (!key) return <TableHead key={id}>{label(id)}</TableHead>;
    const on = prefs.sort.key === key;
    const Arrow = prefs.sort.dir === "asc" ? ArrowUp : ArrowDown;
    return (
      <TableHead key={id} aria-sort={on ? (prefs.sort.dir === "asc" ? "ascending" : "descending") : undefined}>
        <button type="button" onClick={() => sortBy(key)} className={cn("inline-flex items-center gap-1 uppercase hover:text-foreground", on && "text-foreground")}>
          {label(id)}
          {on ? <Arrow className="size-3" /> : null}
        </button>
      </TableHead>
    );
  };
  // A row's key follows the release, not its place, so sorting keeps a grab on its way in its cell.
  const key = (c: Candidate) => `${c.info_hash ?? ""}-${c.indexer ?? ""}-${c.rank ?? ""}-${c.release ?? ""}`;
  return (
    <div className="space-y-3" data-testid="release-grid">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          className="h-8 min-w-56 flex-1 sm:max-w-sm"
          value={filter.text}
          onChange={(e) => setFilter({ ...filter, text: e.target.value })}
          placeholder="Filter releases"
          title="Filter by title, indexer, quality or language"
          aria-label="Filter releases"
          data-testid="release-filter"
        />
        <Toggle on={filter.accepted} onChange={(v) => setFilter({ ...filter, accepted: v })} testid="release-accepted-only">Accepted only</Toggle>
        <Toggle on={filter.cached} onChange={(v) => setFilter({ ...filter, cached: v })} testid="release-cached-only">Cached only</Toggle>
        {kind === "series" ? <Toggle on={filter.packs} onChange={(v) => setFilter({ ...filter, packs: v })} testid="release-packs-only">Packs only</Toggle> : null}
        <span className="flex-1" />
        {prefs.sort.key !== "rank" ? (
          <Button size="sm" variant="ghost" onClick={() => save({ ...prefs, sort: { key: "rank", dir: "asc" } })} data-testid="release-best-match">Best match</Button>
        ) : null}
        <Button size="sm" variant="outline" className="hidden md:inline-flex" onClick={() => setPicker(true)}>
          <Columns3 /> Columns
        </Button>
      </div>
      {capped && capped > items.length ? <p className="text-xs text-[var(--ink-3)]" data-testid="release-capped">Showing the best {items.length} of {capped}.</p> : null}
      {rows.length === 0 ? (
        <p className="py-4 text-center text-sm text-muted-foreground" data-testid="release-no-match">No release matches the filter.</p>
      ) : (
        <>
          <div className="hidden md:block">
            <Table density="dense" data-testid="releases-table">
              <TableHeader>
                <TableRow>
                  {before.map(head)}
                  {head("title")}
                  {after.map(head)}
                  {grab ? <TableHead className="w-20"><span className="sr-only">Grab</span></TableHead> : null}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((c) => (
                  <TableRow key={key(c)} data-release-hash={c.info_hash?.toLowerCase()} data-focus={picked(c) || undefined} className={cn(!c.accepted && "opacity-80", picked(c) && "bg-[var(--accent-soft)] opacity-100")}>
                    {before.map((id) => (
                      <TableCell key={id} className="align-top" data-column={id}>
                        <Cell id={id} c={c} outcomes={outcomes} />
                      </TableCell>
                    ))}
                    <TableCell className="min-w-64 align-top">
                      <div className="font-mono text-[12px] text-[var(--ink)] [overflow-wrap:anywhere]">{c.release}</div>
                    </TableCell>
                    {after.map((id) => (
                      <TableCell key={id} className="align-top" data-column={id}>
                        <Cell id={id} c={c} outcomes={outcomes} />
                      </TableCell>
                    ))}
                    {grab ? (
                      <TableCell className="align-top text-right">
                        <GrabCell c={c} target={grab} sent={isSent(c)} onSent={markSent(c)} />
                      </TableCell>
                    ) : null}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <ul className="space-y-2 md:hidden" data-testid="releases-list">
            {rows.map((c) => (
              <li key={key(c)} data-release-hash={c.info_hash?.toLowerCase()} data-focus={picked(c) || undefined} className={cn("rounded-[5px] border border-[var(--line)] bg-[var(--surface)] p-3", picked(c) && "border-[var(--accent)] bg-[var(--accent-soft)]")}>
                <div className="flex items-start justify-between gap-3">
                  <span className="font-mono text-[12px] text-[var(--ink)] [overflow-wrap:anywhere]">{c.release}</span>
                  <Score c={c} />
                </div>
                <div className="mt-1 text-xs text-[var(--ink-2)]">
                  {[c.quality || "—", c.size ? fmtBytes(c.size) : "", fmtAge(c.published) !== "—" ? fmtAge(c.published) : "", `${peersText(c)} peers`, indexerWords(c.indexer)].filter(Boolean).join(" · ")}
                </div>
                {kind === "series" && c.episodes?.length ? <div className="mt-1 font-mono text-[12px] text-[var(--ink-2)]" data-testid="release-card-episodes">{episodesText(c.episodes)}</div> : null}
                {c.cached_on?.length || c.languages?.length || c.flags?.length ? (
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-[var(--ink-2)]">
                    {c.cached_on?.length ? <Cached c={c} /> : null}
                    {c.languages?.length ? <Languages c={c} /> : null}
                    {c.flags?.length ? <Cell id="flags" c={c} /> : null}
                  </div>
                ) : null}
                <div className="mt-1.5 flex flex-col items-start gap-1">
                  <Status c={c} />
                  <Outcome o={outcomeOf(c, outcomes)} />
                </div>
                {grab ? <div className="mt-2"><GrabCell c={c} target={grab} sent={isSent(c)} onSent={markSent(c)} /></div> : null}
              </li>
            ))}
          </ul>
        </>
      )}
      {picker ? (
        <ColumnPicker
          label="Release columns"
          note="Title and Grab always stay. Kept for your account in this browser."
          columns={columns}
          prefs={prefs}
          onChange={save}
          onClose={() => setPicker(false)}
        />
      ) : null}
    </div>
  );
}
