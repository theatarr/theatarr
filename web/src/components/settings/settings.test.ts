// Interaction tests for the settings pages (docs/design/settings.md). The UI test setup is node-only (no DOM), so
// each step of a flow is rendered to markup from the state the reducers (lib/settings.ts) produce for the operator's
// clicks: the read-only state, write-only secrets, the 409 conflict, the orphan confirmation, test-all and
// the import preview then apply, the audit's field diff, the editor's focus targets and the tests' live region.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement as h, isValidElement, type ReactElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { ApiError } from "@/api/client";
import type { SettingsAuditRow, SettingsRecord } from "@/api/settings";
import { SettingsTabView } from "@/pages/admin/settings";
import {
  editorFrom,
  explainError,
  importReducer,
  initialImport,
  initialTests,
  openEditor,
  testReducer,
  type ImportState,
} from "@/lib/settings";
import { AuditView } from "./audit";
import { budget, dmm, indexerSchema, overview, providerSchema, rd, schema, zilean } from "./fixtures";
import { returnSelector, returnTarget } from "./focus";
import { ImportView, type ImportViewProps } from "./import";
import { Announcer, SecretField } from "./parts";
import { deleteScope, DeleteOrphanChoice, RecordEditor, RecordFormView, type RecordFormProps } from "./record-form";
import { BudgetsView, SourcesPanel, SourcesView, type SourcesViewProps } from "./sources";

const noop = () => {};

function render(el: ReactElement): string {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // The panels keep the open record in the URL (?edit=), so they render inside a router.
  return renderToStaticMarkup(h(MemoryRouter, null, h(QueryClientProvider, { client: qc }, el)));
}

/** The opening tag of the element with this data-testid (the first one). */
function tag(html: string, testid: string): string {
  const m = new RegExp(`<[a-z]+[^>]*data-testid="${testid}"[^>]*>`).exec(html);
  if (!m) throw new Error(`no element with data-testid="${testid}"`);
  return m[0];
}
const all = (html: string, testid: string) => html.match(new RegExp(`<[a-z]+[^>]*data-testid="${testid}"[^>]*>`, "g")) ?? [];
const isDisabled = (t: string) => /\sdisabled=""/.test(t);
const isChecked = (t: string) => /\schecked=""/.test(t);

const unavailableOverview = overview({
  writable: false,
  generation: 0,
  active_generation: 0,
  indexers: [{ ...dmm, id: undefined }],
  providers: [{ ...rd, id: undefined }],
  budgets: [{ ...budget, id: undefined }],
});

function sourcesProps(p: Partial<SourcesViewProps> = {}): SourcesViewProps {
  return {
    entity: "indexers",
    records: [dmm, zilean],
    tests: initialTests,
    includeDisabled: false,
    onIncludeDisabled: noop,
    onTest: noop,
    onTestAll: noop,
    onEdit: noop,
    onMove: noop,
    ...p,
  };
}

function formProps(p: Partial<RecordFormProps> & Pick<RecordFormProps, "entity" | "ent">): RecordFormProps {
  return {
    state: editorFrom(p.entity, p.ent, p.record),
    onState: noop,
    errors: {},
    onSave: noop,
    onCancel: noop,
    ...p,
  };
}

describe("an unavailable settings service is read-only", () => {
  it("says the service is unavailable and greys out every write on the source tabs", () => {
    for (const tab of ["indexers", "providers", "budgets"] as const) {
      const html = render(h(SettingsTabView, { tab, overview: unavailableOverview, schema, versions: [] }));
      expect(tag(html, "settings-authority")).toContain('data-mode="unavailable"');
      expect(html).toContain("Settings service unavailable");
      expect(html).toContain("Unavailable · read-only");
      expect(tag(html, "settings-tab")).toContain('data-readonly="true"');
      expect(isDisabled(tag(html, "add")), `${tab} add`).toBe(true);
      if (tab !== "budgets") {
        expect(isDisabled(tag(html, "testall"))).toBe(true);
        for (const t of [...all(html, "test-one"), ...all(html, "move-up"), ...all(html, "move-down")]) expect(isDisabled(t)).toBe(true);
      }
      // View, not Edit: the form opens read-only.
      expect(html).toContain("View");
    }
  });
  it("disables import, export and the audit", () => {
    const imp = render(h(SettingsTabView, { tab: "import", overview: unavailableOverview, schema, versions: [] }));
    expect(isDisabled(tag(imp, "import-preview"))).toBe(true);
    expect(imp).toContain('data-testid="import-readonly"');
    expect(imp).not.toContain('data-testid="export-link"');
    const audit = render(h(SettingsTabView, { tab: "audit", overview: unavailableOverview, schema, versions: [] }));
    expect(audit).toContain('data-testid="audit-readonly"');
  });
  it("renders the form with every control disabled and the reason", () => {
    const html = render(h(RecordFormView, formProps({ entity: "indexers", ent: indexerSchema, record: dmm, readOnly: "Settings service unavailable." })));
    expect(html).toContain('data-testid="form-readonly"');
    expect(isDisabled(tag(html, "record-save"))).toBe(true);
    expect(isDisabled(tag(html, "secret-replace"))).toBe(true);
    const controls = html.match(/<(input|select)[^>]*>/g) ?? [];
    expect(controls.length).toBeGreaterThan(4);
    for (const t of controls) expect(isDisabled(t), t).toBe(true);
  });
  it("shows the database as live in db mode, with writes enabled", () => {
    const html = render(h(SettingsTabView, { tab: "indexers", overview: overview({ indexers: [dmm] }), schema, versions: [] }));
    expect(tag(html, "settings-authority")).toContain('data-mode="db"');
    expect(html).toContain("Settings come from the database");
    expect(isDisabled(tag(html, "add"))).toBe(false);
    expect(isDisabled(tag(html, "testall"))).toBe(false);
  });
});

describe("indexer credential badges", () => {
  it("shows no credential not set for built-in sources that take no key", () => {
    const html = render(h(SourcesView, sourcesProps({ entity: "indexers", records: [
      { ...zilean, secret_set: false },
      { ...zilean, id: 5, key: "torrentio", kind: "torrentio", config: { kind: "torrentio", url: "https://torrentio.example.org", privacy: "public" } },
      { ...dmm, secret_set: false },
    ] })));
    const row = (key: string) => html.slice(html.indexOf(`data-record="${key}"`), html.indexOf("</tr>", html.indexOf(`data-record="${key}"`)));
    expect(row("zilean")).not.toContain("credential");
    expect(row("torrentio")).not.toContain("credential");
    expect(row("dmm")).toContain("credential not set");
  });
});

describe("secrets are write-only", () => {
  // A record as a careless server might send it: the UI must not show anything it did not ask for.
  const leaky = { ...rd, secret: "s3cr3t-value", config: { ...rd.config, api_key_env: "THEATARR_RD_KEY" } } as SettingsRecord;
  it("never renders a stored value: lists and forms show set or not set only", () => {
    const list = render(h(SourcesView, sourcesProps({ entity: "providers", records: [leaky, { ...rd, id: 8, key: "ad", secret_set: false }] })));
    const form = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: leaky })));
    for (const html of [list, form]) {
      expect(html).not.toContain("s3cr3t-value");
      expect(html).not.toContain("THEATARR_RD_KEY");
    }
    expect(list).toContain("credential set");
    expect(list).toContain("credential not set");
    expect(form).toContain('data-testid="secret-state"');
    expect(tag(form, "secret-field")).toContain('data-action="keep"');
    // Keep: no credential input at all, only Replace and Clear.
    expect(form).not.toContain('data-testid="secret-input"');
    expect(form).toContain('data-testid="secret-replace"');
    expect(form).toContain('data-testid="secret-clear"');
  });
  it("Replace shows an empty password field for a new value; Clear says what happens", () => {
    const replace = renderToStaticMarkup(h(SecretField, { set: true, action: "replace", value: "", onAction: noop, onValue: noop }));
    const input = tag(replace, "secret-input");
    expect(input).toContain('type="password"');
    expect(input).toContain('value=""');
    expect(input).toContain('autoComplete="off"');
    const clear = renderToStaticMarkup(h(SecretField, { set: true, action: "clear", value: "", onAction: noop, onValue: noop }));
    expect(clear).toContain("will be removed on save");
    const unset = renderToStaticMarkup(h(SecretField, { set: false, action: "keep", value: "", onAction: noop, onValue: noop }));
    expect(tag(unset, "secret-state")).toBeDefined();
    expect(unset).toContain(">not set<");
    expect(unset).not.toContain('data-testid="secret-clear"');
  });
  it("explains inline that testing a new address needs the credential again", () => {
    const st = editorFrom("providers", providerSchema, rd);
    st.config = { ...st.config, url: "https://rd-proxy.example.com/rest/1.0" };
    const html = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: rd, state: st, onTest: noop })));
    expect(html).toContain('data-testid="secret-needs-again"');
    // Saving keeps the stored credential after a confirmation; only the test needs it again.
    expect(isDisabled(tag(html, "record-save"))).toBe(false);
    expect(isDisabled(tag(html, "record-test"))).toBe(true);
    const refused = explainError(new ApiError(422, "send the credential again to test a new address: the stored credential is only sent to the saved source's scheme, host and port"));
    const err = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: rd, error: refused })));
    expect(tag(err, "settings-error")).toContain('data-kind="readdress"');
    expect(err).toContain("Enter the credential again for the new address");
  });
});

describe("409: settings changed elsewhere", () => {
  it("explains the conflict and offers a reload, keeping what was typed", () => {
    const err = explainError(new ApiError(409, "settings changed since this generation was read; reload and retry", undefined, { error: "x", generation: 9 }));
    const st = { ...editorFrom("indexers", indexerSchema, dmm), priority: "3" };
    const html = render(h(RecordFormView, formProps({ entity: "indexers", ent: indexerSchema, record: dmm, state: st, error: err, onReload: noop })));
    expect(tag(html, "settings-error")).toContain('data-kind="stale"');
    expect(html).toContain("Settings changed elsewhere");
    expect(html).toContain("generation 9");
    expect(html).toContain('data-testid="settings-reload"');
    expect(html).toMatch(/data-field="priority"[^>]*value="3"|value="3"[^>]*data-field="priority"/);
  });
  it("offers a reload on a refused priority move too", () => {
    const err = explainError(new ApiError(409, "settings changed since this generation was read; reload and retry", undefined, { generation: 5 }));
    const html = render(h(SourcesView, sourcesProps({ moveError: err, onReload: noop })));
    expect(html).toContain('data-testid="settings-reload"');
  });
});

describe("the editor across tabs", () => {
  /** The first element of this component type in an unrendered element tree. */
  function find(n: ReactNode, type: unknown): ReactElement | undefined {
    if (Array.isArray(n)) return n.map((c: ReactNode) => find(c, type)).find(Boolean);
    if (!isValidElement(n)) return undefined;
    if (n.type === type) return n;
    return find((n.props as { children?: ReactNode }).children, type);
  }
  const prowlarr: SettingsRecord = { id: 7, key: "prowlarr-public", kind: "torznab", enabled: true, priority: 1, secret_set: false, config: { kind: "torznab", url: "http://prowlarr:9696/1/api" } };

  it("starts a new sources panel on a tab change, so no open editor carries over", () => {
    const o = overview({ indexers: [prowlarr], providers: [rd] });
    const panels = (["indexers", "providers"] as const).map((tab) => find(SettingsTabView({ tab, overview: o, schema, versions: [] }), SourcesPanel));
    expect(panels[0]?.type).toBe(panels[1]?.type);
    // Same component in the same place: only a different key makes React drop the indexer panel's editor state.
    expect(panels[0]?.key).toBeTruthy();
    expect(panels[0]?.key).not.toBe(panels[1]?.key);
  });
  it("refuses an editor opened for another entity: no provider form for an indexer record", () => {
    const html = render(h(RecordEditor, { entity: "providers", ent: providerSchema, session: openEditor("indexers", prowlarr, 4), nextOrder: 2, onDone: noop }));
    expect(html).not.toContain("Edit provider");
    expect(html).not.toContain('data-testid="record-save"');
    expect(html).not.toContain('data-testid="record-delete"');
    expect(html).toContain("This form belongs to another tab");
    const ok = render(h(RecordEditor, { entity: "indexers", ent: indexerSchema, session: openEditor("indexers", prowlarr, 4), nextOrder: 2, onDone: noop }));
    expect(ok).toContain("Edit indexer");
    expect(isDisabled(tag(ok, "record-save"))).toBe(false);
  });
  it("on a conflict in the editor, offers to reload discarding or keeping the draft", () => {
    const err = explainError(new ApiError(409, "settings changed since this generation was read; reload and retry", undefined, { generation: 5 }));
    const html = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: rd, error: err, onReload: noop, onReloadKeep: noop })));
    expect(html).toContain("Settings changed elsewhere");
    expect(html).toContain("Reload, discard my changes");
    expect(html).toContain('data-testid="settings-reload-keep"');
    expect(html).not.toContain('data-testid="draft-kept"');
    const kept = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: rd, kept: 5 })));
    expect(kept).toContain('data-testid="draft-kept"');
    expect(kept).toContain("generation 5");
  });
});

describe("the orphan confirmation", () => {
  it("defaults to off on a provider edit and explains what it does", () => {
    const html = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: rd })));
    const box = tag(html, "orphan-confirm");
    expect(box).toContain('type="checkbox"');
    expect(isChecked(box)).toBe(false);
    expect(html).toContain("orphan_active_downloads");
    expect(html).toContain("provider removed by settings");
    expect(html).toMatch(/is refused; nothing changes/);
  });
  it("is not offered for indexers or new providers", () => {
    expect(render(h(RecordFormView, formProps({ entity: "indexers", ent: indexerSchema, record: dmm })))).not.toContain("orphan-confirm");
    expect(render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema })))).not.toContain("orphan-confirm");
  });
  it("defaults to off on a provider delete", () => {
    // The delete dialog's choice (RecordEditor opens it with deleteOrphan false; editors.test.tsx clicks through it).
    const confirm = render(h(DeleteOrphanChoice, { checked: false, onChange: noop }));
    expect(isChecked(tag(confirm, "orphan-confirm"))).toBe(false);
    expect(confirm).toContain("the delete is refused while the provider has active downloads");
    expect(deleteScope("providers", "rd").keeps.join(" ")).toContain("must drop it first");
  });
  it("shows the continuity refusal plainly", () => {
    const err = explainError(new ApiError(422, "settings refused: providers[rd]: removal would orphan 2 active download(s); set orphan_active_downloads to confirm"));
    const html = render(h(RecordFormView, formProps({ entity: "providers", ent: providerSchema, record: rd, error: err })));
    expect(tag(html, "settings-error")).toContain('data-kind="orphan"');
    expect(html).toContain("This would fail 2 active downloads");
    const order = explainError(new ApiError(422, 'settings refused: versions[movie-web].providers[1]: "rd" listed out of fallback order (providers[rd].order is 2)'));
    const list = render(h(SourcesView, sourcesProps({ entity: "providers", records: [rd], moveError: order })));
    expect(list).toContain("A library lists the providers in another order");
  });
});

describe("test and test-all", () => {
  it("shows a loading state over every source while test-all runs", () => {
    const tests = testReducer(initialTests, { type: "start-all", keys: ["dmm", "zilean"], at: Date.now() });
    const html = render(h(SourcesView, sourcesProps({ tests, includeDisabled: true })));
    expect(isDisabled(tag(html, "testall"))).toBe(true);
    expect(tag(html, "testall-running")).toContain('role="status"');
    expect(html).toContain("Testing 2 indexers");
    expect(all(html, "test-running").map((t) => /data-key="([^"]+)"/.exec(t)?.[1])).toEqual(["dmm", "zilean"]);
    for (const t of all(html, "test-one")) expect(isDisabled(t)).toBe(true);
  });
  it("then shows one result per source, cooldowns with their retry", () => {
    let tests = testReducer(initialTests, { type: "start-all", keys: ["dmm", "zilean"], at: 1 });
    const retry = new Date(Date.now() + 25_000).toISOString();
    tests = testReducer(tests, {
      type: "all-done",
      results: [
        { key: "dmm", entity: "indexers", status: "ok", latency_ms: 80, results: 12 },
        { key: "zilean", entity: "indexers", status: "cooldown", retry_at: retry },
      ],
    });
    const html = render(h(SourcesView, sourcesProps({ tests, includeDisabled: true })));
    expect(html).not.toContain('data-testid="testall-running"');
    expect(tag(html, "testall-summary")).toBeDefined();
    expect(html).toContain("1 of 2 ok");
    const results = all(html, "test-result");
    expect(results.map((t) => /data-key="([^"]+)"/.exec(t)?.[1])).toEqual(["dmm", "zilean"]);
    expect(results[1]).toContain('data-status="cooldown"');
    expect(html).toMatch(/try again in 2\d s/);
    expect(html).toContain("12 releases");
  });
  it("shows the saved last test and back-off when nothing ran on this page", () => {
    const html = render(h(SourcesView, sourcesProps()));
    expect(tag(html, "test-result")).toContain('data-key="dmm"');
    expect(html).toContain("never tested");
    expect(html).toContain("backed off until");
  });
  it("shows a single test's cooldown (429)", () => {
    const tests = testReducer(initialTests, {
      type: "result",
      key: "dmm",
      result: { key: "dmm", entity: "indexers", status: "cooldown", retry_at: new Date(Date.now() + 17_000).toISOString() },
    });
    const html = render(h(SourcesView, sourcesProps({ tests })));
    expect(html).toMatch(/tested recently; try again in 1[78] s/);
  });
  it("shows budget use", () => {
    const html = render(h(BudgetsView, { records: [budget], onEdit: noop }));
    expect(html).toContain("16/1m0s");
    expect(html).toMatch(/data-col="hour"><span[^>]*>This hour <\/span>12</);
    expect(html).toMatch(/data-col="day"><span[^>]*>Today <\/span>140</);
  });
});

describe("import: preview, then apply", () => {
  const preview = {
    generation: 4,
    changes: [
      { entity: "indexers" as const, key: "dmm", operation: "update" as const, credential_changed: true },
      { entity: "providers" as const, key: "tb", operation: "delete" as const },
      { entity: "budgets" as const, key: "seadex", operation: "update" as const },
    ],
    before: { indexers: [], providers: [], budgets: [] },
    after: { indexers: [], providers: [], budgets: [] },
    yaml: "indexers:\n  dmm: {}\n",
    sections_note: "Settings sections are kept as stored; none is filled from the files.",
    sections_from_files: [],
  };
  const view = (state: ImportState, p: Partial<ImportViewProps> = {}) =>
    render(h(ImportView, { overview: overview(), state, onPreview: noop, onConfirm: noop, onOrphan: noop, onApply: noop, onReset: noop, ...p }));

  it("says imports share the explicit command path", () => {
    const html = view(initialImport);
    expect(html).toContain("The database is always the live settings authority");
    expect(html).toContain("theatarr settings import");
    expect(isDisabled(tag(html, "import-preview"))).toBe(false);
    expect(html).toContain('data-testid="import-apply-waiting"');
    expect(html).not.toContain('data-testid="import-apply"');
  });
  it("shows the diff with credential_changed, and keeps apply off until confirmed", () => {
    let s = importReducer(initialImport, { type: "preview-start" });
    expect(isDisabled(tag(view(s), "import-preview"))).toBe(true);
    s = importReducer(s, { type: "preview-done", preview });
    let html = view(s);
    expect(tag(html, "import-panel")).toContain('data-step="preview"');
    expect(html).toContain('data-change="indexers/dmm"');
    expect(html).toContain('data-op="delete"');
    expect(all(html, "credential-changed")).toHaveLength(1);
    expect(html).toContain("1 credential replaced");
    expect(isChecked(tag(html, "import-confirm"))).toBe(false);
    expect(isChecked(tag(html, "orphan-confirm"))).toBe(false);
    expect(isDisabled(tag(html, "import-apply"))).toBe(true);
    s = importReducer(s, { type: "confirm", value: true });
    html = view(s);
    expect(isChecked(tag(html, "import-confirm"))).toBe(true);
    expect(isChecked(tag(html, "orphan-confirm"))).toBe(false);
    expect(isDisabled(tag(html, "import-apply"))).toBe(false);
    s = importReducer(s, { type: "apply-start" });
    expect(isDisabled(tag(view(s), "import-apply"))).toBe(true);
    s = importReducer(s, { type: "apply-done", generation: 5 });
    html = view(s);
    expect(tag(html, "import-applied")).toBeDefined();
    expect(html).toContain("generation 5 is stored and running");
  });
  it("leaves the orphan choice out when no provider changes", () => {
    const s = importReducer(initialImport, { type: "preview-done", preview: { ...preview, changes: [preview.changes[2]] } });
    expect(view(s)).not.toContain("orphan-confirm");
  });
  it("shows a refused apply and asks for a new preview when stale", () => {
    let s = importReducer(importReducer(initialImport, { type: "preview-done", preview }), { type: "confirm", value: true });
    s = importReducer(importReducer(s, { type: "apply-start" }), {
      type: "apply-error",
      error: explainError(new ApiError(409, "settings changed since this generation was read; reload and retry", undefined, { generation: 6 })),
    });
    const html = view(s, { onReload: noop });
    expect(tag(html, "settings-error")).toContain('data-kind="stale"');
    expect(html).toContain('data-testid="import-apply-waiting"');
  });
});

describe("the audit shows what changed", () => {
  const seadexBefore = { id: 5, key: "seadex", entity: "budgets", created_at: "2026-10-19T09:00:00Z", updated_at: "2026-10-19T09:00:00Z", enabled: true, config: { requests: 10, per: "1m0s", burst: 0, hourly: 0, daily: 0 }, secret: "" };
  const row = (r: Partial<SettingsAuditRow>): SettingsAuditRow =>
    ({ id: 12, actor: "admin", created_at: "2026-10-26T07:00:00Z", generation: 3, entity: "budgets", key: "seadex", operation: "update", ...r }) as SettingsAuditRow;
  const seadex = row({ before: seadexBefore, after: { ...seadexBefore, updated_at: "2026-10-26T07:00:00Z", config: { ...seadexBefore.config, hourly: 500 } } });
  const page = (records: SettingsAuditRow[]) => ({ records, next: 0 }) as unknown as Parameters<typeof AuditView>[0]["page"];
  const view = (records: SettingsAuditRow[], open: Record<number, boolean> = {}) => render(h(AuditView, { page: page(records), pageNo: 1, open, onToggle: noop }));

  it("names the changed value in the collapsed entry, at every width", () => {
    const html = view([seadex]);
    expect(html).toContain("changed");
    expect(html).toContain("config.hourly 0 → 500");
    const toggle = tag(html, "audit-toggle");
    expect(toggle).toContain('aria-expanded="false"');
    expect(toggle).toContain('aria-controls="audit-12-changes"');
    expect(html).toContain("Show changes (1)");
    expect(html).not.toContain('data-testid="audit-changes"');
    // Nothing in an entry is hidden by width: the old change column (hidden md:table-cell) is gone; entries stack.
    const entry = html.slice(html.indexOf("data-audit="), html.indexOf("</ol>"));
    expect(entry).not.toMatch(/class="(?:[^"]*\s)?hidden[\s"]/);
    expect(entry).toContain("sm:grid-cols-");
  });
  it("expands into a field diff with the full redacted records on demand", () => {
    const html = view([seadex], { 12: true });
    expect(tag(html, "audit-toggle")).toContain('aria-expanded="true"');
    expect(html).toContain("Hide changes");
    const panel = tag(html, "audit-changes");
    expect(panel).toContain('id="audit-12-changes"');
    expect(html).toMatch(/data-path="config.hourly" data-op="changed"/);
    expect(html).toMatch(/<span class="sr-only">from <\/span><span[^>]*>0<\/span>.*<span[^>]*>500<\/span>/);
    // The timestamps are not listed as changes.
    expect(html).not.toContain('data-path="updated_at"');
    expect(tag(html, "audit-full")).toMatch(/^<details/);
    const full = html.slice(html.indexOf('data-testid="audit-full"'));
    expect(full).toContain("Full records (redacted)");
    expect(full).toContain("&quot;hourly&quot;: 500");
  });
  it("lists creates, deletes and tests as added and removed fields; a whole-settings row has nothing to expand", () => {
    const created = view([row({ operation: "create", before: null, after: { key: "x", config: { requests: 1 } } })], { 12: true });
    expect(created).toMatch(/data-path="config.requests" data-op="added"/);
    const deleted = view([row({ operation: "delete", before: { key: "x", enabled: true }, after: null })], { 12: true });
    expect(deleted).toMatch(/data-path="enabled" data-op="removed"/);
    expect(deleted).toContain("(none)");
    const test = view([row({ entity: "indexers", key: "dmm", operation: "test", before: null, after: { status: "failed" } })]);
    expect(test).toContain("status &quot;failed&quot;");
    const imported = view([row({ entity: "settings", key: "", operation: "bootstrap-import", before: null, after: null })]);
    expect(imported).not.toContain('data-testid="audit-toggle"');
    expect(imported).toContain("first-start import");
  });
  it("never shows a credential: a changed secret is just changed, even if a record carried a value", () => {
    const leaky = row({
      entity: "providers",
      key: "rd",
      before: { key: "rd", secret: "set", config: { url: "https://api.example/rest", api_key_env: "THEATARR_RD_KEY" } },
      after: { key: "rd", secret: "s3cr3t-value", token: "tok-123", config: { url: "https://api.example/rest", api_key_env: "THEATARR_RD_KEY2", password: "pw-1" } },
    });
    for (const html of [view([leaky]), view([leaky], { 12: true })]) {
      for (const v of ["s3cr3t-value", "tok-123", "pw-1", "THEATARR_RD_KEY"]) expect(html, v).not.toContain(v);
    }
    const html = view([leaky], { 12: true });
    expect(html).toMatch(/data-path="secret" data-op="changed"/);
    expect(html).toContain("credential changed");
    expect(html).toContain("value never shown");
    expect(html).toContain("[redacted]");
    // The server's own presence marker stays readable in the full record.
    expect(html).toContain("&quot;secret&quot;: &quot;set&quot;");
  });
});

describe("the editor moves focus", () => {
  it("gives the form a focusable heading that names it", () => {
    const html = render(h(RecordFormView, formProps({ entity: "indexers", ent: indexerSchema, record: dmm })));
    const heading = tag(html, "record-form-heading");
    expect(heading).toMatch(/^<h2/);
    expect(heading).toContain('tabindex="-1"');
    const id = /id="([^"]+)"/.exec(heading)?.[1];
    expect(id).toBeTruthy();
    expect(tag(html, "record-form")).toContain(`aria-labelledby="${id}"`);
  });
  it("gives focus back to the opener, or to the panel when it is gone or disabled", () => {
    // Opening the sheet focuses its heading and closing it returns focus: editors.test.tsx checks both in the DOM.
    const focused: string[] = [];
    const el = (name: string, disabled = false) => ({ disabled, focus: () => focused.push(name) });
    const root = (found: Record<string, ReturnType<typeof el>>) => ({ focus: () => focused.push("panel"), querySelector: (s: string) => found[s] ?? null });
    returnTarget(root({ '[data-record="dmm"] [data-testid="edit"]': el("edit dmm") }), { edit: "dmm" }).focus();
    returnTarget(root({ '[data-testid="add"]': el("add") }), "add").focus();
    returnTarget(root({ '[data-testid="add"]': el("add", true) }), "add").focus();
    returnTarget(root({}), { edit: "zilean" }).focus();
    expect(focused).toEqual(["edit dmm", "add", "panel", "panel"]);
    expect(returnSelector({ edit: 'a"b' })).toBe('[data-record="a\\"b"] [data-testid="edit"]');
  });
  it("puts the record key on each row, so the Edit button can be found again", () => {
    const html = render(h(SourcesView, sourcesProps()));
    expect(html).toMatch(/data-record="dmm"[^]*data-testid="edit"/);
    const panelHtml = render(h(SourcesPanel, { entity: "indexers", overview: overview({ indexers: [dmm] }), ent: indexerSchema, versions: [] }));
    expect(panelHtml).toMatch(/^<div tabindex="-1"/);
  });
});

describe("finished tests are announced", () => {
  it("keeps a polite live region mounted, empty until a test finishes", () => {
    for (const tests of [initialTests, testReducer(initialTests, { type: "start-all", keys: ["dmm"], at: 1 })]) {
      const region = render(h(SourcesView, sourcesProps({ tests })));
      const t = tag(region, "test-announce");
      expect(t).toContain('role="status"');
      expect(t).toContain('aria-live="polite"');
      expect(region).toContain('data-testid="test-announce"></div>');
    }
  });
  it("says the latest outcome, cooldowns included", () => {
    const html = render(h(SourcesView, sourcesProps({ announcement: { seq: 3, text: "Test of zilean: cooldown, tested recently; try again in 25 s." } })));
    expect(html).toContain('data-testid="test-announce"><span>Test of zilean: cooldown, tested recently; try again in 25 s.</span></div>');
  });
  it("renders the same announcement identically, so a re-render says nothing new", () => {
    const a = { seq: 1, text: "Test all finished: 2 of 2 indexers ok." };
    const first = renderToStaticMarkup(h(Announcer, { announcement: a, testid: "x" }));
    expect(renderToStaticMarkup(h(Announcer, { announcement: { ...a }, testid: "x" }))).toBe(first);
    // A new announcement is a new node: React swaps the keyed span even when the text repeats.
    const el = Announcer({ announcement: a, testid: "x" });
    const span = (el.props as { children: ReactElement }).children;
    expect(span.key).toBe("1");
  });
  it("has a live region in the editor for an unsaved test", () => {
    const html = render(h(RecordFormView, formProps({ entity: "indexers", ent: indexerSchema, record: dmm, announcement: { seq: 1, text: "Test of dmm: ok, reachable." } })));
    expect(tag(html, "candidate-announce")).toContain('aria-live="polite"');
    expect(html).toContain("Test of dmm: ok, reachable.");
  });
});
