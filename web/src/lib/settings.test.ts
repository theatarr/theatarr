import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import type { SchemaField, SettingsRecord, SettingsTestResult } from "@/api/settings";
import { indexerSchema, overview, providerSchema, rd } from "@/components/settings/fixtures";
import {
  authority,
  canApply,
  candidateBody,
  configFromForm,
  deletePath,
  editorFrom,
  editorInput,
  editorKey,
  editorMismatch,
  editorReturn,
  explainError,
  explainReloadError,
  fieldWords,
  formFromConfig,
  hasAdvancedValues,
  importCounts,
  importReducer,
  initialImport,
  initialTests,
  movedOrder,
  movedPriorities,
  needsCredentialAgain,
  openEditor,
  orderConflicts,
  rebaseEditor,
  secretPatch,
  sessionDelete,
  sessionInput,
  serviceURL,
  sourceAddress,
  testAnnouncement,
  testErrorAction,
  testReducer,
  testSummary,
  writeBody,
} from "./settings";

describe("source of authority", () => {
  it("makes an unavailable settings service read-only", () => {
    const a = authority(overview({ writable: false, generation: 0, active_generation: 0 }));
    expect(a.mode).toBe("unavailable");
    expect(a.title).toBe("Settings service unavailable");
    expect(a.readOnly).toMatch(/Settings service unavailable/);
    expect(a.lines.join(" ")).toMatch(/settings service is unavailable/);
  });
  it("says the database is live in db mode, and flags a startup fallback", () => {
    const a = authority(overview());
    expect(a.mode).toBe("db");
    expect(a.readOnly).toBeUndefined();
    expect(a.fallback).toBeUndefined();
    expect(a.lines[1]).toBe("These sections in /config/policy are ignored until you import them again (Import & export). Pause, arr views and the other policy sections (library and Plex, notifications, metadata, egress and the rest) are managed in Settings; an import does not change the switches or those sections once stored.");
    expect(authority(overview({ generation: 5, active_generation: 4 })).fallback).toEqual({ stored: 5, active: 4 });
  });
});

describe("refusals explained", () => {
  it("explains a stale generation (409) with the current generation and a reload", () => {
    const e = explainError(new ApiError(409, "settings changed since this generation was read; reload and retry", undefined, { error: "x", generation: 9 }));
    expect(e).toMatchObject({ kind: "stale", title: "Settings changed elsewhere", generation: 9, reload: true });
    expect(e.message).toMatch(/generation 9/);
    expect(e.message).toMatch(/Nothing was saved/);
  });
  it("tells changed downloads apart from a stale generation", () => {
    expect(explainError(new ApiError(409, "downloads changed during save; retry")).kind).toBe("downloads_changed");
  });
  it("explains the continuity refusals (422)", () => {
    const orphan = explainError(new ApiError(422, "settings refused: providers[rd]: removal would orphan 3 active download(s); set orphan_active_downloads to confirm"));
    expect(orphan.kind).toBe("orphan");
    expect(orphan.title).toBe("This would fail 3 active downloads");
    expect(orphan.message).toMatch(/Fail its active downloads/);
    const account = explainError(new ApiError(422, "settings refused: providers[rd]: account change would orphan 2 active download(s): new account cannot see their torrents"));
    expect(account.kind).toBe("account_orphan");
    expect(account.message).toMatch(/Fail its active downloads/);
    for (const msg of ["removal would orphan 3 active download(s); set orphan_active_downloads to confirm", "account change would orphan 2 active download(s): new account cannot see their torrents"]) {
      const r = explainReloadError(new ApiError(422, "settings refused: providers[rd]: " + msg));
      expect(r.message).toMatch(/Wait for those downloads to finish/);
      expect(r.message).not.toMatch(/tick/);
    }
    expect(explainReloadError(new ApiError(422, "settings refused: providers[rd]: 1 add(s) in progress for provider rd; retry shortly")).kind).toBe("adds_in_progress");
    expect(explainError(new ApiError(422, "settings refused: providers[rd]: could not verify 1 active download(s) on the new account; retry")).kind).toBe("unverified");
    expect(explainError(new ApiError(422, "settings refused: providers[rd]: 1 add(s) in progress for provider rd; retry shortly")).kind).toBe("adds_in_progress");
  });
  it("explains a new address without the credential (422)", () => {
    const e = explainError(new ApiError(422, "send the credential again to test a new address: the stored credential is only sent to the saved source's scheme, host and port"));
    expect(e.kind).toBe("readdress");
    expect(e.message).toMatch(/Replace/);
  });
  it("explains a provider order that contradicts a version's provider list", () => {
    const e = explainError(new ApiError(422, 'settings refused: versions[movie-web].providers[1]: "rd" listed out of fallback order (providers[rd].order is 2)', [
      'versions[movie-web].providers[1]: "rd" listed out of fallback order (providers[rd].order is 2)',
    ]));
    expect(e.kind).toBe("order");
    expect(e.message).toMatch(/Libraries editor/);
  });
  it("shows Retry-After on a cooldown (429)", () => {
    const e = explainError(new ApiError(429, "429 Too Many Requests", undefined, { key: "dmm", status: "cooldown", message: "tested recently" }, 17));
    expect(e).toMatchObject({ kind: "cooldown", retryAfter: 17 });
    expect(e.message).toMatch(/try again in 17 s/);
    const now = Date.parse("2026-10-27T12:00:00Z");
    const busy = explainError(new ApiError(429, "429", undefined, { status: "busy", retry_at: "2026-10-27T12:02:00Z" }), now);
    expect(busy).toMatchObject({ kind: "busy", retryAfter: 120 });
    expect(busy.message).toMatch(/try again in 2 min/);
  });
  it("explains an import whose *_env credential is missing", () => {
    expect(explainError(new ApiError(422, "settings: import requires a missing credential")).kind).toBe("missing_credential");
  });
  it("keeps validation detail", () => {
    const e = explainError(new ApiError(422, "settings refused: indexers[x].url: required", ["indexers[x].url: required", "indexers[x].kind: unknown"]));
    expect(e.kind).toBe("validation");
    expect(e.detail).toHaveLength(2);
    expect(e.message).toBe("Nothing was saved.");
  });
  it("names a refused field in words", () => {
    expect(fieldWords("versions[movie-web].library")).toBe("Library movie-web, folder");
    expect(fieldWords("library.versions[tv].roots[0]")).toBe("Library tv, folders");
    expect(fieldWords("indexers[x].url")).toBe("Indexer x, url");
    expect(fieldWords("policy.something[odd]")).toBe("policy.something[odd]");
    const e = explainError(
      new ApiError(422, "settings refused: versions[movie-web].library: directory must exist", ["versions[movie-web].library: directory must exist"], {
        fields: [{ path: "versions[movie-web].library", message: "directory must exist" }],
      }),
    );
    expect(e).toMatchObject({ kind: "validation", title: "Not saved", message: "Nothing was saved. Library movie-web, folder: directory must exist." });
    expect(e.server).toBeUndefined();
  });
});

describe("the 409 flow over the API client", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("sends the generation and turns a stale answer into a reload offer", async () => {
    const sent: unknown[] = [];
    vi.stubGlobal("fetch", async (_url: string, init: RequestInit) => {
      sent.push(JSON.parse(String(init.body)));
      return new Response(JSON.stringify({ error: "settings changed since this generation was read; reload and retry", generation: 6 }), { status: 409 });
    });
    const { body } = writeBody({ entity: "indexers", generation: 5, record: { id: 3, key: "dmm", config: {} }, config: { kind: "dmm" }, enabled: true });
    const err = await api("/settings/indexers/3", { method: "PUT", body }).catch((e) => e);
    expect((sent[0] as { generation: number }).generation).toBe(5);
    expect(err).toBeInstanceOf(ApiError);
    expect(explainError(err)).toMatchObject({ kind: "stale", generation: 6, reload: true });
  });
  it("keeps Retry-After from a 429", async () => {
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ key: "dmm", status: "cooldown" }), { status: 429, headers: { "Retry-After": "23" } }));
    const err = (await api("/settings/indexers/1/test", { method: "POST" }).catch((e: unknown) => e)) as ApiError;
    expect(err.retryAfter).toBe(23);
    const a = testErrorAction("dmm", err);
    expect(a.type).toBe("result");
    if (a.type === "result") {
      expect(a.result.status).toBe("cooldown");
      expect(testSummary(a.result).line).toMatch(/try again in 2[23] s/);
    }
  });
});

describe("the editor session", () => {
  afterEach(() => vi.unstubAllGlobals());
  // An indexer whose id is also a provider's: the editor must never send it to the providers' endpoints.
  const prowlarr: SettingsRecord = { id: 7, key: "prowlarr-public", kind: "torznab", enabled: true, priority: 1, config: { kind: "torznab", url: "http://prowlarr:9696/1/api" } };

  it("saves, tests and deletes at the generation the draft was read at, not a newer overview's", () => {
    const s = openEditor("providers", rd, 4);
    // A background refresh (after a candidate test, say) brings generation 5: the open session does not move.
    const newer = overview({ generation: 5, active_generation: 5, providers: [{ ...rd, config: { ...rd.config, budget: "other" } }] });
    expect(newer.generation).toBe(5);
    const st = editorFrom("providers", providerSchema, s.record);
    const { input, refused } = sessionInput(s, "providers", providerSchema, st, 2);
    expect(refused).toBeUndefined();
    expect(writeBody(input!).body.generation).toBe(4);
    expect(candidateBody(input!).body.generation).toBe(4);
    expect(sessionDelete(s, "providers", false).path).toBe("/providers/7?generation=4&orphan_active_downloads=false");
  });
  it("gets the server's 409 for a draft over a change made elsewhere", async () => {
    const sent: { generation: number }[] = [];
    vi.stubGlobal("fetch", async (_url: string, init: RequestInit) => {
      const b = JSON.parse(String(init.body)) as { generation: number };
      sent.push(b);
      if (b.generation !== 5) return new Response(JSON.stringify({ error: "settings changed since this generation was read; reload and retry", generation: 5 }), { status: 409 });
      return new Response(JSON.stringify({ generation: 6 }), { status: 200 });
    });
    const s = openEditor("providers", rd, 4);
    const { input } = sessionInput(s, "providers", providerSchema, editorFrom("providers", providerSchema, rd), 2);
    const err = await api("/settings/providers/7", { method: "PUT", body: writeBody(input!).body }).catch((e: unknown) => e);
    expect(sent[0].generation).toBe(4);
    expect(explainError(err)).toMatchObject({ kind: "stale", generation: 5, reload: true });
  });
  it("refuses to save or delete from another entity's panel", () => {
    const s = openEditor("indexers", prowlarr, 4);
    expect(editorMismatch(s, "indexers")).toBeUndefined();
    expect(editorMismatch(s, "providers")?.title).toBe("This form belongs to another tab");
    const r = sessionInput(s, "providers", providerSchema, editorFrom("indexers", indexerSchema, prowlarr), 2);
    expect(r.input).toBeUndefined();
    expect(r.refused).toBeDefined();
    const d = sessionDelete(s, "providers", true);
    expect(d.path).toBeUndefined();
    expect(d.refused).toBeDefined();
  });
  it("keys the editor by entity and record, not by generation", () => {
    expect(editorKey(openEditor("indexers", prowlarr, 4))).not.toBe(editorKey(openEditor("providers", rd, 4)));
    expect(editorKey(openEditor("indexers", undefined, 4))).not.toBe(editorKey(openEditor("providers", undefined, 4)));
    expect(editorKey(openEditor("providers", rd, 4))).toBe(editorKey(openEditor("providers", { ...rd, enabled: false }, 5)));
  });
  it("moves to the current record and generation only on a reload, and finds a deleted record gone", () => {
    const s = openEditor("providers", rd, 4);
    const changed = { ...rd, enabled: false };
    const r = rebaseEditor(s, overview({ generation: 6, active_generation: 6, providers: [changed] }));
    expect(r).toEqual({ entity: "providers", record: changed, generation: 6 });
    expect(rebaseEditor(s, overview({ generation: 6, active_generation: 6, providers: [] }))).toBeUndefined();
    expect(rebaseEditor(openEditor("budgets", undefined, 4), overview({ generation: 7, active_generation: 7 }))).toEqual({ entity: "budgets", record: undefined, generation: 7 });
  });
});

describe("write bodies", () => {
  it("sends a secret only when replacing it", () => {
    expect(secretPatch("keep", "typed")).toEqual({});
    expect(secretPatch("replace", "new")).toEqual({ secret: "new" });
    expect(secretPatch("replace", "").error).toBeDefined();
    expect(secretPatch("clear", "ignored")).toEqual({ clear_secret: true });
    const keep = writeBody({ entity: "indexers", generation: 2, record: { id: 1, key: "x", config: {}, secret_set: true }, config: {}, secret: { action: "keep", value: "x" } }).body;
    expect(keep).not.toHaveProperty("secret");
    expect(keep).not.toHaveProperty("clear_secret");
  });
  it("states orphan_active_downloads on every provider update, false unless confirmed", () => {
    expect(writeBody({ entity: "providers", generation: 3, record: rd, config: {} }).body.orphan_active_downloads).toBe(false);
    expect(writeBody({ entity: "providers", generation: 3, record: rd, config: {}, orphan: true }).body.orphan_active_downloads).toBe(true);
    expect(writeBody({ entity: "providers", generation: 3, key: "new", config: {} }).body).not.toHaveProperty("orphan_active_downloads");
    expect(deletePath("providers", 7, 3)).toBe("/providers/7?generation=3&orphan_active_downloads=false");
    expect(deletePath("indexers", 2, 3)).toBe("/indexers/2?generation=3");
  });
  it("never sends enabled or a credential for a budget", () => {
    const b = writeBody({ entity: "budgets", generation: 1, key: "b", config: { requests: 1, per: "1m" }, enabled: true, secret: { action: "replace", value: "x" } }).body;
    expect(b).toEqual({ generation: 1, key: "b", config: { requests: 1, per: "1m" } });
  });
  it("builds candidate tests by id with no orphan flag", () => {
    const { body } = candidateBody({ entity: "providers", generation: 3, record: rd, config: { url: "https://x" }, orphan: true });
    expect(body.id).toBe(7);
    expect(body).not.toHaveProperty("orphan_active_downloads");
  });
  it("asks for the credential again when a saved address changes", () => {
    expect(sourceAddress("https://API.example.com/v1")).toBe("https://api.example.com:443");
    expect(needsCredentialAgain(rd, { url: "https://api.real-debrid.com:443/rest/2.0" }, "keep")).toBe(false);
    expect(needsCredentialAgain(rd, { url: "https://other.example.com/rest/1.0" }, "keep")).toBe(true);
    expect(needsCredentialAgain(rd, { url: "https://other.example.com/rest/1.0" }, "replace")).toBe(false);
    expect(needsCredentialAgain({ ...rd, secret_set: false }, { url: "https://other.example.com" }, "keep")).toBe(false);
    // The kind, a cardigann definition and its hosts are part of the address too.
    expect(needsCredentialAgain(rd, { kind: "torbox" }, "keep")).toBe(true);
    const site = { ...rd, kind: "cardigann", config: { url: "https://site.test/", definition: "site", hosts: ["b.test", "a.test"] } };
    expect(needsCredentialAgain(site, { hosts: "A.test, b.test" }, "keep")).toBe(false);
    expect(needsCredentialAgain(site, { hosts: "a.test, b.test, evil.test" }, "keep")).toBe(true);
    expect(needsCredentialAgain(site, { definition: "other" }, "keep")).toBe(true);
  });
});

describe("forms from the schema", () => {
  const fields = [
    { name: "url", type: "string", required: true, help: "" },
    { name: "page_cap", type: "integer", help: "" },
    { name: "multi_languages", type: "list", help: "" },
    { name: "privacy", type: "enum", enum: ["public", "private"], help: "" },
    { name: "seadex", type: "object", help: "", fields: [{ name: "tags", type: "map", help: "" }, { name: "best", type: "integer", help: "" }] },
  ];
  it("types the values and reports bad ones", () => {
    const r = configFromForm(fields, { url: " http://x ", page_cap: "200", multi_languages: "French, German", privacy: "public", seadex: { tags: "HDR=20, Patch Required=-1000", best: "" } }, undefined);
    expect(r.errors).toEqual({});
    expect(r.config).toEqual({ url: "http://x", page_cap: 200, multi_languages: ["French", "German"], privacy: "public", seadex: { tags: { HDR: 20, "Patch Required": -1000 } } });
    const bad = configFromForm(fields, { url: "", page_cap: "lots", privacy: "nope", seadex: { tags: "HDR", best: "" } }, undefined);
    expect(Object.keys(bad.errors).sort()).toEqual(["page_cap", "privacy", "seadex.tags", "url"]);
  });
  it("reads a cardigann indexer's settings as name=value text", () => {
    const settings: SchemaField[] = [{ name: "settings", type: "string_map", help: "" }];
    expect(configFromForm(settings, { settings: "sort=seeders, type=desc\nfreeleech=true" }, undefined).config).toEqual({ settings: { sort: "seeders", type: "desc", freeleech: "true" } });
    expect(configFromForm(settings, { settings: "seeders" }, undefined).errors).toEqual({ settings: "write name=value, separated by commas" });
    expect(formFromConfig(settings, { settings: { sort: "seeders" } })).toEqual({ settings: "sort=seeders" });
  });
  it("sends a cleared field that was set as its zero value on an edit", () => {
    const r = configFromForm(fields, { url: "http://x", page_cap: "", multi_languages: "", privacy: "" }, { url: "http://x", page_cap: 200, privacy: "public" });
    expect(r.config).toEqual({ url: "http://x", page_cap: 0, privacy: "" });
  });
  it("sends a cleared nullable field as null so it takes its default", () => {
    const seeders: SchemaField[] = [{ name: "min_seeders", type: "integer", nullable: true, help: "" }];
    expect(configFromForm(seeders, { min_seeders: "" }, { min_seeders: 5 }).config).toEqual({ min_seeders: null });
    expect(configFromForm(seeders, { min_seeders: "" }, { min_seeders: 0 }).config).toEqual({ min_seeders: null });
    expect(configFromForm(seeders, { min_seeders: "" }, {}).config).toEqual({});
    expect(configFromForm(seeders, { min_seeders: "0" }, { min_seeders: 5 }).config).toEqual({ min_seeders: 0 });
  });
  it("starts a provider editor with the orphan choice off and keeps the order", () => {
    const st = editorFrom("providers", providerSchema, rd);
    expect(st.orphan).toBe(false);
    expect(st.secretAction).toBe("keep");
    expect(st.config).not.toHaveProperty("order");
    const { input } = editorInput("providers", providerSchema, st, 4, rd, 9);
    expect(input?.config).toMatchObject({ kind: "realdebrid", order: 1, url: rd.config.url, pacing: { adds_per_minute: 10, cooldown: "10m" } });
    expect(writeBody(input!).body.orphan_active_downloads).toBe(false);
    const fresh = editorFrom("providers", providerSchema);
    expect(fresh.enabled).toBe(false);
    expect(editorInput("providers", providerSchema, { ...fresh, key: "tb2", config: { ...fresh.config, url: "https://api.torbox.app/v1" } }, 4, undefined, 9).input?.config.order).toBe(9);
  });
});

describe("service addresses of a new record", () => {
  it("prefills Torrentio and SeaDex, leaves the others empty, and starts a new indexer on", () => {
    expect(serviceURL("indexers", "torrentio")).toBe("https://torrentio.strem.fun");
    expect(serviceURL("indexers", "realdebrid")).toBe("");
    expect(serviceURL("budgets", "torrentio")).toBe("");
    for (const [kind, url] of [["torrentio", "https://torrentio.strem.fun"], ["seadex", "https://seadex.moe"], ["dmm", ""], ["zilean", ""], ["torznab", ""], ["cardigann", ""]]) {
      const st = editorFrom("indexers", indexerSchema, undefined, kind);
      expect(st.config.url, kind).toBe(url);
      expect(st.enabled).toBe(true);
      expect(st.key).toBe(kind);
    }
    expect(editorFrom("indexers", indexerSchema).config.url).toBe("");
    expect(editorInput("indexers", indexerSchema, editorFrom("indexers", indexerSchema, undefined, "seadex"), 4, undefined, 1).input).toMatchObject({ enabled: true, config: { kind: "seadex", url: "https://seadex.moe" } });
    // The ticked libraries go with a new indexer only.
    const input = editorInput("indexers", indexerSchema, { ...editorFrom("indexers", indexerSchema, undefined, "seadex"), libraries: ["anime"] }, 4, undefined, 1).input!;
    expect(writeBody(input).body.versions).toEqual(["anime"]);
    expect(writeBody({ ...input, versions: [] }).body).not.toHaveProperty("versions");
  });
  it("never fills a saved record's address and does not count the prefill as an advanced value", () => {
    const saved: SettingsRecord = { id: 9, key: "torrentio", kind: "torrentio", enabled: true, priority: 0, config: { kind: "torrentio", url: "" } };
    expect(editorFrom("indexers", indexerSchema, saved).config.url).toBe("");
    expect(hasAdvancedValues("indexers", indexerSchema, { ...saved, config: { kind: "torrentio", url: "https://torrentio.strem.fun" } })).toBe(false);
  });
});

describe("priority and order", () => {
  const ix = (id: number, key: string, priority: number): SettingsRecord => ({ id, key, priority, config: {} });
  it("swaps neighbours' priorities, renumbering ties", () => {
    expect(movedPriorities([ix(1, "a", 0), ix(2, "b", 5)], 2, -1)).toEqual([{ id: 2, priority: 0 }, { id: 1, priority: 5 }]);
    expect(movedPriorities([ix(1, "a", 0), ix(2, "b", 0), ix(3, "c", 0)], 3, -1)).toEqual([{ id: 1, priority: 0 }, { id: 3, priority: 10 }, { id: 2, priority: 20 }]);
    expect(movedPriorities([ix(1, "a", 0)], 1, -1)).toBeNull();
  });
  it("moves providers and finds versions it would contradict", () => {
    const ps = [{ ...rd }, { ...rd, id: 8, key: "ad", order: 2 }, { ...rd, id: 9, key: "tb", order: 3 }];
    expect(movedOrder(ps, 8, -1)).toEqual([8, 7, 9]);
    expect(movedOrder(ps, 9, 1)).toBeNull();
    const versions = [{ key: "movie-web", providers: ["rd", "ad", "tb"] }, { key: "anime", providers: ["tb"] }];
    expect(orderConflicts(["ad", "rd", "tb"], versions)).toEqual([{ version: "movie-web", providers: ["rd", "ad", "tb"] }]);
    expect(orderConflicts(["rd", "ad", "tb"], versions)).toEqual([]);
  });
});

describe("test-all", () => {
  const res = (key: string, status: SettingsTestResult["status"]): SettingsTestResult => ({ key, status, entity: "indexers", latency_ms: 40 });
  it("is loading over its sources, then holds one result per source", () => {
    let s = testReducer(initialTests, { type: "start-all", keys: ["a", "b"], at: 1 });
    expect(s.all).toBe("running");
    expect(s.running).toEqual({ a: true, b: true });
    s = testReducer(s, { type: "all-done", results: [res("a", "ok"), res("b", "cooldown")] });
    expect(s.all).toBe("idle");
    expect(s.running).toEqual({});
    expect(Object.keys(s.results)).toEqual(["a", "b"]);
    expect(testSummary(s.results.a).tone).toBe("positive");
    expect(testSummary(s.results.b).label).toBe("cooldown");
  });
  it("records a refused test-all without losing earlier results", () => {
    let s = testReducer(initialTests, { type: "result", key: "a", result: res("a", "ok") });
    s = testReducer(s, { type: "start-all", keys: ["a"], at: 1 });
    s = testReducer(s, { type: "all-error", error: explainError(new ApiError(409, "settings changed since this generation was read; reload and retry")) });
    expect(s.all).toBe("idle");
    expect(s.errors["*"].kind).toBe("stale");
    expect(s.results.a.status).toBe("ok");
  });
});

describe("import preview, then apply", () => {
  const preview = {
    generation: 4,
    changes: [
      { entity: "indexers" as const, key: "dmm", operation: "update" as const, credential_changed: true },
      { entity: "providers" as const, key: "tb", operation: "delete" as const },
      { entity: "budgets" as const, key: "new", operation: "create" as const },
    ],
    before: { indexers: [], providers: [], budgets: [] },
    after: { indexers: [], providers: [], budgets: [] },
    yaml: "indexers: {}\n",
    sections_note: "Settings sections are kept as stored; none is filled from the files.",
    sections_from_files: [],
  };
  it("needs a preview and an explicit confirmation before apply; the orphan choice starts off", () => {
    let s = importReducer(initialImport, { type: "apply-start" });
    expect(s.step).toBe("idle");
    s = importReducer(s, { type: "preview-start" });
    s = importReducer(s, { type: "preview-done", preview });
    expect(s).toMatchObject({ step: "preview", confirmed: false, orphan: false });
    expect(canApply(s)).toBe(false);
    expect(importReducer(s, { type: "apply-start" }).step).toBe("preview");
    s = importReducer(s, { type: "confirm", value: true });
    expect(canApply(s)).toBe(true);
    s = importReducer(s, { type: "apply-start" });
    expect(s.step).toBe("applying");
    s = importReducer(s, { type: "apply-done", generation: 5 });
    expect(s).toMatchObject({ step: "applied", appliedGeneration: 5 });
    expect(importCounts(preview.changes)).toEqual({ create: 1, update: 1, delete: 1, credential: 1 });
  });
  it("drops a stale preview, and keeps the preview (unconfirmed) on other refusals", () => {
    let s = importReducer(importReducer(initialImport, { type: "preview-done", preview }), { type: "confirm", value: true });
    s = importReducer(s, { type: "apply-start" });
    const orphan = importReducer(s, { type: "apply-error", error: explainError(new ApiError(422, "settings refused: providers[tb]: removal would orphan 1 active download(s)")) });
    expect(orphan).toMatchObject({ step: "preview", confirmed: false });
    expect(orphan.preview).toBeDefined();
    const stale = importReducer(s, { type: "apply-error", error: explainError(new ApiError(409, "settings changed", undefined, { generation: 5 })) });
    expect(stale.step).toBe("idle");
    expect(stale.preview).toBeUndefined();
    expect(stale.error?.kind).toBe("stale");
  });
  it("keeps the reviewed import while its separate version confirmation is open", () => {
    let s = importReducer(importReducer(initialImport, { type: "preview-done", preview }), { type: "confirm", value: true });
    s = importReducer(s, { type: "apply-start" });
    s = importReducer(s, { type: "apply-review" });
    expect(s.preview).toBe(preview);
    expect(canApply(s)).toBe(true);
    expect(s.error).toBeUndefined();
  });
});

describe("focus back from a closed editor", () => {
  const keys = ["dmm", "zilean", "seadex"];
  const rec = (key: string) => ({ id: 1, key }) as SettingsRecord;
  it("returns to the record's Edit button, or Add for a new record", () => {
    expect(editorReturn(openEditor("indexers", rec("zilean"), 4), "indexers", keys)).toEqual({ edit: "zilean" });
    expect(editorReturn(openEditor("indexers", undefined, 4), "indexers", keys)).toBe("add");
  });
  it("after a delete, moves to the next record, else the previous one, else Add", () => {
    expect(editorReturn(openEditor("indexers", rec("zilean"), 4), "indexers", keys, true)).toEqual({ edit: "seadex" });
    expect(editorReturn(openEditor("indexers", rec("seadex"), 4), "indexers", keys, true)).toEqual({ edit: "zilean" });
    expect(editorReturn(openEditor("indexers", rec("dmm"), 4), "indexers", ["dmm"], true)).toBe("add");
  });
  it("returns to Add when the record is no longer listed or the form belonged to another tab", () => {
    expect(editorReturn(openEditor("indexers", rec("gone"), 4), "indexers", keys)).toBe("add");
    expect(editorReturn(openEditor("indexers", rec("dmm"), 4), "providers", keys)).toBe("add");
  });
});

describe("test announcements", () => {
  const now = Date.parse("2026-10-28T12:00:00Z");
  const retry = new Date(now + 25_000).toISOString();
  it("says each finished test once, with its outcome", () => {
    expect(testAnnouncement({ type: "start", key: "dmm" }, "indexer", now)).toBeUndefined();
    expect(testAnnouncement({ type: "start-all", keys: ["dmm"], at: now }, "indexer", now)).toBeUndefined();
    expect(testAnnouncement({ type: "result", key: "dmm", result: { key: "dmm", status: "ok", latency_ms: 80, results: 12 } }, "indexer", now)).toBe(
      "Test of dmm: ok, reachable · 80 ms · 12 releases.",
    );
    expect(testAnnouncement({ type: "result", key: "dmm", result: { key: "dmm", status: "cooldown", retry_at: retry } }, "indexer", now)).toBe(
      "Test of dmm: cooldown, tested recently; try again in 25 s.",
    );
    const refused = explainError(new ApiError(503, "unavailable"));
    expect(testAnnouncement({ type: "error", key: "dmm", error: refused }, "indexer", now)).toBe(`Test of dmm refused. ${refused.title}: ${refused.message}`);
  });
  it("sums up test-all, naming the sources that were not ok", () => {
    const text = testAnnouncement(
      {
        type: "all-done",
        results: [
          { key: "dmm", status: "ok" },
          { key: "zilean", status: "cooldown", retry_at: retry },
          { key: "seadex", status: "timeout" },
        ],
      },
      "indexer",
      now,
    );
    expect(text).toBe("Test all finished: 1 of 3 indexers ok. zilean: cooldown, tested recently; try again in 25 s. seadex: timeout, no answer within the test timeout.");
    expect(testAnnouncement({ type: "all-done", results: [{ key: "rd", status: "ok" }] }, "provider", now)).toBe("Test all finished: 1 of 1 provider ok.");
  });
  it("says a refused test-all with the 429 explanation", () => {
    const e = explainError(new ApiError(429, "busy", undefined, { status: "busy" }, 12));
    expect(testAnnouncement({ type: "all-error", error: e }, "indexer", now)).toBe("Test all refused. No test slot free: Other tests are running; try again in 12 s.");
  });
});

describe("a failed test's next step", () => {
  const failed = (category: string, status = "failed") => ({ key: "feed", entity: "indexers", status, category, message: "could not reach the source", latency_ms: 0, tested_at: "2026-11-10T10:00:00Z" }) as Parameters<typeof testSummary>[0];
  it("names what to check for the failures an admin can fix on the form", () => {
    expect(testSummary(failed("connectivity")).next).toContain("that the Theatarr container can reach it");
    expect(testSummary(failed("auth")).next).toContain("API key");
    expect(testSummary(failed("timeout", "timeout")).next).toContain("service is running");
    // The message itself is the server's, unchanged.
    expect(testSummary(failed("connectivity")).line).toBe("could not reach the source");
    expect(testSummary({ ...failed("ok"), status: "ok" }).next).toBeUndefined();
  });
});
