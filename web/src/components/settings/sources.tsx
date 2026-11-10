// The indexer, provider and budget lists of the settings pages: status (enabled, last test, back-off, budget use),
// test and test-all with per-source results, indexer priority and provider fallback order, and the editor.
// SourcesView and BudgetsView are plain views over their props; SourcesPanel and BudgetsPanel hold the state, open
// the editor in a side sheet beside the list (the row stays marked) and announce finished tests.
import { ArrowDown, ArrowUp, Loader2, Pencil, Plus, Zap } from "lucide-react";
import { useEffect, useReducer, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { qk } from "@/api/query-keys";
import { useWriteConfirmation } from "./confirm-write";
import {
  testAll,
  testSaved,
  useReloadSettings,
  useSettingsWrite,
  type Entity,
  type SchemaEntity,
  type SettingsOverview,
  type SettingsRecord,
  type SourceEntity,
} from "@/api/settings";
import { Empty, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Choice } from "@/components/ui/confirm-dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";
import {
  editorKey,
  editorReturn,
  explainError,
  initialTests,
  movedOrder,
  movedPriorities,
  openEditor,
  orderConflicts,
  testAnnouncement,
  testErrorAction,
  testReducer,
  type EditorReturn,
  type EditorSession,
  type Explained,
  type TestAction,
  type TestState,
} from "@/lib/settings";
import { returnTarget } from "./focus";
import { Sheet, useEditParam } from "@/components/ui/sheet";
import { Announcer, ExplainBox, TestResult, useAnnouncement, type Announcement } from "./parts";
import { NOUN, RecordEditor } from "./record-form";
import { AddIndexer, HoldsCard } from "./add-indexer";
import { useCardigannCatalog, type CardigannDefinition } from "@/api/cardigann";

const KIND: Record<string, string> = { realdebrid: "Real-Debrid", alldebrid: "AllDebrid", torbox: "TorBox" };

/** A source's back-off. compact: only an active one, as a line in the indexer's row (status in the row). */
function Backoff({ r, compact }: { r: SettingsRecord; compact?: boolean }) {
  const b = r.backoff as { active?: boolean; until?: string; reason?: string; failures?: number; updated_at?: string } | undefined;
  if (compact) {
    if (!b?.active) return null;
    return (
      <div className="mt-1 text-[12.5px]" data-testid="row-backoff">
        <Badge tone="warning">backed off until {fmtTime(b.until)}</Badge>
        <span className="ml-1 break-words text-muted-foreground">{b.reason || "failure"}</span>
      </div>
    );
  }
  if (!b) return <span className="text-muted-foreground">none</span>;
  return (
    <div className="min-w-0 text-[13px]">
      {b.active ? <Badge tone="warning">backed off until {fmtTime(b.until)}</Badge> : <span className="text-muted-foreground">over</span>}
      <div className="break-words text-muted-foreground">{b.reason || "failure"}{b.updated_at ? ` · ${fmtAgo(b.updated_at)}` : ""}</div>
    </div>
  );
}

/** What each request purpose means (docs/design/gateway.md "Indexer requests by purpose"). */
const PURPOSE: Record<string, { label: string; detail: string }> = {
  interactive: { label: "Interactive", detail: "Release searches someone opened: the releases list and the arrs' interactive search" },
  wanted: { label: "Missing", detail: "Searches for something missing: wanted runs, search commands, add-time and follow-up searches" },
  upgrade: { label: "Upgrade", detail: "Searches for a better copy: cutoff unmet, or a search of a movie that has a file" },
  rss: { label: "RSS", detail: "The RSS sync reading recent releases" },
  shadow: { label: "Shadow", detail: "The shadow comparison searching for an arr grab" },
  test: { label: "Tests", detail: "Source tests from this page" },
  other: { label: "Other", detail: "Requests no search path named" },
};

/** An indexer's requests by purpose (today and the last 7 UTC days), with its budget's daily use beside them. */
export function IndexerRequests({ r, budgets }: { r: SettingsRecord; budgets?: SettingsRecord[] }) {
  const req = r.requests;
  if (!req) return null;
  const key = r.config?.budget ? String(r.config.budget) : "";
  const use = (key ? budgets?.find((b) => b.key === key)?.usage : undefined) as { daily?: number; used_day?: number } | undefined;
  const daily = use?.daily ? ` · budget ${fmtNumber(use.used_day)}/${fmtNumber(use.daily)} today` : "";
  return (
    <details className="mt-1 text-xs" data-testid="indexer-requests">
      <summary className="cursor-pointer text-muted-foreground">
        {fmtNumber(req.today)} requests today · {fmtNumber(req.week)} in 7 days{daily}
      </summary>
      {req.purposes.length === 0 ? (
        <p className="mt-1 text-muted-foreground">No requests in the last 7 days.</p>
      ) : (
        <table className="mt-1 tabular-nums" data-testid="requests-table">
          <thead>
            <tr className="text-muted-foreground">
              <th className="pr-3 text-left font-normal">Purpose</th>
              <th className="pr-3 text-right font-normal">Today</th>
              <th className="text-right font-normal">7 days</th>
            </tr>
          </thead>
          <tbody>
            {req.purposes.map((p) => (
              <tr key={p.purpose} data-purpose={p.purpose} title={(PURPOSE[p.purpose] ?? PURPOSE.other).detail}>
                <td className="pr-3">{(PURPOSE[p.purpose] ?? PURPOSE.other).label}</td>
                <td className="pr-3 text-right">{fmtNumber(p.today)}</td>
                <td className="text-right">{fmtNumber(p.week)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </details>
  );
}

function LastTest({ r, tests, now }: { r: SettingsRecord; tests: TestState; now?: number }) {
  const key = r.key;
  if (tests.running[key]) {
    return (
      <span className="inline-flex items-center gap-1.5 text-[13px] text-muted-foreground" data-testid="test-running" data-key={key}>
        <Loader2 className="size-3.5 animate-spin" /> testing…
      </span>
    );
  }
  const err = tests.errors[key];
  if (err) return <span className="text-[13px] text-[var(--bad-ink)]" data-testid="test-error" data-key={key}>{err.title}: {err.message}</span>;
  const res = tests.results[key] ?? r.last_test;
  // Only a test of the source as saved is kept (this row's Test, Test all, a full preflight): the editor's test of
  // unsaved values is not recorded, so a source tested before its save still reads as untested here.
  if (!res) return <span className="text-[13px] text-muted-foreground" data-testid="test-none">saved source not tested since Theatarr started; a test of unsaved values is not recorded</span>;
  return (
    <div>
      <TestResult result={res} now={now} />
      <div className="text-xs text-muted-foreground" data-testid="test-of-saved">saved source{res.tested_at ? `, tested ${fmtAgo(res.tested_at)}` : ""}</div>
    </div>
  );
}

export type SourcesViewProps = {
  entity: SourceEntity;
  records: SettingsRecord[];
  readOnly?: string;
  tests: TestState;
  includeDisabled: boolean;
  onIncludeDisabled: (v: boolean) => void;
  onTest: (r: SettingsRecord) => void;
  onTestAll: () => void;
  onEdit: (r: SettingsRecord | "new") => void;
  /** The record open in the sheet: its row stays marked. */
  selected?: string;
  onMove: (r: SettingsRecord, dir: -1 | 1) => void;
  moving?: boolean;
  /** A refusal of a priority or order change. */
  moveError?: Explained;
  onReload?: () => void;
  /** Versions whose provider lists contradict a proposed order, checked before sending. */
  orderNote?: string;
  now?: number;
  /** The latest finished test or test-all, for the live region. */
  announcement?: Announcement;
  /** Set by SourcesView while the filter hides rows: moves are off then. */
  filtered?: boolean;
  /** Why a catalog definition cannot feed the RSS sync, by definition id (from the catalog). */
  rssProblems?: Record<string, string>;
  /** The budgets, for an indexer's budget use beside its requests. */
  budgets?: SettingsRecord[];
};

function TestAllBar(p: SourcesViewProps) {
  const running = p.tests.all === "running";
  const n = p.tests.allKeys?.length ?? 0;
  const done = Object.values(p.tests.results);
  const ok = done.filter((r) => r.status === "ok").length;
  return (
    <Card className="mb-3 flex flex-wrap items-center gap-3 px-4 py-2.5" data-testid="testall-bar">
      <Button size="sm" variant="outline" onClick={p.onTestAll} disabled={running || !!p.readOnly} data-testid="testall">
        {running ? <Loader2 className="animate-spin" /> : <Zap />} Test all {p.includeDisabled ? "" : "enabled"}
      </Button>
      <Choice checked={p.includeDisabled} onChange={p.onIncludeDisabled} disabled={running || !!p.readOnly} testid="testall-disabled">
        include disabled
      </Choice>
      {running ? (
        <span className="text-[13px] text-muted-foreground" role="status" data-testid="testall-running">
          Testing {n} {NOUN[p.entity]}{n === 1 ? "" : "s"}: up to 20 s each, 4 at a time, so this can take a while. Tests never save or enable a source.
        </span>
      ) : done.length > 0 ? (
        <span className="text-[13px] text-muted-foreground" data-testid="testall-summary">
          {ok} of {done.length} ok on this page
        </span>
      ) : (
        <span className="text-[13px] text-muted-foreground">Tests go out through the gateway (VPN guard, budgets, back-offs) and are audited.</span>
      )}
      {p.tests.errors["*"] ? <ExplainBox error={p.tests.errors["*"]} onReload={p.onReload} className="w-full" /> : null}
    </Card>
  );
}

function MoveButtons({ p, r, i, count, label }: { p: SourcesViewProps; r: SettingsRecord; i: number; count: number; label?: string }) {
  const ro = !!p.readOnly || p.filtered;
  return (
    <span className="inline-flex items-center gap-0.5">
      <Button size="icon-sm" variant="ghost" aria-label={`Move ${r.key} up${label ?? ""}`} disabled={ro || p.moving || i === 0} onClick={() => p.onMove(r, -1)} data-testid="move-up">
        <ArrowUp />
      </Button>
      <Button size="icon-sm" variant="ghost" aria-label={`Move ${r.key} down${label ?? ""}`} disabled={ro || p.moving || i === count - 1} onClick={() => p.onMove(r, 1)} data-testid="move-down">
        <ArrowDown />
      </Button>
    </span>
  );
}

function TestEdit({ p, r }: { p: SourcesViewProps; r: SettingsRecord }) {
  const ro = !!p.readOnly;
  return (
    <div className="flex flex-wrap items-center justify-end gap-1.5 sm:flex-nowrap">
      <Button size="sm" variant="outline" disabled={ro || !!p.tests.running[r.key]} onClick={() => p.onTest(r)} data-testid="test-one" aria-label={`Test ${r.key}`}>
        <Zap /> Test
      </Button>
      <Button size="sm" variant="outline" onClick={() => p.onEdit(r)} data-testid="edit" aria-label={ro ? `View ${r.key}` : `Edit ${r.key}`}>
        <Pencil /> {ro ? "View" : "Edit"}
      </Button>
    </div>
  );
}

/** Built-in indexer kinds that search without a key (policy.IndexerTakesKey in Go): their rows show no "credential not
 * set". Zilean takes an optional one, shown when set. */
const KEYLESS_KINDS = new Set(["torrentio", "zilean", "seadex"]);

const ADAPTER: Record<string, string> = { torznab: "Torznab", dmm: "DMM", torrentio: "Torrentio", zilean: "Zilean", seadex: "SeaDex", cardigann: "Catalog" };

function host(url: unknown): string {
  try {
    return typeof url === "string" && url ? new URL(url).host : "";
  } catch {
    return "";
  }
}

/** Where an indexer's searches go: its adapter and host, or the catalog definition it runs, with the reason when that
 * definition cannot feed the RSS sync (rssProblems, from the catalog). */
export function via(r: SettingsRecord, rssProblems?: Record<string, string>): string {
  const c = r.config ?? {};
  const kind = String(c.kind ?? r.kind ?? "");
  const name = ADAPTER[kind] ?? kind;
  const noRSS = kind === "cardigann" && c.definition ? rssProblems?.[String(c.definition)] : undefined;
  const parts = [kind === "cardigann" && c.definition ? `${name} · ${String(c.definition)}` : name, host(c.url), noRSS ? `no RSS: ${noRSS}` : ""];
  return parts.filter(Boolean).join(" · ");
}

/** The catalog definitions that cannot feed the RSS sync, by id, with the reason. */
export function rssProblemsOf(defs: CardigannDefinition[] | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const d of defs ?? []) if (d.rss === false) out[d.id] = d.rss_problem || "no search without keywords";
  return out;
}

/** Whether a record matches the filter box: key, kind, definition or address. */
export function matchesFilter(r: SettingsRecord, q: string): boolean {
  const t = q.trim().toLowerCase();
  if (!t) return true;
  return [r.key, r.kind, r.config?.definition, r.config?.url, r.config?.privacy].some((v) => typeof v === "string" && v.toLowerCase().includes(t));
}

/** The provider fallback order, as the versions use it: 1 RD › 2 AD › 3 TB. */
function FallbackOrder({ records }: { records: SettingsRecord[] }) {
  const short: Record<string, string> = { realdebrid: "RD", alldebrid: "AD", torbox: "TB" };
  const sorted = [...records].sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  return (
    <p className="mb-3 flex flex-wrap items-center gap-1.5 text-[13px]" data-testid="fallback-order" aria-label={`Fallback order: ${sorted.map((r) => r.key).join(", then ")}`}>
      <span className="mr-1 text-muted-foreground">Fallback order</span>
      {sorted.map((r, i) => (
        <span key={r.key} className="inline-flex items-center gap-1.5">
          {i > 0 ? <span aria-hidden className="text-muted-foreground">›</span> : null}
          <Badge tone={r.enabled ? "outline" : "neutral"} title={r.key}>
            {r.order} {short[r.kind ?? ""] ?? r.key}
          </Badge>
        </span>
      ))}
    </p>
  );
}

export function SourcesView(p: SourcesViewProps) {
  const { entity, records } = p;
  const [filter, setFilter] = useState("");
  const shown = records.filter((r) => matchesFilter(r, filter));
  const view: SourcesViewProps = { ...p, filtered: filter.trim() !== "" };
  return (
    <div data-testid={`${entity}-list`}>
      <Announcer announcement={p.announcement} testid="test-announce" />
      <TestAllBar {...p} />
      {p.moveError ? <ExplainBox error={p.moveError} onReload={p.onReload} className="mb-3" /> : null}
      {p.orderNote ? <p className="mb-3 text-[13px] text-[var(--warn-ink)]" data-testid="order-note">{p.orderNote}</p> : null}
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <p className="min-w-0 flex-1 basis-64 text-[13px] text-muted-foreground">
          {entity === "indexers"
            ? "Searched in priority order (lower first); the arrows swap neighbours. Requests use the gateway with the configured VPN requirements. Privacy is a label: the libraries that list an indexer decide where its releases go."
            : "Tried in this fallback order; the arrows move one place. Libraries list their providers in Settings, and the order here must agree."}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          {entity === "indexers" ? (
            <Input
              className="h-8 w-48"
              type="search"
              placeholder="Filter indexers…"
              aria-label="Filter indexers"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              data-testid="source-filter"
            />
          ) : null}
          <Button size="sm" onClick={() => p.onEdit("new")} disabled={!!p.readOnly} data-testid="add">
            <Plus /> Add {NOUN[entity]}
          </Button>
        </div>
      </div>
      {view.filtered ? <p className="mb-2 text-[12.5px] text-muted-foreground" data-testid="filter-note">{shown.length} of {records.length} shown; clear the filter to reorder.</p> : null}
      {records.length === 0 ? (
        <Card><Empty>No {entity} yet.</Empty></Card>
      ) : entity === "indexers" ? (
        <Card className="p-0">
          <Table>
            {/* Below sm each row stacks: the indexer, then privacy and last test beside the actions (cells drop the table height, which a grid would treat as fixed). */}
            <TableHeader className="max-sm:hidden">
              <TableRow>
                <TableHead>Indexer</TableHead>
                <TableHead className="hidden md:table-cell">Via</TableHead>
                <TableHead>Privacy</TableHead>
                <TableHead className="hidden sm:table-cell">Priority</TableHead>
                <TableHead>Last test</TableHead>
                <TableHead className="text-right"><span className="sr-only">Actions</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((r) => {
                const i = records.indexOf(r);
                const privacy = String(r.config?.privacy || "private");
                return (
                  <TableRow key={r.key} data-record={r.key} data-selected={p.selected === r.key ? "true" : undefined} className="max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:py-1 max-sm:[&>td]:h-auto">
                    <TableCell className="min-w-0 max-sm:col-span-2 max-sm:whitespace-normal max-sm:pb-1">
                      <Mono className="[overflow-wrap:anywhere]">{r.key}</Mono>
                      <div className="mt-1 flex flex-wrap gap-1">
                        <Badge tone={r.enabled ? "positive" : "neutral"} data-testid="enabled-state">{r.enabled ? "enabled" : "disabled"}</Badge>
                        {r.secret_set || !KEYLESS_KINDS.has(String(r.config?.kind ?? r.kind ?? "")) ? <Badge tone={r.secret_set ? "positive" : "neutral"} data-testid="row-secret">credential {r.secret_set ? "set" : "not set"}</Badge> : null}
                      </div>
                      <div className="mt-1 text-xs text-muted-foreground md:hidden">{via(r, p.rssProblems)}</div>
                      <div className="md:hidden"><IndexerRequests r={r} budgets={p.budgets} /></div>
                      <Backoff r={r} compact />
                    </TableCell>
                    <TableCell className="hidden max-w-[16rem] text-[13px] md:table-cell">
                      <span className="[overflow-wrap:anywhere]">{via(r, p.rssProblems)}</span>
                      {r.config?.budget ? <div className="text-xs text-muted-foreground">budget <Mono>{String(r.config.budget)}</Mono></div> : null}
                      <IndexerRequests r={r} budgets={p.budgets} />
                    </TableCell>
                    <TableCell className="max-sm:col-start-1 max-sm:py-1">
                      <Badge tone={privacy === "public" ? "positive" : "warning"}>{privacy}</Badge>
                    </TableCell>
                    <TableCell className="hidden whitespace-nowrap tabular-nums sm:table-cell">
                      <span className="mr-1 inline-block min-w-[1.5ch] text-right">{r.priority ?? 0}</span>
                      <MoveButtons p={view} r={r} i={i} count={records.length} />
                    </TableCell>
                    <TableCell className="max-w-[16rem] max-sm:col-start-1 max-sm:max-w-none max-sm:whitespace-normal max-sm:pt-1"><LastTest r={r} tests={p.tests} now={p.now} /></TableCell>
                    <TableCell className="sm:w-px max-sm:col-start-2 max-sm:row-span-2 max-sm:row-start-2 max-sm:self-end">
                      <TestEdit p={p} r={r} />
                      <div className="mt-1 flex justify-end sm:hidden"><MoveButtons p={view} r={r} i={i} count={records.length} /></div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      ) : (
        <>
          <FallbackOrder records={records} />
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {records.map((r, i) => (
              <Card key={r.key} data-record={r.key} data-selected={p.selected === r.key ? "true" : undefined} className="flex flex-col gap-3">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <h3 className="font-semibold">
                      <span className="mr-1.5 text-muted-foreground">{r.order}.</span>
                      {KIND[r.kind ?? ""] ?? r.kind} <Mono className="text-muted-foreground">{r.key}</Mono>
                    </h3>
                    <div className="mt-1 flex flex-wrap gap-1">
                      <Badge tone={r.enabled ? "positive" : "neutral"} data-testid="enabled-state">{r.enabled ? "enabled" : "disabled"}</Badge>
                      <Badge tone={r.secret_set ? "positive" : "negative"} data-testid="row-secret">credential {r.secret_set ? "set" : "not set"}</Badge>
                    </div>
                  </div>
                  <MoveButtons p={view} r={r} i={i} count={records.length} />
                </div>
                <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-[13px]">
                  <dt className="text-muted-foreground">Budget</dt>
                  <dd><Mono>{String(r.config?.budget ?? "—")}</Mono></dd>
                  <dt className="text-muted-foreground">Pacing</dt>
                  <dd className="break-words">{pacing(r.config?.pacing)}</dd>
                  <dt className="text-muted-foreground">Last test</dt>
                  <dd><LastTest r={r} tests={p.tests} now={p.now} /></dd>
                  <dt className="text-muted-foreground">Back-off</dt>
                  <dd><Backoff r={r} /></dd>
                </dl>
                <div className="mt-auto"><TestEdit p={p} r={r} /></div>
              </Card>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

/** The open editor of a panel, in a side sheet. The sheet focuses its heading on open and gives focus back to its
 * opener on close; after a delete, when that row is gone, to the neighbour editorReturn picks, or to the panel itself
 * (root, tabIndex -1). The record being edited is kept in ?edit=<key>. */
function useEditor(entity: Entity, records: SettingsRecord[], generation: number) {
  const [editing, setEditing] = useState<EditorSession | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const back = useRef<EditorReturn | null>(null);
  const start = (r: SettingsRecord | undefined, kind?: string) => {
    back.current = null;
    setEditing(openEditor(entity, r, generation, kind));
  };
  const setParam = useEditParam(true, !!editing, (key) => {
    const r = records.find((x) => x.key === key);
    if (key === "new" || r) start(r);
    else setParam(null);
  });
  const open = (r: SettingsRecord | "new", kind?: string) => {
    start(r === "new" ? undefined : r, kind);
    setParam(r === "new" ? "new" : r.key);
  };
  const close = (deleted?: boolean) => {
    back.current = editing && deleted ? editorReturn(editing, entity, records.map((r) => r.key), true) : null;
    setEditing(null);
    setParam(null);
  };
  const returnFocus = () => (back.current && root.current ? (returnTarget(root.current, back.current) as HTMLElement) : undefined);
  const label = editing?.record ? `Edit ${NOUN[entity]} ${editing.record.key}` : `Add ${NOUN[entity]}`;
  return { editing, open, close, root, heading, returnFocus, label, selected: editing?.record?.key };
}

function pacing(v: unknown): string {
  if (!v || typeof v !== "object") return "—";
  const p = v as Record<string, unknown>;
  const parts: string[] = [];
  if (p.adds_per_minute) parts.push(`${p.adds_per_minute} adds/min`);
  if (p.uncached_adds_per_hour) parts.push(`${p.uncached_adds_per_hour} uncached/h`);
  if (p.cooldown) parts.push(`cooldown ${p.cooldown}`);
  if (p.backoff_max) parts.push(`back-off up to ${p.backoff_max}`);
  if (p.min_gap) parts.push(`${p.min_gap} between adds`);
  return parts.join(" · ") || "—";
}

/** Indexers or providers: the list with tests, moves and the editor. */
export function SourcesPanel({
  entity,
  overview,
  ent,
  versions,
  readOnly,
}: {
  entity: SourceEntity;
  overview: SettingsOverview;
  ent: SchemaEntity;
  versions: { key: string; providers: string[] }[];
  readOnly?: string;
}) {
  const records = overview[entity];
  const catalog = useCardigannCatalog(entity === "indexers" && records.some((r) => (r.config?.kind ?? r.kind) === "cardigann"));
  const [tests, dispatchTest] = useReducer(testReducer, initialTests);
  const [announcement, announce] = useAnnouncement();
  // Every finished test is also said in the live region, once, with the text it had when it finished.
  const dispatch = (a: TestAction) => {
    dispatchTest(a);
    announce(testAnnouncement(a, NOUN[entity]));
  };
  const [includeDisabled, setIncludeDisabled] = useState(false);
  const ed = useEditor(entity, records, overview.generation);
  const [moveError, setMoveError] = useState<Explained>();
  const [orderNote, setOrderNote] = useState<string>();
  const write = useSettingsWrite();
  const confirm = useWriteConfirmation();
  const reload = useReloadSettings();
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: qk.settings.overview });

  const onTest = async (r: SettingsRecord) => {
    dispatch({ type: "start", key: r.key });
    try {
      dispatch({ type: "result", key: r.key, result: await testSaved(entity, r.id!) });
    } catch (e) {
      dispatch(testErrorAction(r.key, e));
    }
    refresh();
  };
  const onTestAll = async () => {
    const keys = records.filter((r) => includeDisabled || r.enabled).map((r) => r.key);
    dispatch({ type: "start-all", keys, at: Date.now() });
    try {
      dispatch({ type: "all-done", results: (await testAll(entity, includeDisabled)).results });
    } catch (e) {
      dispatch({ type: "all-error", error: explainError(e) });
    }
    refresh();
  };
  const onMove = (r: SettingsRecord, dir: -1 | 1) => {
    setMoveError(undefined);
    setOrderNote(undefined);
    if (entity === "indexers") {
      const priorities = movedPriorities(records, r.id!, dir);
      if (!priorities) return;
      const send = (token?: string) => write.mutate({ method: "PUT", path: "/indexers/priority", body: { generation: overview.generation, priorities }, token }, { onError: (e) => { if (!confirm.ask(e, send)) setMoveError(explainError(e)); } });
      send();
      return;
    }
    const order = movedOrder(records, r.id!, dir);
    if (!order) return;
    const keys = order.map((id) => records.find((x) => x.id === id)!.key);
    const conflicts = orderConflicts(keys, versions);
    if (conflicts.length > 0) {
      setOrderNote(
        `Not sent: ${conflicts.map((c) => `${c.version} lists ${c.providers.join(" › ")}`).join("; ")} in the policy files. ` +
          "The fallback order must agree with every library's provider list; change the libraries in the YAML first (library settings are YAML-owned).",
      );
      return;
    }
    const send = (token?: string) => write.mutate({ method: "PUT", path: "/providers/order", body: { generation: overview.generation, order }, token }, { onError: (e) => { if (!confirm.ask(e, send)) setMoveError(explainError(e)); } });
    send();
  };
  const nextOrder = Math.max(0, ...overview.providers.map((p) => p.order ?? 0)) + 1;
  // Indexers are added through the add-indexer sheet (catalog, built-in sources, Prowlarr migration); a built-in
  // source swaps it for the record editor in the same sheet, whose heading then takes focus.
  const [adding, setAdding] = useState(false);
  const swapped = useRef(false);
  const builtIn = (kind: string) => {
    swapped.current = true;
    setAdding(false);
    ed.open("new", kind);
  };
  useEffect(() => {
    if (swapped.current && ed.editing) ed.heading.current?.focus();
    swapped.current = false;
  }, [ed.editing, ed.heading]);
  return (
    <div ref={ed.root} tabIndex={-1} className="outline-none">
      {confirm.dialog}
      {entity === "indexers" ? <HoldsCard /> : null}
      {adding && !ed.editing ? (
        <Sheet label="Add indexer" onClose={() => setAdding(false)} initialFocus={ed.heading}>
          <AddIndexer overview={overview} kinds={ent.kinds ?? []} onBuiltIn={builtIn} onDone={() => setAdding(false)} headingRef={ed.heading} />
        </Sheet>
      ) : ed.editing ? (
        <Sheet label={ed.label} onClose={() => ed.close()} initialFocus={ed.heading} returnFocus={ed.returnFocus}>
          <RecordEditor
            key={editorKey(ed.editing)}
            entity={entity}
            ent={ent}
            session={ed.editing}
            nextOrder={nextOrder}
            records={records}
            providerKeys={overview.providers.map((r) => r.key)}
            readOnly={readOnly}
            onDone={ed.close}
            headingRef={ed.heading}
          />
        </Sheet>
      ) : null}
      <SourcesView
        entity={entity}
        records={records}
        readOnly={readOnly}
        tests={tests}
        includeDisabled={includeDisabled}
        onIncludeDisabled={setIncludeDisabled}
        onTest={(r) => void onTest(r)}
        onTestAll={() => void onTestAll()}
        onEdit={(r) => (r === "new" && entity === "indexers" ? setAdding(true) : ed.open(r))}
        selected={ed.selected}
        onMove={onMove}
        moving={write.isPending}
        moveError={moveError}
        onReload={() => {
          setMoveError(undefined);
          void reload();
        }}
        orderNote={orderNote}
        announcement={announcement}
        rssProblems={rssProblemsOf(catalog.data?.definitions)}
        budgets={overview.budgets}
      />
    </div>
  );
}

export type BudgetsViewProps = {
  records: SettingsRecord[];
  readOnly?: string;
  /** The record open in the sheet: its row stays marked. */
  selected?: string;
  onEdit: (r: SettingsRecord | "new") => void;
};

export function BudgetsView({ records, readOnly, onEdit, selected }: BudgetsViewProps) {
  return (
    <div data-testid="budgets-list">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <p className="text-[13px] text-muted-foreground">Request budgets shared by the sources that name them; use is live from the gateway (this hour · today).</p>
        <Button size="sm" onClick={() => onEdit("new")} disabled={!!readOnly} data-testid="add">
          <Plus /> Add budget
        </Button>
      </div>
      <Card className="p-0">
        <Table>
          {/* Below sm each budget stacks: its key and labelled values, with Edit beside them; nothing is dropped. */}
          <TableHeader className="max-sm:hidden">
            <TableRow>
              <TableHead>Budget</TableHead>
              <TableHead>Rate</TableHead>
              <TableHead className="hidden sm:table-cell">Caps</TableHead>
              <TableHead className="text-right">Hour</TableHead>
              <TableHead className="text-right">Today</TableHead>
              <TableHead>Used by</TableHead>
              <TableHead className="text-right"> </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {records.map((r) => {
              const c = r.config as { requests?: number; per?: string; burst?: number; hourly?: number; daily?: number };
              const u = r.usage as { used_hour?: number; used_day?: number; used_by?: string[]; hour_capped?: boolean; day_capped?: boolean; backoff?: { active?: boolean } } | undefined;
              return (
                <TableRow key={r.key} data-record={r.key} data-selected={selected === r.key ? "true" : undefined} className="max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:py-2 max-sm:[&>td]:h-auto max-sm:[&>td]:py-0.5">
                  <TableCell className="max-sm:col-start-1">
                    <Mono className="[overflow-wrap:anywhere]">{r.key}</Mono>
                    {u?.backoff?.active ? <Badge tone="warning" className="ml-2 max-sm:ml-0 max-sm:mt-1 max-sm:flex max-sm:w-fit">backed off</Badge> : null}
                    {u?.hour_capped || u?.day_capped ? <Badge tone="negative" className="ml-2 max-sm:ml-0 max-sm:mt-1 max-sm:flex max-sm:w-fit">cap reached</Badge> : null}
                  </TableCell>
                  <TableCell className="max-sm:col-start-1 sm:whitespace-nowrap"><span className="text-muted-foreground sm:hidden">Rate </span>{c.requests ?? "?"}/{c.per ?? "?"}{c.burst ? ` · burst ${c.burst}` : ""}</TableCell>
                  <TableCell className="max-sm:col-start-1 sm:whitespace-nowrap">
                    <span className="text-muted-foreground sm:hidden">Caps </span>{[c.hourly ? `${fmtNumber(c.hourly)}/h` : "", c.daily ? `${fmtNumber(c.daily)}/day` : ""].filter(Boolean).join(" · ") || <span className="text-muted-foreground">rate only</span>}
                  </TableCell>
                  <TableCell className="tabular-nums max-sm:col-start-1 sm:text-right" data-col="hour"><span className="text-muted-foreground sm:hidden">This hour </span>{u ? fmtNumber(u.used_hour) : "—"}</TableCell>
                  <TableCell className="tabular-nums max-sm:col-start-1 sm:text-right" data-col="day"><span className="text-muted-foreground sm:hidden">Today </span>{u ? fmtNumber(u.used_day) : "—"}</TableCell>
                  <TableCell className="max-w-[16rem] text-[13px] text-muted-foreground max-sm:col-start-1 max-sm:max-w-none"><span className="text-muted-foreground sm:hidden">Used by </span>{(u?.used_by ?? []).join(", ") || "—"}</TableCell>
                  <TableCell className="text-right max-sm:col-start-2 max-sm:row-start-1">
                    <Button size="xs" variant="outline" onClick={() => onEdit(r)} data-testid="edit" aria-label={readOnly ? `View ${r.key}` : `Edit ${r.key}`}>
                      <Pencil /> {readOnly ? "View" : "Edit"}
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}

export function BudgetsPanel({ overview, ent, readOnly }: { overview: SettingsOverview; ent: SchemaEntity; readOnly?: string }) {
  const entity: Entity = "budgets";
  const ed = useEditor(entity, overview.budgets, overview.generation);
  return (
    <div ref={ed.root} tabIndex={-1} className="outline-none">
      {ed.editing ? (
        <Sheet label={ed.label} onClose={() => ed.close()} initialFocus={ed.heading} returnFocus={ed.returnFocus}>
          <RecordEditor
            key={editorKey(ed.editing)}
            entity={entity}
            ent={ent}
            session={ed.editing}
            nextOrder={0}
            readOnly={readOnly}
            onDone={ed.close}
            headingRef={ed.heading}
          />
        </Sheet>
      ) : null}
      <BudgetsView records={overview.budgets} readOnly={readOnly} onEdit={ed.open} selected={ed.selected} />
    </div>
  );
}
