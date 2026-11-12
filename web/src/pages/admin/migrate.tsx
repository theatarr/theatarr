// Admin › Setup › Migrate from arrs (docs/migration.md). Source keys stay in this component until a plan is saved.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { api, ApiError, type Schemas } from "@/api/client";
import { apply, inspect, verify, type Inventory, type Plan, type PlanOptions, type PlanView, type Sources, type Step, type Summary, type VerifyReport } from "@/api/migrate";
import { qk } from "@/api/query-keys";
import { ArrTabs } from "./arr-tabs";
import { ErrorState, Loading, PageHeader } from "@/components/page";
import { SELECT_CLASS } from "@/components/settings/parts";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Checkpoints, Gaps, InventoryView, PlanSteps, selectable, Verification } from "./migrate-parts";
import { Field } from "./setup";

const labels = ["Sources", "Inspect", "Plan", "Apply", "Verify"];
const route = "/admin/advanced/arr/migrate";
type SourceDraft = { id: number; name: string; flavour: "sonarr" | "radarr"; url: string; key: string };
type ServiceDraft = { url: string; key: string };
type Pending = { steps: string[]; token?: string; effects: { version: string; effect: string }[] };

export function MigratePage() {
  const [params] = useSearchParams();
  const [restart, setRestart] = useState(0);
  const raw = params.get("plan");
  const id = raw && /^[1-9]\d*$/.test(raw) ? Number(raw) : undefined;
  const qc = useQueryClient();
  const saved = useQuery({ queryKey: qk.migrate.plan(id ?? ""), queryFn: () => api<PlanView>(`/migrate/plans/${id}`), enabled: !!id });
  if (raw && !id) return <ErrorState error={new Error("Invalid migration plan id.")} />;
  if (id && saved.isPending) return <Loading />;
  if (id && saved.error) return <ErrorState error={saved.error} />;
  return <Wizard key={`${id ?? "new"}:${restart}`} initial={id ? saved.data : undefined} onRestart={() => setRestart((n) => n + 1)} onSaved={(view) => {
    qc.setQueryData(qk.migrate.plan(view.plan.id), view);
    void qc.invalidateQueries({ queryKey: qk.migrate.list });
    // The Start another migration action remounts the wizard, including on this same URL.
  }} />;
}

function resumeStep(plan?: Plan): number {
  if (!plan) return 0;
  const automatic = (plan.steps ?? []).filter((s) => s.automatic);
  if (automatic.every((s) => s.status === "done")) return 4;
  return plan.checkpoints?.length || automatic.some((s) => s.status !== "pending") ? 3 : 2;
}

function Wizard({ initial, onSaved, onRestart }: { initial?: PlanView; onSaved: (view: PlanView) => void; onRestart: () => void }) {
  const qc = useQueryClient();
  const plans = useQuery({ queryKey: qk.migrate.list, queryFn: () => api<Summary[]>("/migrate/plans") });
  const [step, setStep] = useState(() => resumeStep(initial?.plan));
  const heading = useRef<HTMLHeadingElement>(null);
  const nextID = useRef(1);
  const [arr, setArr] = useState<SourceDraft[]>([{ id: 0, name: "radarr", flavour: "radarr", url: "", key: "" }]);
  const [prowlarr, setProwlarr] = useState<ServiceDraft>({ url: "", key: "" });
  const [seerr, setSeerr] = useState<ServiceDraft>({ url: "", key: "" });
  const [recyclarr, setRecyclarr] = useState("");
  const [inventory, setInventory] = useState<Inventory | undefined>(initial?.inventory);
  const [plan, setPlan] = useState<Plan | undefined>(initial?.plan);
  const [options, setOptions] = useState<PlanOptions>(initial?.plan.options ?? { name: "", native: [], skip: [] });
  const [selected, setSelected] = useState(() => (initial?.plan.steps ?? []).filter(selectable).map((s) => s.id));
  const [report, setReport] = useState<VerifyReport>();
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<Record<number, unknown>>({});
  const [confirmation, setConfirmation] = useState<Pending>();
  const saved = !!plan?.id;
  useEffect(() => { heading.current?.focus(); }, [step]);

  const available = (n: number) => !busy && (n === 0 ? !saved : n < 3 ? !!inventory : saved);
  const move = (n: number) => { if (available(n)) setStep(n); };
  function navigate(event: KeyboardEvent<HTMLElement>) {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const index = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + buttons.length) % buttons.length;
    event.preventDefault(); buttons[index]?.focus();
  }
  function editSources() { setInventory(undefined); setPlan(undefined); setReport(undefined); }
  function updateArr(id: number, values: Partial<SourceDraft>) { editSources(); setArr(arr.map((a) => a.id === id ? { ...a, ...values } : a)); }
  function sourceRequest(): { sources: Sources; keys: Record<string, string> } {
    const keys: Record<string, string> = {};
    const sources: Sources = { arr: arr.map((a) => {
      const key = `ARR_${a.id}_KEY`; keys[key] = a.key;
      return { name: a.name, flavour: a.flavour, url: a.url.trim(), key };
    }) };
    if (prowlarr.url.trim()) { sources.prowlarr = { url: prowlarr.url.trim(), key: "PROWLARR_KEY" }; keys.PROWLARR_KEY = prowlarr.key; }
    if (seerr.url.trim()) { sources.seerr = { url: seerr.url.trim(), key: "SEERR_KEY" }; keys.SEERR_KEY = seerr.key; }
    if (recyclarr.trim()) sources.recyclarr = recyclarr.trim();
    return { sources, keys };
  }
  async function read(save: boolean) {
    const at = save ? 2 : 1;
    setStep(at); setBusy(true); setErrors((e) => ({ ...e, [at]: undefined }));
    try {
      const { sources, keys } = sourceRequest();
      const result = await inspect(sources, keys, options, save);
      if (!result.plan) throw new Error("Inspect did not return a plan preview.");
      const { plan: next, ...inv } = result;
      setInventory(inv); setPlan(next); setSelected((next.steps ?? []).filter(selectable).map((s) => s.id));
      if (save) {
        setArr([]); setProwlarr({ url: "", key: "" }); setSeerr({ url: "", key: "" });
        onSaved({ plan: next, inventory: inv });
      }
    } catch (error) { setErrors((e) => ({ ...e, [at]: error })); }
    finally { setBusy(false); }
  }
  function select(s: Step, checked: boolean) {
    const next = new Set(selected);
    const steps = plan?.steps ?? [];
    if (checked) {
      const add = (current: Step) => {
        if (next.has(current.id)) return;
        if (selectable(current)) next.add(current.id);
        for (const id of current.depends_on ?? []) { const dep = steps.find((x) => x.id === id); if (dep) add(dep); }
      };
      add(s);
    } else {
      next.delete(s.id);
      let changed = true;
      while (changed) {
        changed = false;
        for (const current of steps) {
          if (next.has(current.id) && current.depends_on?.some((id) => !next.has(id) && steps.find((x) => x.id === id)?.status !== "done")) {
            next.delete(current.id); changed = true;
          }
        }
      }
    }
    setSelected([...next]);
  }
  async function runApply(pending?: Pending) {
    if (!plan?.id || !selected.length) return;
    setBusy(true); setErrors((e) => ({ ...e, 3: undefined })); setConfirmation(undefined);
    try {
      const next = await apply(plan.id, selected, pending?.steps, pending?.token);
      setPlan(next); setReport(undefined);
      qc.setQueryData(qk.migrate.plan(plan.id), { plan: next, inventory });
      void qc.invalidateQueries({ queryKey: qk.migrate.list });
      void qc.invalidateQueries({ queryKey: qk.settings.all });
      const needs = (next.steps ?? []).filter((s) => selected.includes(s.id) && s.status === "needs_confirmation" && s.confirmation);
      // Review one stored token at a time: a settings change can invalidate the next token's generation.
      if (needs[0]) setConfirmation({ steps: [needs[0].id], token: needs[0].confirmation?.token, effects: needs[0].confirmation?.effects ?? [] });
    } catch (error) {
      const body = error instanceof ApiError && error.status === 422 ? error.body as { confirmation?: Schemas["SettingsConfirmation"]; token?: string; effects?: Pending["effects"]; step?: string; plan?: Plan } | undefined : undefined;
      const token = body?.confirmation?.token ?? body?.token;
      const effects = body?.confirmation?.effects ?? body?.effects;
      if (token && effects) {
        if (body?.plan) {
          setPlan(body.plan);
          qc.setQueryData(qk.migrate.plan(body.plan.id), { plan: body.plan, inventory });
          void qc.invalidateQueries({ queryKey: qk.migrate.list });
        }
        if (body?.plan) setPlan(body.plan);
        setConfirmation({ steps: body?.step ? [body.step] : selected, token, effects });
      } else setErrors((e) => ({ ...e, 3: error }));
    } finally { setBusy(false); }
  }
  async function runVerify() {
    if (!plan?.id) return;
    setBusy(true); setErrors((e) => ({ ...e, 4: undefined }));
    try { setReport(await verify(plan.id)); }
    catch (error) { setErrors((e) => ({ ...e, 4: error })); }
    finally { setBusy(false); }
  }

  return <div className="min-w-0 space-y-4 [overflow-wrap:anywhere]">
    <PageHeader crumb={[{ label: "Admin", to: "/admin" }, { label: "Advanced", to: "/admin/advanced" }, { label: "Sonarr/Radarr migration", to: "/admin/advanced/arr" }]} title="Migrate" description={<>Coming from the arrs? <Link className="underline" to="/admin/settings">Find a setting</Link> knows the Sonarr, Radarr and Prowlarr page names and shows where each moved. Inspect, review and apply a migration with its libraries Watch only, then verify it before planning a handover. To bring in titles and the files already on your debrid mount, use <Link className="underline" to="/admin/arr-import">Import from Sonarr/Radarr</Link>.</>} />
    <ArrTabs />
    <Card className="space-y-2 text-sm"><h2 className="font-semibold">Saved plans</h2>
      {plans.isPending ? <Loading /> : plans.error ? <><StepError error={plans.error} /><Button variant="outline" onClick={() => void plans.refetch()}>Reload plans</Button></> : !plans.data?.length ? <p>No saved plans yet.</p> : <ul className="space-y-1">{plans.data.map((p) => <li key={p.id}><Link className="underline" to={`${route}?plan=${p.id}`}>Resume {p.name} (#{p.id})</Link> · {Object.entries(p.steps).map(([status, count]) => `${count} ${status.replaceAll("_", " ")}`).join(" · ")}</li>)}</ul>}
      {saved ? <Link className="inline-block underline" to={route} onClick={onRestart}>Start another migration</Link> : null}
    </Card>
    <nav aria-label="Migration steps" onKeyDown={navigate}><ol className="flex flex-wrap gap-2">{labels.map((label, n) => <li key={label}><Button variant={step === n ? "default" : "outline"} size="sm" disabled={!available(n)} aria-current={step === n ? "step" : undefined} onClick={() => move(n)}>{n + 1}. {label}</Button></li>)}</ol></nav>
    <Card className="min-w-0 space-y-4 text-sm" aria-busy={busy}>
      <h2 ref={heading} tabIndex={-1} className="text-lg font-semibold outline-none">{step + 1}. {labels[step]}{saved ? ` · ${plan.name}` : ""}</h2>
      {step === 0 ? <div className="space-y-3">
        <p>Keys are write-only, sent only to inspect and kept in memory until the plan is saved or you leave this page.</p>
        {arr.map((a, index) => <fieldset key={a.id} className="grid min-w-0 gap-2 rounded-lg border border-border p-3 sm:grid-cols-2"><legend className="px-1 font-medium">Arr instance {index + 1}</legend>
          <Field label={`Instance ${index + 1} name`}><Input value={a.name} onChange={(e) => updateArr(a.id, { name: e.target.value })} /></Field>
          <Field label={`Instance ${index + 1} type`}><select className={SELECT_CLASS} value={a.flavour} onChange={(e) => updateArr(a.id, { flavour: e.target.value as SourceDraft["flavour"] })}><option value="radarr">Radarr</option><option value="sonarr">Sonarr</option></select></Field>
          <Field label={`Instance ${index + 1} URL`}><Input type="url" value={a.url} onChange={(e) => updateArr(a.id, { url: e.target.value })} /></Field>
          <Field label={`Instance ${index + 1} API key`}><Input type="password" ignorePasswordManagers value={a.key} onChange={(e) => updateArr(a.id, { key: e.target.value })} /></Field>
          <Button variant="ghost" className="w-fit" onClick={() => { editSources(); setArr(arr.filter((x) => x.id !== a.id)); }}>Remove instance {index + 1}</Button>
        </fieldset>)}
        <Button variant="outline" onClick={() => { editSources(); setArr([...arr, { id: nextID.current++, name: "", flavour: "sonarr", url: "", key: "" }]); }}>Add Sonarr/Radarr instance</Button>
        <ServiceFields label="Prowlarr" value={prowlarr} onChange={(v) => { editSources(); setProwlarr(v); }} />
        <ServiceFields label="Seerr / Overseerr" value={seerr} onChange={(v) => { editSources(); setSeerr(v); }} />
        <Field label="Recyclarr config path (on the Theatarr server)"><Input value={recyclarr} onChange={(e) => { editSources(); setRecyclarr(e.target.value); }} placeholder="/config/recyclarr.yml" /></Field>
        <Button disabled={busy} onClick={() => void read(false)}>Run inspect</Button>
      </div> : null}
      {step === 1 ? <>{busy ? <p role="status">Reading sources…</p> : null}{inventory && plan ? <><InventoryView inventory={inventory} plan={plan} /><Button onClick={() => move(2)}>Continue to plan</Button></> : <Button disabled={busy} onClick={() => void read(false)}>Retry inspect</Button>}</> : null}
      {step === 2 ? <>
        {!saved ? <div className="space-y-3"><Field label="Plan name"><Input value={options.name ?? ""} onChange={(e) => setOptions({ ...options, name: e.target.value })} /></Field>
          <p>Choose per Prowlarr indexer. Native uses a runnable catalog definition where available; otherwise the plan explains its fallback.</p>
          {(inventory?.prowlarr?.indexers ?? []).map((ix) => <Field key={ix.id} label={`${ix.name} migration`}><select className={SELECT_CLASS} value={options.skip?.includes(ix.name) ? "skip" : options.native?.includes(ix.name) ? "native" : "prowlarr"} onChange={(e) => setOptions({ ...options, native: [...(options.native ?? []).filter((n) => n !== ix.name), ...(e.target.value === "native" ? [ix.name] : [])], skip: [...(options.skip ?? []).filter((n) => n !== ix.name), ...(e.target.value === "skip" ? [ix.name] : [])] })}><option value="prowlarr">Keep Prowlarr</option><option value="native">Native catalog</option><option value="skip">Skip</option></select></Field>)}
          <p>Creating the plan reads the sources again with these choices. Credentials for migrated feeds and providers can be entered later under Settings › Indexers and Settings › Download clients.</p>
          <Button disabled={busy} onClick={() => void read(true)}>Create plan</Button>
        </div> : <p>Review the saved plan and choose which automatic steps to apply. Dependencies are selected together. Manual and blocked steps need attention outside the wizard.</p>}
        {plan && saved ? <>{(plan.indexers ?? []).map((ix) => <p key={ix.key}>{ix.name}: {ix.choice}{ix.notes?.length ? ` · ${ix.notes.join(" · ")}` : ""}</p>)}<Gaps gaps={plan.gaps} /><PlanSteps plan={plan} selected={selected} onSelect={select} /></> : null}
        {saved ? <Button onClick={() => move(3)}>Continue to apply</Button> : null}
      </> : null}
      {step === 3 && plan ? <>
        <p>Apply takes a backup before changing settings. New libraries start Watch only with their downloads off; indexers and providers start disabled.</p>
        <PlanSteps plan={plan} selected={selected} onSelect={select} /><Checkpoints plan={plan} />
        <div className="flex flex-wrap gap-2"><Button disabled={busy || !selected.some((id) => plan.steps?.some((s) => s.id === id && selectable(s)))} onClick={() => void runApply()}>Apply selected steps</Button><Button variant="outline" disabled={busy} onClick={() => move(4)}>Continue to verify</Button></div>
      </> : null}
      {step === 4 ? <><Button disabled={busy} onClick={() => void runVerify()}>{busy ? "Verifying…" : "Run verify"}</Button>{report ? <Verification report={report} /> : null}
        <Card className="space-y-2"><h3 className="font-semibold">Watch only first, Live later</h3><p>Review the pending work and compare Watch only decisions before handing over one library at a time. Follow <a className="underline" href="https://github.com/theatarr/theatarr/blob/main/docs/migration.md#2-shadow-compare-with-the-arr" target="_blank" rel="noreferrer">docs/migration.md</a> from stage 2 for the follower library, the go-live check, the handover and rollback.</p><p><Link className="underline" to="/admin/settings/libraries">Settings › Libraries</Link> · <Link className="underline" to="/admin/advanced/arr/comparison">Migration comparison</Link></p></Card>
      </> : null}
      {errors[step] ? <StepError error={errors[step]} /> : null}
    </Card>
    {confirmation ? <ConfirmDialog danger={false} subject={plan?.name} scope={{ title: "Confirm migration effects", does: confirmation.effects.map((e) => (e.version ? `${e.version}: ${e.effect}` : e.effect)), keeps: ["Old Sonarr, Radarr, Prowlarr and Seerr settings.", "Router and library switches; new libraries stay Watch only with their downloads off."], confirm: "Confirm and apply" }} busy={busy} onClose={() => setConfirmation(undefined)} onConfirm={() => void runApply(confirmation)} /> : null}
  </div>;
}

function ServiceFields({ label, value, onChange }: { label: string; value: ServiceDraft; onChange: (v: ServiceDraft) => void }) {
  return <fieldset className="grid min-w-0 gap-2 rounded-lg border border-border p-3 sm:grid-cols-2"><legend className="px-1 font-medium">{label} (optional)</legend>
    <Field label={`${label} URL`}><Input type="url" value={value.url} onChange={(e) => onChange({ ...value, url: e.target.value })} /></Field>
    <Field label={`${label} API key`}><Input type="password" ignorePasswordManagers value={value.key} onChange={(e) => onChange({ ...value, key: e.target.value })} /></Field>
  </fieldset>;
}

function StepError({ error }: { error: unknown }) {
  const body = error instanceof ApiError ? error.body as { fields?: { path: string; message: string }[] } | undefined : undefined;
  return <div role="alert" className="space-y-1 text-[var(--bad-ink)]"><p>{errText(error)}</p>
    {error instanceof ApiError ? (error.detail ?? []).map((d, i) => <p key={i}>{d}</p>) : null}
    {body?.fields?.map((f, i) => <p key={i}>{f.path}: {f.message}</p>)}
  </div>;
}
