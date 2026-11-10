import { Empty, Loading, ErrorState } from "@/components/page";
import { Link } from "react-router";
import { settingHref } from "@/lib/settings-map";
// The add-indexer sheet (docs/design/settings.md, "Adding an indexer"): three tabs. Catalog lists the cardigann
// definitions Theatarr runs itself, with a set-up form built from the chosen definition's settings; Torznab & built-in sources
// opens the record editor for torznab/newznab, dmm, torrentio, zilean or seadex; Migrate from Prowlarr (once) turns
// Prowlarr's indexers into native ones. Whatever is added here is saved disabled, with a budget of its own, and joins
// no version: enabling it and listing it in a version stay separate steps. The one exception: a catalog set-up whose
// test passed offers Add and enable, which saves it enabled. The held definition updates are listed by
// HoldsCard, on the indexers page.
import { ArrowLeft, Loader2, Plus, Zap } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { ApiError } from "@/api/client";
import {
  prowlarrProposals,
  useApproveHold,
  useCardigannCatalog,
  useCardigannHolds,
  type CardigannDefinition,
  type CardigannHold,
  type CardigannSetting,
  type ProwlarrProposal,
} from "@/api/cardigann";
import { testCandidate, useSettingsWrite, type SettingsOverview, type SettingsTestResult, type SettingsWrite } from "@/api/settings";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { SheetLayout } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  catalogLanguages,
  contentLabel,
  filterCatalog,
  inCredential,
  isInfo,
  NO_FILTER,
  proposalWrite,
  setupForm,
  setupWrite,
  writeBodies,
  type CatalogFilter,
  type IndexerWrite,
  type SetupForm,
} from "@/lib/cardigann";
import { fmtAgo } from "@/lib/format";
import { cn } from "@/lib/utils";
import { explainError, type Explained } from "@/lib/settings";
import { Announcer, ExplainBox, Field, SELECT_CLASS, TestResult, useAnnouncement } from "./parts";

const STATUS: Record<string, { label: string; tone: "positive" | "info" | "warning" | "negative" }> = {
  runs: { label: "runs", tone: "positive" },
  needs_account: { label: "needs account", tone: "info" },
  needs_flaresolverr: { label: "needs FlareSolverr", tone: "warning" },
  unsupported: { label: "unsupported", tone: "negative" },
};

/** What each built-in adapter is, for the Torznab & built-in sources tab. */
const BUILT_IN: Record<string, [string, string]> = {
  torznab: ["Torznab / Newznab", "Any Torznab or Newznab feed URL: Jackett, Prowlarr, a usenet indexer."],
  dmm: ["DMM", "Debrid Media Manager's cached feeds."],
  torrentio: ["Torrentio", "The Stremio addon's search; public."],
  zilean: ["Zilean", "The DMM hash database; public."],
  seadex: ["SeaDex", "The best anime releases, with its scoring table."],
};

function catalogOff(e: unknown): boolean {
  return e instanceof ApiError && e.status === 503;
}

const PrivacyBadge = ({ privacy }: { privacy: string }) => <Badge tone={privacy === "public" ? "positive" : "warning"}>{privacy}</Badge>;

type Tab = "catalog" | "builtin" | "prowlarr";

/** The sheet's content. onBuiltIn swaps it for the record editor with that kind; onDone closes it (added: the key). */
export function AddIndexer({
  overview,
  kinds,
  onBuiltIn,
  onDone,
  headingRef,
}: {
  overview: SettingsOverview;
  /** The indexer kinds the settings schema offers. */
  kinds: string[];
  onBuiltIn: (kind: string) => void;
  onDone: (added?: string) => void;
  headingRef?: RefObject<HTMLHeadingElement | null>;
}) {
  // The tab the admin picked. Until then the sheet opens on the catalog, or on the Torznab and built-in sources when
  // the catalog is off: a Prowlarr or Jackett feed needs no catalog, and an empty first tab hid that.
  const [picked, setTab] = useState<Tab>();
  const catalog = useCardigannCatalog();
  const tab = picked ?? (catalog.data?.unavailable || catalogOff(catalog.error) ? "builtin" : "catalog");
  const [setup, setSetup] = useState<CardigannDefinition>();
  // The set-up view and the list replace each other in the sheet: the new heading takes focus, as on open.
  const swapped = useRef(false);
  useEffect(() => {
    if (swapped.current) headingRef?.current?.focus();
    swapped.current = false;
  }, [setup, headingRef]);
  const show = (d: CardigannDefinition | undefined) => {
    swapped.current = true;
    setSetup(d);
  };
  if (setup) return <SetupView def={setup} overview={overview} onBack={() => show(undefined)} onDone={onDone} headingRef={headingRef} />;
  return (
    <SheetLayout
      testid="add-indexer"
      headingRef={headingRef}
      title="Add indexer"
      subtitle="Added in no library: add it to libraries afterwards. A catalog indexer whose test passes can be added enabled; the rest start disabled."
      onClose={() => onDone()}
      footer={<Button type="button" size="sm" variant="outline" onClick={() => onDone()} data-testid="add-close">Close</Button>}
    >
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList className="flex w-full flex-wrap justify-start" aria-label="How to add an indexer">
          <TabsTrigger value="catalog" data-testid="tab-catalog">Catalog</TabsTrigger>
          <TabsTrigger value="builtin" data-testid="tab-builtin">Torznab &amp; built-in sources</TabsTrigger>
          <TabsTrigger value="prowlarr" data-testid="tab-prowlarr">Migrate from Prowlarr (once)</TabsTrigger>
        </TabsList>
        <TabsContent value="catalog">
          <CatalogTab onSetUp={show} />
        </TabsContent>
        <TabsContent value="builtin">
          <div className="grid gap-2 sm:grid-cols-2" data-testid="builtin-list">
            {kinds.filter((k) => k !== "cardigann").map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => onBuiltIn(k)}
                data-testid={`builtin-${k}`}
                className="rounded-xl border border-[var(--control)] bg-[var(--surface)] p-3 text-left outline-none transition hover:border-[var(--line-focus)] focus-visible:ring-2 focus-visible:ring-[var(--line-focus)]"
              >
                <div className="text-sm font-semibold text-[var(--ink)]">{BUILT_IN[k]?.[0] ?? k}</div>
                <div className="mt-0.5 text-[12.5px] text-muted-foreground">{BUILT_IN[k]?.[1] ?? `A ${k} source.`}</div>
              </button>
            ))}
          </div>
        </TabsContent>
        <TabsContent value="prowlarr">
          <MigrateTab overview={overview} />
        </TabsContent>
      </Tabs>
    </SheetLayout>
  );
}

function CatalogTab({ onSetUp }: { onSetUp: (d: CardigannDefinition) => void }) {
  const catalog = useCardigannCatalog();
  const [f, setF] = useState<CatalogFilter>(NO_FILTER);
  const defs = useMemo(() => catalog.data?.definitions ?? [], [catalog.data]);
  const shown = useMemo(() => filterCatalog(defs, f), [defs, f]);
  const languages = useMemo(() => catalogLanguages(defs), [defs]);
  if (catalog.isPending) return <p className="text-sm text-muted-foreground"><Loader2 className="mr-1 inline size-4 animate-spin" aria-hidden />Loading the catalog…</p>;
  if (catalog.error || catalog.data.unavailable) {
    return !catalog.error || catalogOff(catalog.error) ? (
      <p className="text-sm text-muted-foreground" data-testid="catalog-off">
        The indexer catalog is off. Turn it on in <a href={settingHref("cardigann", "definitions")}>Indexers › Catalog</a>: Turn the catalog on, then Save. A Prowlarr, Jackett or other Torznab feed needs no catalog: add it on the Torznab &amp; built-in sources tab.
      </p>
    ) : (
      <ExplainBox error={explainError(catalog.error)} />
    );
  }
  const st = catalog.data.status;
  const set = (p: Partial<CatalogFilter>) => setF((x) => ({ ...x, ...p }));
  return (
    <div data-testid="catalog">
      <p className="mb-2 text-[12.5px] text-muted-foreground" data-testid="catalog-status">
        Prowlarr's Cardigann definitions, run by Theatarr itself: {catalog.data.total} definitions
        {st.commit ? <> at <code>{st.commit.slice(0, 7)}</code></> : null}
        {st.updated_at ? `, updated ${fmtAgo(st.updated_at)}` : ""}. Privacy comes from the definition and cannot be lowered.
        {st.last_error ? <span className="text-[var(--warn-ink)]"> Last check failed: {st.last_error}</span> : null}
      </p>
      <div className="mb-2 grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
        <Input className="col-span-2 h-9 min-w-0 sm:flex-1 sm:basis-48" type="search" placeholder={`Search ${catalog.data.total} definitions…`} aria-label="Search the catalog" value={f.q} onChange={(e) => set({ q: e.target.value })} data-testid="catalog-search" />
        <select className={cn(SELECT_CLASS, "sm:w-auto")} aria-label="Privacy" value={f.privacy} onChange={(e) => set({ privacy: e.target.value })} data-testid="catalog-privacy">
          <option value="">Any privacy</option>
          <option value="public">public</option>
          <option value="private">private</option>
        </select>
        <select className={cn(SELECT_CLASS, "sm:w-auto")} aria-label="Content" value={f.category} onChange={(e) => set({ category: e.target.value as CatalogFilter["category"] })} data-testid="catalog-category">
          <option value="">Any content</option>
          <option value="movies">Movies</option>
          <option value="tv">TV</option>
          <option value="anime">Anime</option>
        </select>
        <select className={cn(SELECT_CLASS, "sm:w-auto")} aria-label="Language" value={f.language} onChange={(e) => set({ language: e.target.value })} data-testid="catalog-language">
          <option value="">Any language</option>
          {languages.map((l) => <option key={l} value={l}>{l}</option>)}
        </select>
        <select className={cn(SELECT_CLASS, "sm:w-auto")} aria-label="Support" value={f.runsHere ? "runs" : "all"} onChange={(e) => set({ runsHere: e.target.value === "runs" })} data-testid="catalog-runs">
          <option value="runs">Runs here</option>
          <option value="all">All</option>
        </select>
      </div>
      <p className="mb-2 text-[12.5px] text-muted-foreground" role="status" data-testid="catalog-count">{shown.length} of {defs.length} shown</p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Site</TableHead>
            <TableHead className="hidden sm:table-cell">Privacy</TableHead>
            <TableHead className="hidden md:table-cell">Language · content</TableHead>
            <TableHead className="hidden sm:table-cell">Here</TableHead>
            <TableHead><span className="sr-only">Actions</span></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {shown.slice(0, 200).map((d) => {
            const s = STATUS[d.status] ?? { label: d.status, tone: "info" as const };
            return (
              <TableRow key={d.id} data-definition={d.id}>
                <TableCell className="min-w-[11rem] whitespace-normal">
                  <div className="font-medium [overflow-wrap:anywhere]">{d.name}</div>
                  <div className="text-[12px] text-muted-foreground [overflow-wrap:anywhere]">{d.description || d.id}</div>
                  <div className="mt-1 flex flex-wrap gap-1 sm:hidden">
                    <PrivacyBadge privacy={d.privacy} />
                    <Badge tone={s.tone}>{s.label}</Badge>
                  </div>
                </TableCell>
                <TableCell className="hidden sm:table-cell"><PrivacyBadge privacy={d.privacy} /></TableCell>
                <TableCell className="hidden text-[12.5px] md:table-cell">{d.language} · {contentLabel(d.categories)}</TableCell>
                <TableCell className="hidden sm:table-cell"><Badge tone={s.tone}>{s.label}</Badge></TableCell>
                <TableCell className="text-right">
                  {d.status === "unsupported" ? null : (
                    <Button size="sm" variant="outline" onClick={() => onSetUp(d)} aria-label={`Set up ${d.name}`} data-testid="set-up">Set up</Button>
                  )}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      {shown.length > 200 ? <p className="mt-2 text-[12.5px] text-muted-foreground">The first 200 are listed; search to narrow it down.</p> : null}
    </div>
  );
}

/** The budget an add writes, or why the key cannot have one of its own. */
function budgetState(overview: SettingsOverview, key: string): { exists: boolean; usedBy?: string } {
  const exists = overview.budgets.some((b) => b.key === key);
  const usedBy = overview.indexers.find((i) => i.config?.budget === key)?.key;
  return { exists, usedBy };
}

/** Writes the indexer's budget (unless it exists) and then the indexer, disabled unless enabled is asked for (the
 * set-up view, after its test passed). Returns the new generation. */
async function addIndexer(write: ReturnType<typeof useSettingsWrite>, overview: SettingsOverview, w: IndexerWrite, generation: number, budgetMade = false, enabled = false): Promise<number> {
  const b = budgetState(overview, w.key);
  if (b.usedBy) throw new Error(`The budget ${w.key} is already used by indexer ${b.usedBy}; a catalog indexer needs one of its own. Pick another key.`);
  const bodies = writeBodies(w, generation, b.exists || budgetMade);
  let gen = generation;
  if (bodies.budget) gen = ((await write.mutateAsync({ method: "POST", path: "/budgets", body: bodies.budget })) as SettingsWrite).generation ?? gen;
  return ((await write.mutateAsync({ method: "POST", path: "/indexers", body: { ...bodies.indexer(gen), enabled } })) as SettingsWrite).generation ?? gen;
}

function SetupView({
  def,
  overview,
  onBack,
  onDone,
  headingRef,
}: {
  def: CardigannDefinition;
  overview: SettingsOverview;
  onBack: () => void;
  onDone: (added?: string) => void;
  headingRef?: RefObject<HTMLHeadingElement | null>;
}) {
  const taken = { indexers: overview.indexers.map((i) => i.key), budgets: overview.budgets.map((b) => b.key) };
  const [form, setForm] = useState<SetupForm>(() => setupForm(def, taken.indexers));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState<Explained>();
  const [test, setTest] = useState<SettingsTestResult>();
  // The form the shown test ran on: Add and enable is offered only while the form still holds exactly it.
  const [tested, setTested] = useState<SetupForm>();
  const [busy, setBusy] = useState<"" | "test" | "add" | "enable">("");
  // The budget keys this view wrote (by Test search), so Add doesn't write them again before the overview reloads.
  const [made, setMade] = useState<string[]>([]);
  const [announcement, announce] = useAnnouncement();
  const write = useSettingsWrite();
  const set = (p: Partial<SetupForm>) => setForm((f) => ({ ...f, ...p }));
  const setValue = (name: string, v: string | boolean) => setForm((f) => ({ ...f, values: { ...f.values, [name]: v } }));

  const build = () => {
    const r = setupWrite(def, form, taken);
    setErrors(r.errors);
    setError(undefined);
    if (!r.write) announce("Fix the marked fields.");
    return r.write;
  };
  // The candidate test searches on the live budgets, so the indexer's own budget is written first; it limits nothing
  // else, and Add reuses it.
  const onTest = async () => {
    const w = build();
    if (!w) return;
    setBusy("test");
    setTest(undefined);
    try {
      const b = budgetState(overview, w.key);
      if (b.usedBy) throw new Error(`The budget ${w.key} is already used by indexer ${b.usedBy}; a catalog indexer needs one of its own. Pick another key.`);
      const bodies = writeBodies(w, overview.generation, b.exists || made.includes(w.key));
      if (bodies.budget) {
        await write.mutateAsync({ method: "POST", path: "/budgets", body: bodies.budget });
        setMade((m) => [...m, w.key]);
      }
      const body = bodies.indexer(0);
      delete body.generation;
      const res = await testCandidate("indexers", body);
      setTest(res);
      setTested(form);
      announce(`Test finished: ${res.status}.`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 429 && e.body) setTest(e.body as SettingsTestResult);
      else setError(explainError(e));
      announce("The test did not run.");
    } finally {
      setBusy("");
    }
  };
  const passed = test?.status === "ok" && tested === form;
  const onAdd = async (enabled = false) => {
    const w = build();
    if (!w) return;
    setBusy(enabled ? "enable" : "add");
    try {
      await addIndexer(write, overview, w, overview.generation, made.includes(w.key), enabled);
      onDone(w.key);
    } catch (e) {
      setError(explainError(e));
      announce("The indexer was not added.");
      setBusy("");
    }
  };

  const settings = def.settings.filter((s) => !isInfo(s));
  const notes = def.settings.filter(isInfo);
  const key = form.key.trim();
  return (
    <SheetLayout
      testid="setup"
      data-definition={def.id}
      headingRef={headingRef}
      title={`Set up ${def.name}`}
      subtitle={`definition ${def.id} · in no library`}
      onClose={() => onDone()}
      footer={
        <>
          <Button type="button" size="sm" variant="outline" onClick={onBack} data-testid="setup-back"><ArrowLeft /> Catalog</Button>
          <Button type="button" size="sm" variant="outline" onClick={() => void onTest()} disabled={!!busy} data-testid="setup-test">
            {busy === "test" ? <Loader2 className="animate-spin" /> : <Zap />} Test search
          </Button>
          <Button type="button" size="sm" variant={passed ? "outline" : "default"} onClick={() => void onAdd()} disabled={!!busy} className="ml-auto" data-testid="setup-add">
            {busy === "add" ? <Loader2 className="animate-spin" /> : <Plus />} Add (disabled)
          </Button>
          {passed ? (
            <Button type="button" size="sm" onClick={() => void onAdd(true)} disabled={!!busy} data-testid="setup-add-enable">
              {busy === "enable" ? <Loader2 className="animate-spin" /> : <Plus />} Add and enable
            </Button>
          ) : null}
        </>
      }
    >
      <div className="space-y-4">
        <Announcer announcement={announcement} testid="setup-announce" />
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-semibold">{def.name}</span>
          <PrivacyBadge privacy={def.privacy} />
          <span className="text-muted-foreground">{def.language} · {contentLabel(def.categories)}</span>
          <Badge tone={STATUS[def.status]?.tone ?? "info"}>{STATUS[def.status]?.label ?? def.status}</Badge>
        </div>
        {error ? <><ExplainBox error={error} />{overview.source === "db" && error.title === "The indexer catalog is off" ? <a href={settingHref("cardigann", "definitions")}>Indexers › Catalog</a> : null}</> : null}
        <div className="grid gap-3 sm:grid-cols-2">
          {settings.filter(inCredential).map((s) => <SettingField key={s.name} s={s} value={form.values[s.name]} error={errors[`setting.${s.name}`]} onChange={(v) => setValue(s.name, v)} />)}
          <p className="text-sm">Enabled: Off. After a passed Test search, Add and enable turns it on as it is added.</p>
        </div>
        <details open={Object.keys(errors).length > 0 || undefined}>
          <summary className="cursor-pointer text-sm font-medium">Show advanced</summary>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
          {settings.filter(s => !inCredential(s)).map((s) => <SettingField key={s.name} s={s} value={form.values[s.name]} error={errors[`setting.${s.name}`]} onChange={(v) => setValue(s.name, v)} />)}
          <Field label="Key" error={errors.key} help="The indexer's name in Theatarr; its budget gets the same key.">
            <Input value={form.key} onChange={(e) => set({ key: e.target.value })} aria-invalid={!!errors.key} data-testid="setup-key" />
          </Field>
          <Field label="Site address" error={errors.url}>
            <select className={SELECT_CLASS} value={form.url} onChange={(e) => set({ url: e.target.value })} data-testid="setup-url">
              {def.links.map((l) => <option key={l} value={l}>{l}</option>)}
            </select>
          </Field>
          <Field label="Privacy" help="A label from the definition; read-only.">
            <Input value={def.privacy} readOnly data-testid="setup-privacy" />
          </Field>
          <Field label="Priority" error={errors.priority} help="Lower is searched first.">
            <Input inputMode="numeric" value={form.priority} onChange={(e) => set({ priority: e.target.value })} aria-invalid={!!errors.priority} />
          </Field>
          <Field label="Budget: requests" error={errors.requests} help="This site's own budget; nothing else uses it.">
            <Input inputMode="numeric" value={form.requests} onChange={(e) => set({ requests: e.target.value })} aria-invalid={!!errors.requests} />
          </Field>
          <Field label="Budget: per" error={errors.per} help="A duration: 1m, 30s, 1h.">
            <Input value={form.per} onChange={(e) => set({ per: e.target.value })} aria-invalid={!!errors.per} />
          </Field>
        </div>
        </details>
        {def.hosts?.length ? (
          <p className="text-[12.5px] text-muted-foreground" data-testid="setup-hosts">Searches also reach {def.hosts.join(", ")}; the indexer lists them, so the gateway allows them like the site.</p>
        ) : null}
        {notes.map((n) => (
          <div key={n.name} className="rounded-xl border border-[var(--line)] p-3 text-[12.5px]" data-testid="setup-note">
            <div className="font-medium">{n.label}</div>
            <p className="mt-0.5 text-muted-foreground">{typeof n.default === "string" ? n.default : ""}</p>
          </div>
        ))}
        {def.problems?.length ? <ul className="list-disc pl-5 text-[12.5px] text-[var(--warn-ink)]">{def.problems.map((p) => <li key={p}>{p}</li>)}</ul> : null}
        {def.rss === false && def.supported ? (
          <p className="text-[12.5px] text-[var(--warn-ink)]" data-testid="setup-no-rss">No RSS: {def.rss_problem}. The RSS sync skips it; it is searched per title only.</p>
        ) : null}
        <p className="text-[12.5px] text-muted-foreground">
          Fields come from the definition's settings; the secret ones go in the indexer's credential, write-only. Every request uses
          the gateway, and the test requires a fresh VPN check when indexers are listed in require_vpn_for.
          {budgetState(overview, key).exists || made.includes(key) ? ` The budget ${key} exists and is reused.` : " Test search writes the budget first; Add reuses it."}
        </p>
        {test ? <div data-testid="setup-test-result"><TestResult result={test} /></div> : null}
      </div>
    </SheetLayout>
  );
}

function SettingField({ s, value, error, onChange }: { s: CardigannSetting; value: string | boolean | undefined; error?: string; onChange: (v: string | boolean) => void }) {
  const label = `${s.label}${s.required ? " (required)" : ""}`;
  if (s.type === "checkbox") {
    return (
      <label className="flex items-center gap-2 text-sm sm:col-span-2">
        <input type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} className="size-4 accent-[var(--accent-hover)]" />
        {s.label}
      </label>
    );
  }
  if (s.type === "select" && s.options?.length) {
    return (
      <Field label={label} error={error}>
        <select className={SELECT_CLASS} value={typeof value === "string" ? value : ""} onChange={(e) => onChange(e.target.value)}>
          {s.options.map((o) => <option key={o.value} value={o.value}>{o.label || o.value}</option>)}
        </select>
      </Field>
    );
  }
  const secret = inCredential(s);
  return (
    <Field label={label} error={error} help={secret ? "Goes in the credential: stored write-only, never shown again." : undefined}>
      <Input
        type={s.type === "password" ? "password" : "text"}
        autoComplete={s.type === "password" ? "new-password" : "off"}
        ignorePasswordManagers={secret}
        value={typeof value === "string" ? value : ""}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={!!error}
        data-testid={`setting-${s.name}`}
      />
    </Field>
  );
}

type RowResult = { ok: boolean; text: string };

function MigrateTab({ overview }: { overview: SettingsOverview }) {
  const [proposals, setProposals] = useState<ProwlarrProposal[]>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Explained>();
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [results, setResults] = useState<Record<string, RowResult>>({});
  const [busy, setBusy] = useState(false);
  const [announcement, announce] = useAnnouncement();
  const write = useSettingsWrite();
  const existing = overview.indexers.map((i) => i.key);
  const addable = (p: ProwlarrProposal) => p.status === "proposed" && !!p.key && !existing.includes(p.key) && !results[p.key]?.ok;

  const read = async () => {
    setLoading(true);
    setError(undefined);
    try {
      const ps = await prowlarrProposals();
      setProposals(ps);
      setPicked(Object.fromEntries(ps.filter(addable).map((p) => [p.key!, true])));
    } catch (e) {
      setError(catalogOff(e) ? { kind: "other", title: "The indexer catalog is off", message: "The indexer catalog is off. Turn it on in Indexers › Catalog: Turn the catalog on, then Save." } : explainError(e));
    } finally {
      setLoading(false);
    }
  };
  const chosen = (proposals ?? []).filter((p) => addable(p) && picked[p.key!]);
  const migrate = async () => {
    setBusy(true);
    let gen = overview.generation;
    let priority = Math.max(0, ...overview.indexers.map((i) => i.priority ?? 0)) + 1;
    let added = 0;
    for (const p of chosen) {
      const w = proposalWrite(p, priority);
      if (!w) continue;
      try {
        gen = await addIndexer(write, overview, w, gen);
        setResults((r) => ({ ...r, [p.key!]: { ok: true, text: "added, disabled" } }));
        priority++;
        added++;
      } catch (e) {
        setResults((r) => ({ ...r, [p.key!]: { ok: false, text: explainError(e).message } }));
      }
    }
    setBusy(false);
    announce(`${added} of ${chosen.length} added, disabled.`);
  };
  return (
    <div className="space-y-3" data-testid="migrate-tab">
      <Announcer announcement={announcement} testid="migrate-announce" />
      <div className="rounded-xl border border-[var(--th-info-border)] bg-[var(--accent-soft)] p-3 text-[13px] text-[var(--accent-ink)]">
        <b>One-off migration.</b> Reads Prowlarr's indexer list once (through the gateway) and proposes a native indexer from the
        catalog for each one with a Cardigann definition. Only the ticked proposals are added, each disabled with a budget of its
        own and in no library; the Prowlarr feeds and the libraries stay as they are. Privacy is the stricter of Prowlarr's and the
        definition's.
      </div>
      {error ? <><ExplainBox error={error} />{overview.source === "db" && error.title === "The indexer catalog is off" ? <a href={settingHref("cardigann", "definitions")}>Indexers › Catalog</a> : null}</> : null}
      {!proposals ? (
        <Button type="button" size="sm" variant="outline" onClick={() => void read()} disabled={loading} data-testid="read-prowlarr">
          {loading ? <Loader2 className="animate-spin" /> : null} Read Prowlarr's indexers
        </Button>
      ) : proposals.length === 0 ? (
        <p className="text-sm text-muted-foreground">Prowlarr has no indexers.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead><span className="sr-only">Add</span></TableHead>
              <TableHead>Prowlarr indexer</TableHead>
              <TableHead className="hidden sm:table-cell">Definition</TableHead>
              <TableHead>Privacy</TableHead>
              <TableHead>Result</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {proposals.map((p) => {
              const can = addable(p);
              const res = p.key ? results[p.key] : undefined;
              return (
                <TableRow key={p.prowlarr.id} data-proposal={p.key ?? p.prowlarr.name} data-status={p.status}>
                  <TableCell>
                    {can ? (
                      <input type="checkbox" className="size-4 accent-[var(--accent-hover)]" checked={!!picked[p.key!]} disabled={busy}
                        onChange={(e) => setPicked((x) => ({ ...x, [p.key!]: e.target.checked }))} aria-label={`Add ${p.prowlarr.name} as ${p.key}`} />
                    ) : null}
                  </TableCell>
                  <TableCell className="min-w-[10rem] whitespace-normal">
                    <div className="font-medium [overflow-wrap:anywhere]">{p.prowlarr.name} <span className="text-[12px] text-muted-foreground">#{p.prowlarr.id}</span></div>
                    {p.key ? <div className="font-mono text-[12px] text-muted-foreground">{p.key}</div> : null}
                    {p.secrets?.length ? <div className="text-[12px] text-[var(--warn-ink)]">Needs its credential afterwards: {p.secrets.join(", ")}</div> : null}
                    {p.notes?.map((n) => <div key={n} className="text-[12px] text-muted-foreground">{n}</div>)}
                  </TableCell>
                  <TableCell className="hidden font-mono text-[12px] sm:table-cell">{p.indexer?.definition ?? p.prowlarr.definition_name}</TableCell>
                  <TableCell><PrivacyBadge privacy={p.indexer?.privacy ?? p.prowlarr.privacy} /></TableCell>
                  <TableCell className="whitespace-normal text-[12.5px]" data-testid="proposal-result">
                    {res ? (
                      <span className={res.ok ? "text-[var(--ok-ink)]" : "text-[var(--bad-ink)]"}>{res.text}</span>
                    ) : p.status !== "proposed" ? (
                      <span className="text-muted-foreground">{p.status === "no_definition" ? "no definition: stays behind Prowlarr" : p.status}</span>
                    ) : p.key && existing.includes(p.key) ? (
                      <span className="text-muted-foreground">{p.key} exists</span>
                    ) : (
                      <span className="text-muted-foreground">proposed</span>
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
      {proposals?.some(addable) ? (
        <Button type="button" size="sm" disabled={busy || chosen.length === 0} onClick={() => void migrate()} data-testid="migrate">
          {busy ? <Loader2 className="animate-spin" /> : null} Add {chosen.length} disabled
        </Button>
      ) : null}
    </div>
  );
}

/** The definition updates held for review: approving applies one update (or removal); no indexer changes. Hidden
 * while there are none or the catalog is off. */
export function DefinitionHolds({ showEmpty = false }: { showEmpty?: boolean }) {
  const holds = useCardigannHolds();
  const approve = useApproveHold();
  const [asking, setAsking] = useState<CardigannHold>();
  if (showEmpty && holds.isPending) return <Loading />;
  if (showEmpty && holds.error) return <ErrorState error={holds.error} />;
  if (!holds.data?.length) return showEmpty ? <Empty>No definition updates waiting.</Empty> : null;
  return (
    <Card className="mb-3 p-3" data-testid="holds">
      <div className="text-sm font-semibold">Definition updates waiting for review</div>
      <p className="mb-2 text-[12.5px] text-muted-foreground">These definitions are in use; their updates are held until approved.</p>
      <ul className="space-y-2">
        {holds.data.map((h) => (
          <li key={h.id} className="flex flex-wrap items-start justify-between gap-2 rounded-lg border border-[var(--line)] p-2.5" data-hold={h.id}>
            <div className="min-w-0 flex-1 text-[13px]">
              <div className="font-medium">
                <code>{h.id}</code> {h.removed ? <Badge tone="warning">removed upstream</Badge> : null}
                <span className="ml-1 text-[12px] text-muted-foreground">since {fmtAgo(h.since)} · {h.commit.slice(0, 7)}</span>
              </div>
              <div className="text-[12.5px] text-muted-foreground">{h.reasons.join("; ")}</div>
              {h.detail.length ? <ul className="mt-1 list-disc pl-5 text-[12px] text-muted-foreground">{h.detail.map((d) => <li key={d}>{d}</li>)}</ul> : null}
            </div>
            <Button size="sm" variant="outline" onClick={() => { approve.reset(); setAsking(h); }} aria-label={`Review ${h.id}`} data-testid="hold-review">Approve…</Button>
          </li>
        ))}
      </ul>
      {asking ? (
        <ConfirmDialog
          testid="hold-confirm"
          danger={!!asking.removed}
          subject={`Definition ${asking.id}`}
          scope={{
            title: asking.removed ? `Remove definition ${asking.id}?` : `Apply the update of ${asking.id}?`,
            does: [asking.removed ? "Drops the definition: indexers using it stop working." : "Applies the held definition text; searches use it from now on."],
            keeps: ["No indexer's configuration changes."],
            confirm: asking.removed ? "Remove" : "Approve",
          }}
          busy={approve.isPending}
          error={approve.error ?? undefined}
          onClose={() => setAsking(undefined)}
          onConfirm={() => approve.mutate(asking.id, { onSuccess: () => setAsking(undefined) })}
        />
      ) : null}
    </Card>
  );
}

export function HoldsCard() {
  const holds = useCardigannHolds();
  return holds.data?.length ? <p className="mb-3 text-sm">{holds.data.length} definition updates waiting — <Link className="text-primary underline" to="/admin/reviews/indexers">Review</Link></p> : null;
}
