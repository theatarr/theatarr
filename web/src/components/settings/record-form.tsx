import { ConfigFields } from "./field-input";
// The create/edit form for an indexer, provider or budget, rendered from GET /settings/schema inside the side sheet
// (components/ui/sheet.tsx). RecordFormView is the plain view (tested directly); RecordEditor holds its state and
// talks to the API: save with the generation the draft was read at (its session, fixed when the editor opens), an
// unsaved test of the candidate, and delete with its confirmation dialog on top of the sheet. Providers state the
// orphan choice on every update and delete, false unless the operator ticks it.
import { Loader2, Save, Trash2, Zap } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { qk } from "@/api/query-keys";
import { useSettingsEditor } from "./use-settings-editor";
import {
  settingsOverview,
  testCandidate,
  useReloadSettings,
  useSettingsWrite,
  type Entity,
  type SchemaEntity,
  type SettingsRecord,
  type SettingsTestResult,
} from "@/api/settings";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SheetLayout } from "@/components/ui/sheet";
import type { Scope } from "@/lib/ops";
import {
  PROVIDER_SERVICES,
  candidateBody,
  editorFields,
  editorFrom,
  serviceURL,
  editorMismatch,
  explainError,
  hasAdvancedValues,
  needsCredentialAgain,
  rebaseEditor,
  RECORD_GONE,
  sessionDelete,
  sessionInput,
  testAnnouncement,
  testErrorAction,
  writeBody,
  type EditorSession,
  type EditorState,
  type Explained,
  type TestAction,
} from "@/lib/settings";
import type { VersionRecord } from "@/api/settings-versions";
import { matchingLibraries } from "@/lib/cardigann";
import { IndexerLibraries } from "./indexer-libraries";
import { changedPaths } from "./section-draft";
import { changeCount } from "./unsaved-bar";
import { Announcer, ChangedMark, ExplainBox, Field, NamedChoices, SecretField, SELECT_CLASS, TestResult, useAnnouncement, type Announcement } from "./parts";

export const NOUN: Record<Entity, string> = { indexers: "indexer", providers: "provider", budgets: "budget" };

function fieldErrors(error: unknown): Record<string, string> {
  const body = error instanceof Error && "body" in error ? (error as { body?: unknown }).body : undefined;
  const fields = body && typeof body === "object" ? (body as { fields?: unknown }).fields : undefined;
  if (!Array.isArray(fields)) return {};
  return Object.fromEntries(fields.flatMap((field) => {
    if (!field || typeof field !== "object") return [];
    const item = field as { path?: unknown; message?: unknown };
    if (typeof item.path !== "string" || typeof item.message !== "string") return [];
    const path = item.path.replace(/^.*\.config\./, "").replace(/^config\./, "").replace(/^(?:indexers|providers|budgets)\.[^.]+\./, "");
    return [[path, item.message]];
  }));
}

export type RecordFormProps = {
  entity: Entity;
  ent: SchemaEntity;
  records?: SettingsRecord[];
  /** The provider keys an indexer's allowlist chooses from. */
  providerKeys?: string[];
  /** A new indexer: the libraries it can join (its Libraries checklist). */
  libraries?: VersionRecord[];
  record?: SettingsRecord;
  state: EditorState;
  onState: (s: EditorState) => void;
  errors: Record<string, string>;
  /** Why the form cannot be saved (yaml mode); every control is disabled. */
  readOnly?: string;
  saving?: boolean;
  testing?: boolean;
  error?: Explained;
  test?: SettingsTestResult;
  onSave: () => void;
  /** Offered beside a passed test of exactly these values while Enabled is off: turns Enabled on and saves, in one write. */
  onEnableSave?: () => void;
  onTest?: () => void;
  onCancel: () => void;
  onReload?: () => void;
  /** After a conflict: reload the record but keep the draft (onReload then discards it). */
  onReloadKeep?: () => void;
  /** The draft was kept over a reload: the generation its save now targets. */
  kept?: number;
  /** Delete opens its confirmation dialog (RecordEditor's). */
  onAskDelete?: () => void;
  deleting?: boolean;
  now?: number;
  /** The heading, focused when the sheet opens (it has tabIndex -1). */
  headingRef?: RefObject<HTMLHeadingElement | null>;
  /** The latest candidate test outcome for the live region. */
  announcement?: Announcement;
  /** The saved record as an editor state (a new record: its first draft): edited fields are marked and the footer
   * counts the changes, with Discard. */
  saved?: EditorState;
  /** Discard: back to saved, clearing the validation that belonged to the discarded draft. */
  onDiscard?: () => void;
};

/** The edits a record draft holds against its saved state (the typed credential counts once, as its action; a new
 * record's name counts too). */
export function recordChanges(st: EditorState, saved: EditorState): number {
  const view = (s: EditorState) => ({ key: s.key, kind: s.kind, enabled: s.enabled, priority: s.priority, config: s.config, secret: s.secretAction, orphan: s.orphan });
  return changedPaths(view(st), view(saved)).length;
}

/** What "fail its active downloads" means, said once for the save and the delete. */
export function OrphanChoice({ checked, onChange, disabled, children }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; children?: ReactNode }) {
  return (
    <div className="rounded-lg border border-[var(--th-warning-border)] bg-[var(--warn-soft)] p-3 text-[13px] sm:col-span-2" data-testid="orphan-box">
      <Choice checked={checked} onChange={onChange} disabled={disabled} testid="orphan-confirm">
        <span className="font-medium text-[var(--ink)]">Fail its active downloads</span> (orphan_active_downloads)
      </Choice>
      <p className="mt-1.5 pl-6 text-muted-foreground">
        {children ??
          "Leave this off and a save that disables or removes this provider while it has active downloads, or changes its account so that the new one cannot see some of them, is refused; nothing changes. Tick it to go ahead anyway: those downloads are marked failed (\"provider removed by settings\" or \"provider account changed by settings\", not blocklisted) and the router stops following them."}
      </p>
    </div>
  );
}

const keys = (text: string) => text.split(",").map((k) => k.trim()).filter(Boolean);

/** An indexer's provider allowlist: empty is any provider the library lists; otherwise only the ticked ones. Saved as
 * the same comma-separated text the field held. */
function ProviderAllowlist({ value, saved, providers, only, setOnly, onChange, disabled, error, help }: {
  value: string; saved?: string; providers: string[]; only: boolean; setOnly: (v: boolean | null) => void; onChange: (v: string) => void; disabled?: boolean; error?: string; help?: string;
}) {
  const chosen = keys(value);
  // "Only these" stays chosen with nothing ticked (the form refuses to save that, as it would mean Any).
  const showChecks = only;
  const name = useId();
  const changed = saved !== undefined && keys(saved).join(",") !== chosen.join(",");
  return (
    <NamedChoices legend="Providers" testid="indexer-providers" disabled={disabled} help={<>{help ? <>{help} </> : null}{error ? <span role="alert" className="text-destructive">{error}</span> : null}</>}
      options={showChecks ? providers.map((key) => ({ key })) : []} value={showChecks ? chosen : []} onChange={(next) => { setOnly(true); onChange(next.join(", ")); }}>
      <label className="flex items-center gap-2">
        <input type="radio" name={name} className="size-4 accent-[var(--accent)]" checked={!showChecks} disabled={disabled} onChange={() => { setOnly(false); onChange(""); }} />
        Any provider chosen by the library
      </label>
      <label className="flex items-center gap-2">
        <input type="radio" name={name} className="size-4 accent-[var(--accent)]" checked={showChecks} disabled={disabled} onChange={() => setOnly(true)} />
        Only these providers
      </label>
      {changed ? <ChangedMark name="Providers" disabled={disabled} onUndo={() => { setOnly(null); onChange(saved); }} /> : null}
    </NamedChoices>
  );
}

export function RecordFormView(p: RecordFormProps) {
  const { entity, ent, record, state: st, onState, errors, readOnly } = p;
  const disabled = !!readOnly || p.saving || p.deleting;
  const set = (patch: Partial<EditorState>) => onState({ ...st, ...patch });
  const fields = editorFields(entity, ent, st.kind);
  // An indexer's required fields (its URL) come before Show advanced with the name; the rest is tuning. An indexer's
  // provider allowlist is named checkboxes instead of a comma-separated field.
  const allowlist = entity === "indexers" ? fields.find((f) => f.name === "providers" && f.type === "list") : undefined;
  const upFront = entity === "indexers";
  const required = upFront ? fields.filter((f) => f.required) : [];
  const tuning = fields.filter((f) => !required.includes(f) && f !== allowlist);
  // "Only these providers" with none ticked would save as Any, so the save is refused with an inline error instead.
  const [onlyChoice, setOnlyChoice] = useState<boolean | null>(null);
  const [allowlistError, setAllowlistError] = useState("");
  const chosenProviders = keys(String(st.config.providers ?? ""));
  const onlyProviders = onlyChoice ?? chosenProviders.length > 0;
  const configChange = (config: EditorState["config"]) => { if (config.url !== st.config.url) urlEdited.current = true; set({ config }); };
  const hasSecret = entity !== "budgets";
  const needsAgain =
    hasSecret &&
    !!record &&
    needsCredentialAgain(
      record,
      { url: (st.config.url as string) || undefined, kind: st.kind || undefined, definition: st.config.definition, hosts: st.config.hosts },
      st.secretAction,
    );
  const urlEdited = useRef(false);
  const keyEdited = useRef(false);
  const saved = p.saved;
  const changes = saved ? recordChanges(st, saved) : 0;
  const headingId = useId();
  const formId = useId();
  // Save and Enable and save share the form's own check.
  const submit = (save: () => void) => {
    if (allowlist && onlyProviders && !chosenProviders.length) { setAllowlistError("Tick at least one provider, or choose Any."); return; }
    if (!disabled) save();
  };
  const nameField = record ? null : (
    <Field changed={!!saved && st.key !== saved.key} onUndo={() => { if (saved) { keyEdited.current = false; set({ key: saved.key }); } }} disabled={disabled} name="Name" label={<>Name <small className="font-normal text-muted-foreground">key</small> <span className="text-[var(--bad-ink)]">*</span></>} help="Stable name the libraries and budgets refer to; cannot be renamed later." error={errors.key}>
      <Input className="h-9" value={st.key} disabled={disabled} onChange={(e) => { keyEdited.current = true; set({ key: e.target.value }); }} data-field="key" />
    </Field>
  );
  return (
    <SheetLayout
      testid="record-form"
      data-entity={entity}
      data-mode={record ? "edit" : "create"}
      headingId={headingId}
      headingRef={p.headingRef}
      title={record ? <>Edit {NOUN[entity]} <span className="font-mono text-[15px]">{record.key}</span></> : `Add ${NOUN[entity]}`}
      subtitle={record ? [record.kind, record.updated_at ? `changed ${record.updated_at.slice(0, 16).replace("T", " ")} UTC` : ""].filter(Boolean).join(" · ") : entity === "budgets" ? undefined : entity === "indexers" ? "Fill in and save. Test checks the values without saving." : "Fill in, press Test, then Enable and save."}
      onClose={p.onCancel}
      footer={
        <>
          <Button type="submit" form={formId} size="sm" disabled={disabled} data-testid="record-save">
            {p.saving ? <Loader2 className="animate-spin" /> : <Save />} Save
          </Button>
          {p.onTest ? (
            <Button type="button" size="sm" variant="outline" disabled={disabled || p.testing || needsAgain} onClick={p.onTest} data-testid="record-test">
              {p.testing ? <Loader2 className="animate-spin" /> : <Zap />} Test these values
            </Button>
          ) : null}
          <Button type="button" size="sm" variant="outline" onClick={p.onCancel} data-testid="record-cancel">Cancel</Button>
          {changes ? (
            <>
              <span className="text-[13px] font-medium text-[var(--ink)]" data-testid="unsaved-count" aria-live="polite">{changeCount(changes)}</span>
              <Button type="button" size="sm" variant="ghost" disabled={disabled} onClick={() => { urlEdited.current = false; keyEdited.current = false; setOnlyChoice(null); setAllowlistError(""); if (p.onDiscard) p.onDiscard(); else if (saved) onState({ ...saved }); }} data-testid="record-discard">Discard</Button>
            </>
          ) : null}
          {record && p.onAskDelete ? (
            <Button type="button" size="sm" variant="destructive" className="ml-auto" disabled={disabled} onClick={p.onAskDelete} data-testid="record-delete">
              <Trash2 /> Delete
            </Button>
          ) : null}
        </>
      }
    >
      {readOnly ? <p className="mb-3 text-[13px] text-[var(--warn-ink)]" data-testid="form-readonly">{readOnly}</p> : null}
      <form
        id={formId}
        className="grid gap-3 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          submit(p.onSave);
        }}
      >
        {/* Service drives a new record's name and URL, so it has a Changed mark but no Undo; Discard resets all. */}
        {entity !== "budgets" ? (
          <Field label="Service" changed={!!saved && st.kind !== saved.kind} disabled={disabled} error={errors.kind} help={ent.config.find((f) => f.name === "kind")?.help}>
            <select className={SELECT_CLASS} value={st.kind} disabled={disabled} onChange={(e) => {
              const kind = e.target.value;
              set({ kind, ...(!record ? { key: keyEdited.current ? st.key : nextRecordKey(kind, p.records), config: { ...st.config, url: urlEdited.current ? st.config.url : serviceURL(entity, kind) },
                ...(entity === "indexers" && p.libraries ? { libraries: matchingLibraries({ kind }, p.libraries) } : {}) } : {}) });
            }} data-field="kind">
              {(ent.kinds ?? []).map((k) => <option key={k} value={k}>{PROVIDER_SERVICES[k]?.label ?? k}</option>)}
            </select>
          </Field>
        ) : null}
        {entity !== "budgets" ? (
          <div className="flex flex-col justify-end gap-1 text-sm">
            <Choice checked={st.enabled} onChange={(v) => set({ enabled: v })} disabled={disabled} testid="record-enabled">
              Enabled <span className="text-muted-foreground">{record || entity === "indexers" ? "" : "(off until you turn it on)"}</span>
            </Choice>
            {saved && st.enabled !== saved.enabled ? <ChangedMark name="Enabled" disabled={disabled} onUndo={() => set({ enabled: saved.enabled })} /> : null}
          </div>
        ) : null}
        {hasSecret ? (
          <SecretField
            label="API key"
            set={!!record?.secret_set}
            action={st.secretAction}
            value={st.secretValue}
            onAction={(a) => set({ secretAction: a, secretValue: "" })}
            onValue={(v) => set({ secretValue: v })}
            disabled={disabled}
            needsAgain={needsAgain}
            error={errors.secret}
          />
        ) : null}
        {upFront ? nameField : null}
        {required.length ? <ConfigFields fields={required} values={st.config} saved={saved?.config} errors={errors} onChange={configChange} disabled={disabled} /> : null}
        {!record && entity === "indexers" && st.kind === "dmm" ? (
          <p className="text-[12.5px] text-muted-foreground sm:col-span-2" data-testid="dmm-url-hint">
            DMM has no prefilled address: its Service URL is DMM’s Torznab feed, <code>https://debridmediamanager.com/api/torznab/cached</code>, or with <code>rd</code>, <code>ad</code> or <code>tb</code> before <code>/cached</code> for one provider’s cache. It needs your DMM API key; DMM gives keys only to paying GitHub sponsors.
          </p>
        ) : null}
        {!record && entity === "indexers" && p.libraries ? (
          <IndexerLibraries libraries={p.libraries} value={st.libraries} onChange={(libraries) => set({ libraries })} disabled={disabled} />
        ) : null}
        <details className="sm:col-span-2" open={entity === "budgets" || (!!record && hasAdvancedValues(entity, ent, record)) || Object.keys(errors).some(k => k !== "secret" && k !== "kind") || !!allowlistError || undefined}>
          <summary className="cursor-pointer text-sm font-medium">Show advanced</summary>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            {upFront ? null : nameField}
            {entity === "indexers" ? (
              <Field changed={!!saved && st.priority !== saved.priority} onUndo={() => saved && set({ priority: saved.priority })} disabled={disabled} name="Search priority" label={<>Search priority <small className="font-normal text-muted-foreground">priority</small></>} help="Search order; lower numbers are searched first." error={errors.priority}>
                <Input className="h-9" inputMode="numeric" value={st.priority} disabled={disabled} onChange={(e) => set({ priority: e.target.value })} data-field="priority" />
              </Field>
            ) : null}
            {entity === "providers" ? (
              <p className="self-end text-[12.5px] text-muted-foreground sm:col-span-2">
                Fallback order {record?.order ?? "last"}: change it with the arrows in the list; it must agree with every library’s provider list.
              </p>
            ) : null}
            <ConfigFields fields={tuning} values={st.config} saved={saved?.config} errors={errors} onChange={configChange} disabled={disabled} />
            {allowlist ? (
              <ProviderAllowlist value={String(st.config.providers ?? "")} saved={saved ? String(saved.config.providers ?? "") : undefined} providers={p.providerKeys ?? []}
                only={onlyProviders} setOnly={(v) => { setOnlyChoice(v); setAllowlistError(""); }}
                onChange={(v) => set({ config: { ...st.config, providers: v } })} disabled={disabled} error={errors.providers || allowlistError || undefined} help={allowlist.help} />
            ) : null}
          </div>
        </details>
        {entity === "providers" && record ? <OrphanChoice checked={st.orphan} onChange={(v) => set({ orphan: v })} disabled={disabled} /> : null}
        {p.error ? <ExplainBox error={p.error} onReload={p.onReload} onReloadKeep={p.onReloadKeep} className="sm:col-span-2" /> : null}
        {p.kept !== undefined && !p.error ? (
          <p className="text-[13px] text-[var(--warn-ink)] sm:col-span-2" data-testid="draft-kept">
            Your changes are kept over the reloaded settings (generation {p.kept}). Saving them replaces what changed elsewhere in this record.
          </p>
        ) : null}
        {p.test ? (
          <div className="sm:col-span-2">
            <div className="mb-1 text-xs text-muted-foreground">Test of these unsaved values (nothing was saved)</div>
            <div className="flex flex-wrap items-center gap-2">
              <TestResult result={p.test} now={p.now} />
              {p.onEnableSave && p.test.status === "ok" && !st.enabled ? (
                <Button type="button" size="sm" disabled={disabled} onClick={() => submit(p.onEnableSave!)} data-testid="record-enable-save">
                  {p.saving ? <Loader2 className="animate-spin" /> : <Save />} Enable and save
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
        <Announcer announcement={p.announcement} testid="candidate-announce" />
      </form>
      {record?.updated_at ? (
        <div className="mt-4 flex flex-wrap gap-2 text-xs text-muted-foreground">
          <Badge tone="outline">id {record.id}</Badge> changed {record.updated_at.slice(0, 16).replace("T", " ")} UTC
        </div>
      ) : null}
    </SheetLayout>
  );
}

/** What deleting a record does and what has to happen first, for its confirmation dialog. */
export function deleteScope(entity: Entity, key: string): Scope {
  return {
    title: `Delete ${NOUN[entity]} ${key}?`,
    does: [entity === "budgets" ? `Delete the budget ${key}.` : `Delete the ${NOUN[entity]} ${key} and its credential.`],
    keeps: [
      entity === "budgets"
        ? "Sources that name this budget must be changed first, or validation refuses the delete."
        : `Libraries that list this ${NOUN[entity]} must drop it first, or validation refuses the delete.`,
      "The audit keeps a record of the delete.",
    ],
    confirm: "Delete",
  };
}

/** The provider delete's orphan choice: off unless the operator ticks it. */
export function DeleteOrphanChoice({ checked, onChange, disabled }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <OrphanChoice checked={checked} onChange={onChange} disabled={disabled}>
      Leave this off and the delete is refused while the provider has active downloads. Tick it to delete anyway:
      its active downloads are marked failed ("provider removed by settings", not blocklisted).
    </OrphanChoice>
  );
}

/** The editor with its state. The session (entity, record and the generation the draft was read at) is fixed when
 * the editor opens: every write uses its generation, so a change made elsewhere meanwhile answers 409, never a silent
 * save over it. entity is the panel the editor is shown in; a session for another entity cannot write. */
export function RecordEditor({
  entity,
  ent,
  session,
  nextOrder,
  records,
  providerKeys,
  libraries,
  readOnly,
  onDone,
  headingRef,
}: {
  entity: Entity;
  ent: SchemaEntity;
  session: EditorSession;
  nextOrder: number;
  records?: SettingsRecord[];
  providerKeys?: string[];
  /** A new indexer: the libraries it can join; the ones that suit its Service start ticked. */
  libraries?: VersionRecord[];
  readOnly?: string;
  /** The editor closed; deleted when its record was just deleted (focus then goes to a neighbour). */
  onDone: (deleted?: boolean) => void;
  /** The sheet's heading, which the sheet focuses when it opens. */
  headingRef?: RefObject<HTMLHeadingElement | null>;
}) {
  const editor = useSettingsEditor({
    initial: session,
    rebase: async (base) => {
      try {
        return rebaseEditor(base, await qc.fetchQuery({ queryKey: qk.settings.overview, queryFn: settingsOverview, staleTime: 0 }));
      } finally { void reload(); }
    },
    gone: RECORD_GONE,
    onReload: (next, keep) => {
      setConfirmingDelete(false);
      if (keep) return;
      setSt(ticked(editorFrom(next.entity, ent, next.record, next.kind)));
      setErrors({});
      setTest(undefined);
    },
  });
  const { base: sess, error, setError, kept, confirm } = editor;
  // A new record's first draft is its baseline: marks, the count and Discard work before anything is saved.
  const [first] = useState<EditorState>(() => {
    const state = editorFrom(session.entity, ent, session.record, session.kind);
    return session.record ? state : { ...state, key: nextRecordKey(state.key, records) };
  });
  // A new indexer starts with the libraries that suit its Service ticked, once they have loaded.
  const ticked = (state: EditorState): EditorState =>
    !session.record && session.entity === "indexers" && libraries ? { ...state, libraries: matchingLibraries({ kind: state.kind }, libraries) } : state;
  const [st, setSt] = useState<EditorState>(() => ticked(first));
  const tickedOnce = useRef(!!libraries);
  useEffect(() => {
    if (tickedOnce.current || !libraries || session.record || session.entity !== "indexers") return;
    tickedOnce.current = true;
    setSt((state) => ({ ...state, libraries: matchingLibraries({ kind: state.kind }, libraries) }));
  }, [libraries, session.record, session.entity]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [test, setTest] = useState<SettingsTestResult>();
  // The draft the shown test ran on: Enable and save is offered only while the form still holds exactly it.
  const [tested, setTested] = useState<EditorState>();
  const [testing, setTesting] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [deleteOrphan, setDeleteOrphan] = useState(false);
  const [deleteError, setDeleteError] = useState<Explained>();
  const write = useSettingsWrite();
  const reload = useReloadSettings();
  const qc = useQueryClient();
  const [announcement, announce] = useAnnouncement();
  const ownHeading = useRef<HTMLHeadingElement>(null);
  const heading = headingRef ?? ownHeading;
  const record = sess.record;
  const savedState = useMemo(() => record ? editorFrom(sess.entity, ent, record, sess.kind) : first, [record, sess.entity, sess.kind, ent, first]);
  const discard = () => {
    setSt(ticked({ ...savedState }));
    setErrors({});
    // A refusal of the discarded values goes with them; conflicts and server state stay explained.
    if (error?.kind === "validation" || error?.kind === "bad_request") setError(undefined);
  };

  // A server refusal focuses its explanation (focusable with tabIndex -1), including browsers that retain focus on
  // the Save button while its pending state changes.
  useEffect(() => {
    if (!error) return;
    const form = heading.current?.closest('[data-testid="record-form"]');
    (form?.querySelector<HTMLElement>('[data-testid="settings-error"]') ?? heading.current)?.focus();
  }, [error, heading]);

  const mismatch = editorMismatch(sess, entity);
  if (mismatch) {
    return (
      <SheetLayout testid="record-form" data-entity={sess.entity} data-mismatch={entity} headingRef={heading} title="Cannot edit here" onClose={() => onDone()}
        footer={<Button type="button" size="sm" variant="outline" onClick={() => onDone()}>Close</Button>}>
        <ExplainBox error={mismatch} />
      </SheetLayout>
    );
  }

  const build = (state: EditorState = st) => {
    const r = sessionInput(sess, entity, ent, state, nextOrder);
    setErrors(r.errors);
    if (r.refused) setError(r.refused);
    return r.input;
  };
  const save = (state: EditorState = st) => {
    const input = build(state);
    if (!input) return;
    const { body, error: bodyError } = writeBody(input);
    if (bodyError) return setErrors({ secret: bodyError });
    void editor.save(
      { method: record ? "PUT" : "POST", path: record ? `/settings/${sess.entity}/${record.id}` : `/settings/${sess.entity}`, body },
      () => onDone(),
      {
        onError: (e) => { setErrors(fieldErrors(e)); announce("Settings were not saved."); },
        onConfirmation: () => announce("This change needs confirmation."),
      },
    );
  };
  const runTest = async () => {
    if (sess.entity === "budgets") return;
    const input = build();
    if (!input) return;
    const { body, error: bodyError } = candidateBody(input);
    if (bodyError) return setErrors({ secret: bodyError });
    setTesting(true);
    setError(undefined);
    // A retest drops the last result: Enable and save comes back only with a new pass.
    setTest(undefined);
    setTested(undefined);
    const key = record?.key ?? st.key;
    // The live region names the source, or "the new indexer" before it has a key.
    const said = key || `the new ${NOUN[sess.entity]}`;
    try {
      const result = await testCandidate(sess.entity, body);
      setTest(result);
      setTested(st);
      announce(testAnnouncement({ type: "result", key: said, result }, NOUN[sess.entity]));
    } catch (e) {
      const a = testErrorAction(key, e);
      if (a.type === "result") setTest(a.result);
      else if (a.type === "error") setError(a.error);
      announce(testAnnouncement({ ...a, key: said } as TestAction, NOUN[sess.entity]));
    } finally {
      setTesting(false);
      void qc.invalidateQueries({ queryKey: qk.settings.all });
    }
  };
  const del = () => {
    const d = sessionDelete(sess, entity, deleteOrphan);
    if (d.refused) return setDeleteError(d.refused);
    if (!d.path) return;
    setDeleteError(undefined);
    const path = d.path;
    const send = (token?: string) => write.mutate({ method: "DELETE", path, token }, { onSuccess: () => onDone(true), onError: (e) => {
      if (confirm.ask(e, send)) return;
      const x = explainError(e);
      // A conflict goes back to the form, which offers the reload.
      if (x.kind === "stale") {
        setConfirmingDelete(false);
        setError(x);
      } else setDeleteError(x);
    } });
    send();
  };
  return (
    <>
    {confirm.dialog}
    <RecordFormView
      entity={sess.entity}
      ent={ent}
      record={record}
      records={records}
      providerKeys={providerKeys}
      libraries={sess.entity === "indexers" && !record ? libraries : undefined}
      state={st}
      onState={setSt}
      errors={errors}
      readOnly={readOnly}
      saving={editor.busy}
      testing={testing}
      error={error}
      test={test}
      onSave={() => save()}
      onEnableSave={tested === st ? () => { const on = { ...st, enabled: true }; setSt(on); save(on); } : undefined}
      onTest={ent.tests ? () => void runTest() : undefined}
      onCancel={() => onDone()}
      onReload={() => void editor.reload(false)}
      onReloadKeep={() => void editor.reload(true)}
      kept={kept}
      onAskDelete={() => {
        setConfirmingDelete(true);
        setDeleteOrphan(false);
        setDeleteError(undefined);
      }}
      deleting={write.isPending && confirmingDelete}
      headingRef={heading}
      announcement={announcement}
      saved={savedState}
      onDiscard={discard}
    />
    {record && confirmingDelete ? (
      <ConfirmDialog
        testid="delete-confirm"
        scope={deleteScope(sess.entity, record.key)}
        subject={`${NOUN[sess.entity]} ${record.key}`}
        busy={write.isPending}
        error={deleteError ? new Error(`${deleteError.title}: ${deleteError.message}`) : undefined}
        onClose={() => setConfirmingDelete(false)}
        onConfirm={del}
      >
        {sess.entity === "providers" ? <DeleteOrphanChoice checked={deleteOrphan} onChange={setDeleteOrphan} disabled={write.isPending} /> : null}
      </ConfirmDialog>
    ) : null}
    </>
  );
}


/** Suggest an unused key without changing any existing record. */
function nextRecordKey(kind: string, records: SettingsRecord[] = []): string {
  const keys = new Set(records.map(r => r.key));
  let key = kind;
  for (let n = 2; keys.has(key); n++) key = `${kind}-${n}`;
  return key;
}
