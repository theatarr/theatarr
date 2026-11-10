// The settings pages' logic (docs/design/settings.md), kept out of the components so it is unit tested: which
// source of authority is live, request bodies (generation, write-only secrets, the orphan confirmation), what each
// refusal means in plain words, form values from the schema, priority and provider order moves, and the state of
// test-all and import preview/apply.
import { ApiError } from "@/api/client";
import type {
  Entity,
  ImportChange,
  ImportPreview,
  SchemaEntity,
  SchemaField,
  SettingsOverview,
  SettingsRecord,
  SettingsTestResult,
} from "@/api/settings";
import { localDay } from "./format";

// ---- source of authority ----

export type Authority = {
  mode: "db" | "unavailable";
  title: string;
  lines: string[];
  /** Why writes are greyed out, when they are. */
  readOnly?: string;
  /** The stored generation is not the one running (a startup fallback): offer a reload. */
  fallback?: { stored: number; active: number };
};

export const SETTINGS_UNAVAILABLE = "Settings service unavailable.";

export function authority(o: SettingsOverview): Authority {
  if (o.source !== "db" || !o.writable) {
    return {
      mode: "unavailable",
      title: "Settings service unavailable",
      lines: ["The settings service is unavailable. Check System › Status."],
      readOnly: SETTINGS_UNAVAILABLE,
    };
  }
  const out: Authority = {
    mode: "db",
    title: "Settings come from the database",
    lines: [
      `Indexers, providers, budgets, quality profiles, custom formats, format sets, library naming, libraries and views are stored in the settings database (generation ${o.generation}). Saves apply to new operations without a restart; operations already running keep their settings.`,
      `These sections in ${o.policy_dir || "the policy files"} are ignored until you import them again (Import & export). Global switches, arr views and the other policy sections (library and Plex, notifications, metadata, egress and the rest) are managed in Settings; an import does not change the switches or those sections once stored.`,
    ],
  };
  if (o.active_generation && o.active_generation !== o.generation) {
    out.fallback = { stored: o.generation, active: o.active_generation };
  }
  return out;
}

// ---- refusals in plain words ----

export type ExplainedKind =
  | "stale"
  | "downloads_changed"
  | "orphan"
  | "account_orphan"
  | "unverified"
  | "adds_in_progress"
  | "readdress"
  | "missing_credential"
  | "order"
  | "validation"
  | "cooldown"
  | "busy"
  | "bad_request"
  | "not_found"
  | "other";

export type Explained = {
  kind: ExplainedKind;
  title: string;
  /** What it means and what to do. */
  message: string;
  /** The server's own words. */
  server?: string;
  detail?: string[];
  /** stale: the generation the server has now. */
  generation?: number;
  /** cooldown/busy: seconds until a retry can work. */
  retryAfter?: number;
  /** Offer to reload the settings. */
  reload?: boolean;
};

const count = (s: string, re: RegExp) => {
  const m = re.exec(s);
  return m ? Number(m[1]) : undefined;
};

/** Seconds until retry_at, at least 1. */
export function secondsUntil(at: string | undefined, now = Date.now()): number | undefined {
  if (!at) return undefined;
  const t = Date.parse(at);
  return Number.isNaN(t) ? undefined : Math.max(1, Math.ceil((t - now) / 1000));
}

export function retryText(seconds: number | undefined): string {
  if (seconds === undefined) return "try again shortly";
  if (seconds < 90) return `try again in ${seconds} s`;
  return `try again in ${Math.ceil(seconds / 60)} min`;
}

/** A write the server refused until its write-enabling effects are confirmed: a 422 with effects and a token for
 * them. Sending the same request again with the token (X-Confirmation-Token) confirms exactly these effects. */
export type WriteConfirmation = { token: string; effects: { version: string; effect: string }[] };

export function confirmationOf(e: unknown): WriteConfirmation | undefined {
  if (!(e instanceof ApiError) || e.status !== 422 || !e.body || typeof e.body !== "object") return;
  const body = e.body as { effects?: unknown; confirmation?: { token?: unknown } };
  const token = body.confirmation?.token;
  if (!Array.isArray(body.effects) || !body.effects.length || typeof token !== "string" || !token) return;
  return { token, effects: body.effects as WriteConfirmation["effects"] };
}

/** explainError for the reload, which has no "Fail its active downloads" tick: a refusal that would fail active
 * downloads says what works there instead. */
export function explainReloadError(e: unknown): Explained {
  const x = explainError(e);
  if (x.kind !== "orphan" && x.kind !== "account_orphan") return x;
  return {
    ...x,
    message: `${x.title}: the stored settings would fail active downloads, so nothing was reloaded. Wait for those downloads to finish and reload again, or make the provider change in Settings, where "Fail its active downloads" can confirm it.`,
  };
}

export function explainError(e: unknown, now = Date.now()): Explained {
  if (!(e instanceof ApiError)) {
    const msg = e instanceof Error ? e.message : String(e);
    return { kind: "other", title: "Something went wrong", message: msg };
  }
  const server = e.message;
  const detail = e.detail;
  const all = [server, ...(detail ?? [])].join("\n");
  const body = (e.body ?? {}) as { generation?: unknown; status?: unknown; retry_at?: unknown; message?: unknown; category?: unknown };
  switch (e.status) {
    case 409:
      if (/downloads changed during save/.test(server)) {
        return {
          kind: "downloads_changed",
          title: "Downloads changed during the save",
          message: "Active downloads for this provider changed while the save was being checked, so nothing was saved. Try again.",
          server,
        };
      }
      return {
        kind: "stale",
        title: "Settings changed elsewhere",
        message:
          "Someone (another tab, another admin or an import) saved settings after this page loaded" +
          (typeof body.generation === "number" ? ` (they are now at generation ${body.generation})` : "") +
          ". Nothing was saved. Reload to see the current settings, then make your change again.",
        server,
        generation: typeof body.generation === "number" ? body.generation : undefined,
        reload: true,
      };
    case 422: {
      if (/send the credential again/.test(all)) {
        return {
          kind: "readdress",
          title: "Enter the credential again for the new address",
          message:
            "This source's address (kind, scheme, host, port, definition or hosts) changed. A test only sends the stored credential to the saved address, so choose Replace and enter the credential again to test the new address. Saving the new address with the stored credential asks you to confirm instead.",
          server,
        };
      }
      if (/import requires a missing credential/.test(all)) {
        return {
          kind: "missing_credential",
          title: "A credential the policy files name is not set",
          message:
            "An indexer or provider in the policy files names a *_env variable that is not set in Theatarr's environment, so the import cannot resolve its credential. Nothing was changed. Set the variable for theatarr serve (or remove the api_key_env line) and preview again.",
          server,
        };
      }
      const accountN = count(all, /account change would orphan (\d+) active download/);
      if (accountN !== undefined) {
        return {
          kind: "account_orphan",
          title: "The new account cannot see active downloads",
          message: `Changing this provider's account or address would lose ${plural(accountN, "active download")}: the new account cannot see their torrents. Nothing was saved. To go ahead, tick "Fail its active downloads" and save again: those downloads are marked failed ("provider account changed by settings", not blocklisted). Otherwise let them finish first.`,
          server,
          detail,
        };
      }
      const orphanN = count(all, /would orphan (\d+) active download/);
      if (orphanN !== undefined) {
        return {
          kind: "orphan",
          title: `This would fail ${plural(orphanN, "active download")}`,
          message: `Removing or disabling this provider fails its ${plural(orphanN, "active download")} ("provider removed by settings", not blocklisted). Nothing was saved. To go ahead, tick "Fail its active downloads" and save again; otherwise leave the provider as it is until they finish.`,
          server,
          detail,
        };
      }
      const unverifiedN = count(all, /could not verify (\d+) active download/);
      if (unverifiedN !== undefined) {
        return {
          kind: "unverified",
          title: "Could not check the new account",
          message: `${plural(unverifiedN, "active download")} could not be checked on the new account (a timeout or a transient error). Nothing was saved. Try again in a moment.`,
          server,
          detail,
        };
      }
      if (/add\(s\) in progress/.test(all)) {
        return {
          kind: "adds_in_progress",
          title: "Adds in progress",
          message: "The router is adding a download to this provider right now. Nothing was saved. Try again shortly.",
          server,
          detail,
        };
      }
      if (/listed out of fallback order/.test(all)) {
        return {
          kind: "order",
          title: "A library lists the providers in another order",
          message:
            "Every library names its providers in fallback order, and the order here must agree with all of them. Nothing was saved. Change the libraries' provider lists in the Libraries editor first, then reorder here.",
          server,
          detail,
        };
      }
      return { kind: "validation", title: "Refused by validation", message: "Nothing was saved. Fix the fields named below.", server, detail };
    }
    case 429: {
      const busy = body.status === "busy";
      const retryAfter = e.retryAfter ?? secondsUntil(typeof body.retry_at === "string" ? body.retry_at : undefined, now);
      const candidateRate = body.category === "candidate_rate";
      return {
        kind: busy ? "busy" : "cooldown",
        title: busy ? "No test slot free" : candidateRate ? "Too many unsaved tests" : "Tested recently",
        message: busy
          ? `Other tests are running; ${retryText(retryAfter)}.`
          : candidateRate
            ? `Unsaved configurations can be tested 10 times a minute; ${retryText(retryAfter)}.`
            : `Each source can be tested once every 30 seconds; ${retryText(retryAfter)}.`,
        server: typeof body.message === "string" ? body.message : server,
        retryAfter,
      };
    }
    case 400:
      return { kind: "bad_request", title: "The request was refused", message: "Nothing was saved.", server };
    case 404:
      return { kind: "not_found", title: "Not found", message: "This record no longer exists. Reload to see the current settings.", server, reload: true };
  }
  return { kind: "other", title: "Could not save", message: "Nothing was changed.", server, detail };
}

// ---- secrets ----

export type SecretAction = "keep" | "replace" | "clear";

/** The secret part of a body: nothing to keep the stored credential, secret to replace it, clear_secret to remove it. */
export function secretPatch(action: SecretAction, value: string): { secret?: string; clear_secret?: true; error?: string } {
  if (action === "replace") {
    return value === "" ? { error: "Enter the new credential, or keep the stored one." } : { secret: value };
  }
  if (action === "clear") return { clear_secret: true };
  return {};
}

/** A source's address as the server compares it: scheme://host:port, default ports filled in. */
export function sourceAddress(raw: unknown): string {
  if (typeof raw !== "string" || raw === "") return "";
  try {
    const u = new URL(raw);
    const scheme = u.protocol.replace(/:$/, "").toLowerCase();
    const port = u.port || ({ http: "80", https: "443" } as Record<string, string>)[scheme] || "";
    return `${scheme}://${u.hostname.toLowerCase()}:${port}`;
  } catch {
    return "";
  }
}

/** The parts of a saved source's address besides its url (settings.SourceAddress): kind, definition and hosts. */
function addressExtras(kind: unknown, definition: unknown, hosts: unknown): string {
  const list = Array.isArray(hosts) ? hosts.map((h) => String(h).toLowerCase()) : typeof hosts === "string" ? hosts.split(",").map((h) => h.trim().toLowerCase()) : [];
  return [String(kind ?? ""), String(definition ?? ""), [...new Set(list.filter(Boolean))].sort().join(",")].join("|");
}

/**
 * A saved source with a stored credential whose address (kind, url scheme/host/port, definition or hosts) changes:
 * a test needs the credential entered again, and saving it with the stored credential asks for confirmation.
 */
export function needsCredentialAgain(record: SettingsRecord | undefined, config: Record<string, unknown>, action: SecretAction): boolean {
  if (!record || !record.secret_set || action === "replace") return false;
  const saved = record.config ?? {};
  const pick = (name: string) => (name in config && config[name] !== undefined ? config[name] : saved[name]);
  const before = sourceAddress(saved.url) + " " + addressExtras(record.kind, saved.definition, saved.hosts);
  const after = sourceAddress(pick("url")) + " " + addressExtras(pick("kind") ?? record.kind, pick("definition"), pick("hosts"));
  return before !== after;
}

// ---- forms from the schema ----

export type FieldValues = { [name: string]: string | FieldValues };

export const SECRET_FIELDS = new Set(["secret", "clear_secret"]);

/** The config fields that apply to a kind. */
export function fieldsFor(fields: SchemaField[], kind: string): SchemaField[] {
  return fields.filter((f) => !f.kinds?.length || f.kinds.includes(kind));
}

function toText(f: SchemaField, v: unknown): string | FieldValues {
  if (f.type === "object") return formFromConfig(f.fields ?? [], (v ?? {}) as Record<string, unknown>);
  if (v === undefined || v === null) return "";
  if (f.type === "list" && Array.isArray(v)) return v.join(", ");
  if ((f.type === "map" || f.type === "string_map") && typeof v === "object") return Object.entries(v as Record<string, unknown>).map(([k, n]) => `${k}=${n}`).join(", ");
  return String(v);
}

/** Form values from a record's config (every value as text, objects nested). */
export function formFromConfig(fields: SchemaField[], config: Record<string, unknown>): FieldValues {
  const out: FieldValues = {};
  for (const f of fields) out[f.name] = toText(f, config[f.name]);
  return out;
}

function zero(f: SchemaField): unknown {
  switch (f.type) {
    case "integer":
      return 0;
    case "list":
      return [];
    case "map":
    case "string_map":
    case "object":
      return {};
  }
  return "";
}

/** A config object from form values, typed by the schema. On an edit, a cleared field that was set is sent as its
 * zero value, or as null (its default) when the schema marks it nullable; an omitted field keeps the stored value. On
 * a create it is left out. */
export function configFromForm(
  fields: SchemaField[],
  values: FieldValues,
  original: Record<string, unknown> | undefined,
  path = "",
): { config: Record<string, unknown>; errors: Record<string, string> } {
  const config: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  for (const f of fields) {
    const name = path + f.name;
    const raw = values[f.name];
    const was = original?.[f.name];
    if (f.type === "object") {
      const sub = configFromForm(f.fields ?? [], (raw ?? {}) as FieldValues, (was ?? undefined) as Record<string, unknown> | undefined, name + ".");
      Object.assign(errors, sub.errors);
      if (Object.keys(sub.config).length > 0) config[f.name] = sub.config;
      continue;
    }
    const text = typeof raw === "string" ? raw.trim() : "";
    if (text === "") {
      if (f.required) errors[name] = "required";
      else if (f.nullable) {
        if (was !== undefined && was !== null) config[f.name] = null;
      } else if (was !== undefined && was !== null && was !== "" && was !== 0) config[f.name] = zero(f);
      continue;
    }
    switch (f.type) {
      case "integer":
        if (!/^-?\d+$/.test(text)) errors[name] = "must be a whole number";
        else config[f.name] = Number(text);
        break;
      case "list":
        config[f.name] = text.split(/[,\n]/).map((x) => x.trim()).filter(Boolean);
        break;
      case "map": {
        const m: Record<string, number> = {};
        for (const part of text.split(/[,\n]/).map((x) => x.trim()).filter(Boolean)) {
          const [k, v] = part.split("=").map((x) => x.trim());
          if (!k || v === undefined || !/^-?\d+$/.test(v)) {
            errors[name] = "write name=number, separated by commas";
            break;
          }
          m[k] = Number(v);
        }
        config[f.name] = m;
        break;
      }
      case "string_map": {
        const m: Record<string, string> = {};
        for (const part of text.split(/[,\n]/).map((x) => x.trim()).filter(Boolean)) {
          const at = part.indexOf("=");
          const k = at > 0 ? part.slice(0, at).trim() : "";
          if (!k) {
            errors[name] = "write name=value, separated by commas";
            break;
          }
          m[k] = part.slice(at + 1).trim();
        }
        config[f.name] = m;
        break;
      }
      case "enum":
        if (f.enum && !f.enum.includes(text)) errors[name] = `one of ${f.enum.join(", ")}`;
        else config[f.name] = text;
        break;
      case "duration":
        if (!/^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(text)) errors[name] = "a duration such as 90s, 5m or 1h";
        else config[f.name] = text;
        break;
      default:
        config[f.name] = text;
    }
  }
  return { config, errors };
}

// ---- request bodies ----

export type WriteInput = {
  entity: Entity;
  generation: number;
  /** The record being edited; absent for a create. */
  record?: SettingsRecord;
  key?: string;
  enabled?: boolean;
  priority?: number;
  config: Record<string, unknown>;
  secret?: { action: SecretAction; value: string };
  /** Providers: the explicit confirmation that disabling or removing it fails its active downloads. */
  orphan?: boolean;
};

/** A create or update body. Every write carries the generation; a provider update always states the orphan choice
 * (false unless the operator confirmed); a secret appears only when it is being replaced. */
export function writeBody(w: WriteInput): { body: Record<string, unknown>; error?: string } {
  const body: Record<string, unknown> = { generation: w.generation, config: w.config };
  if (!w.record) body.key = w.key;
  if (w.entity !== "budgets") {
    if (w.enabled !== undefined) body.enabled = w.enabled;
    const s = secretPatch(w.secret?.action ?? "keep", w.secret?.value ?? "");
    if (s.error) return { body, error: s.error };
    if (s.secret !== undefined) body.secret = s.secret;
    if (s.clear_secret) body.clear_secret = true;
  }
  if (w.entity === "indexers" && w.priority !== undefined) body.priority = w.priority;
  if (w.entity === "providers" && w.record) body.orphan_active_downloads = w.orphan === true;
  return { body };
}

/** An unsaved-candidate test body: a create (key) or changes to a saved record (id); never the orphan flag. */
export function candidateBody(w: WriteInput): { body: Record<string, unknown>; error?: string } {
  const { body, error } = writeBody({ ...w, orphan: undefined });
  delete body.orphan_active_downloads;
  if (w.record) body.id = w.record.id;
  return { body, error };
}

/** The DELETE path: providers always state the orphan choice. */
export function deletePath(entity: Entity, id: number, generation: number, orphan = false): string {
  const q = new URLSearchParams({ generation: String(generation) });
  if (entity === "providers") q.set("orphan_active_downloads", orphan ? "true" : "false");
  return `/${entity}/${id}?${q.toString()}`;
}

// ---- priority and order ----

/** New indexer priorities after moving one up (-1) or down (+1) in the list as shown (sorted by priority, key).
 * Neighbours with different priorities swap them; ties renumber the whole list in steps of 10. */
export function movedPriorities(list: SettingsRecord[], id: number, dir: -1 | 1): { id: number; priority: number }[] | null {
  const i = list.findIndex((r) => r.id === id);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= list.length) return null;
  const a = list[i], b = list[j];
  if ((a.priority ?? 0) !== (b.priority ?? 0)) {
    return [
      { id: a.id!, priority: b.priority ?? 0 },
      { id: b.id!, priority: a.priority ?? 0 },
    ];
  }
  const order = list.slice();
  [order[i], order[j]] = [order[j], order[i]];
  return order.map((r, n) => ({ id: r.id!, priority: n * 10 }));
}

/** The provider ids in fallback order after moving one. */
export function movedOrder(list: SettingsRecord[], id: number, dir: -1 | 1): number[] | null {
  const ids = list.map((r) => r.id!);
  const i = ids.indexOf(id);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= ids.length) return null;
  [ids[i], ids[j]] = [ids[j], ids[i]];
  return ids;
}

/** Versions whose YAML provider list a new fallback order (keys, first first) contradicts. */
export function orderConflicts(order: string[], versions: { key: string; providers: string[] }[]): { version: string; providers: string[] }[] {
  const rank = new Map(order.map((k, i) => [k, i]));
  const out: { version: string; providers: string[] }[] = [];
  for (const v of versions) {
    let last = -1;
    for (const p of v.providers) {
      const r = rank.get(p);
      if (r === undefined) continue;
      if (r < last) {
        out.push({ version: v.key, providers: v.providers });
        break;
      }
      last = r;
    }
  }
  return out;
}

// ---- test results ----

export type Tone = "positive" | "negative" | "warning" | "neutral" | "info";

/** What to do about a failed test, by its category: the checks the admin can make on this form. */
const TEST_NEXT: Record<string, string> = {
  connectivity: "Check the address (host, port, http or https), and that the Theatarr container can reach it: DNS, the Docker network, and the VPN if this source goes through one.",
  timeout: "Check the address and that the service is running, then test again.",
  auth: "Check the API key and enter it again.",
  credential: "Enter the API key, then test again.",
  http: "Check the address and its path; the service answered, but not with a result.",
};

export function testSummary(r: SettingsTestResult, now = Date.now()): { tone: Tone; label: string; line: string; next?: string } {
  const parts: string[] = [];
  if (r.http_status) parts.push(`HTTP ${r.http_status}`);
  if (r.latency_ms) parts.push(`${r.latency_ms} ms`);
  if (r.results !== undefined) parts.push(`${r.results} release${r.results === 1 ? "" : "s"}`);
  if (r.account) {
    parts.push(r.account.premium ? "premium" : "not premium");
    if (r.account.expires) parts.push(`expires ${localDay(r.account.expires)}`);
  }
  const extra = parts.length ? ` · ${parts.join(" · ")}` : "";
  switch (r.status) {
    case "ok":
      return { tone: "positive", label: "ok", line: (r.message || "reachable") + extra };
    case "refused":
      return { tone: "warning", label: "refused", line: (r.message || r.category || "refused") + extra };
    case "timeout":
      return { tone: "negative", label: "timeout", line: r.message || "no answer within the test timeout", next: TEST_NEXT.timeout };
    case "cooldown":
      return { tone: "neutral", label: "cooldown", line: `tested recently; ${retryText(secondsUntil(r.retry_at, now))}` };
    case "busy":
      return { tone: "warning", label: "busy", line: `no test slot free; ${retryText(secondsUntil(r.retry_at, now))}` };
  }
  return { tone: "negative", label: r.status ?? "failed", line: (r.message || r.category || "failed") + extra, next: TEST_NEXT[r.category ?? ""] };
}

export type TestState = {
  /** Per-source tests running now, by key. */
  running: Record<string, boolean>;
  /** Test-all: idle, or running since startedAt over these keys. */
  all: "idle" | "running";
  startedAt?: number;
  allKeys?: string[];
  /** The latest result per key from this page (newer than the record's last_test). */
  results: Record<string, SettingsTestResult>;
  /** A refusal per key, or for test-all under "*". */
  errors: Record<string, Explained>;
};

export const initialTests: TestState = { running: {}, all: "idle", results: {}, errors: {} };

export type TestAction =
  | { type: "start"; key: string }
  | { type: "result"; key: string; result: SettingsTestResult }
  | { type: "error"; key: string; error: Explained }
  | { type: "start-all"; keys: string[]; at: number }
  | { type: "all-done"; results: SettingsTestResult[] }
  | { type: "all-error"; error: Explained };

const without = <T>(m: Record<string, T>, k: string) => {
  const out = { ...m };
  delete out[k];
  return out;
};

export function testReducer(s: TestState, a: TestAction): TestState {
  switch (a.type) {
    case "start":
      return { ...s, running: { ...s.running, [a.key]: true }, errors: without(s.errors, a.key) };
    case "result":
      return { ...s, running: without(s.running, a.key), results: { ...s.results, [a.key]: a.result }, errors: without(s.errors, a.key) };
    case "error":
      return { ...s, running: without(s.running, a.key), errors: { ...s.errors, [a.key]: a.error } };
    case "start-all": {
      const running = { ...s.running };
      for (const k of a.keys) running[k] = true;
      return { ...s, all: "running", startedAt: a.at, allKeys: a.keys, running, errors: without(s.errors, "*") };
    }
    case "all-done": {
      const results = { ...s.results };
      let running = { ...s.running };
      for (const k of s.allKeys ?? []) running = without(running, k);
      for (const r of a.results) results[r.key ?? ""] = r;
      return { ...s, all: "idle", startedAt: undefined, allKeys: undefined, running, results };
    }
    case "all-error": {
      let running = { ...s.running };
      for (const k of s.allKeys ?? []) running = without(running, k);
      return { ...s, all: "idle", startedAt: undefined, allKeys: undefined, running, errors: { ...s.errors, "*": a.error } };
    }
  }
}

/** A single test's outcome from an error: a 429 carries the test result (cooldown or busy) in its body. */
export function testErrorAction(key: string, e: unknown): TestAction {
  if (e instanceof ApiError && e.status === 429 && e.body && typeof e.body === "object" && "status" in e.body) {
    const r = e.body as SettingsTestResult;
    if (!r.retry_at && e.retryAfter) r.retry_at = new Date(Date.now() + e.retryAfter * 1000).toISOString();
    return { type: "result", key, result: r };
  }
  return { type: "error", key, error: explainError(e) };
}

const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;

/** What the tests' live region says when a test or test-all finishes, or undefined for an action that finishes
 * nothing. The text is built once, from the action, when the outcome arrives: a later render (a countdown, a refresh)
 * never changes it, so it is never announced again. */
export function testAnnouncement(a: TestAction, noun: string, now = Date.now()): string | undefined {
  const outcome = (r: SettingsTestResult) => {
    const s = testSummary(r, now);
    return `${s.label}, ${s.line}`;
  };
  switch (a.type) {
    case "result":
      return `Test of ${a.key}: ${outcome(a.result)}.`;
    case "error":
      return `Test of ${a.key} refused. ${a.error.title}: ${a.error.message}`;
    case "all-done": {
      const ok = a.results.filter((r) => r.status === "ok").length;
      const rest = a.results.filter((r) => r.status !== "ok").map((r) => `${r.key}: ${outcome(r)}.`);
      return [`Test all finished: ${ok} of ${plural(a.results.length, noun)} ok.`, ...rest].join(" ");
    }
    case "all-error":
      return `Test all refused. ${a.error.title}: ${a.error.message}`;
  }
  return undefined;
}

// ---- import preview, then apply ----

export type ImportState = {
  step: "idle" | "previewing" | "preview" | "applying" | "applied";
  preview?: ImportPreview;
  /** The operator ticked "I understand": apply stays disabled until then. */
  confirmed: boolean;
  /** orphan_active_downloads for the apply; false unless explicitly ticked. */
  orphan: boolean;
  error?: Explained;
  appliedGeneration?: number;
};

export const initialImport: ImportState = { step: "idle", confirmed: false, orphan: false };

export type ImportAction =
  | { type: "preview-start" }
  | { type: "preview-done"; preview: ImportPreview }
  | { type: "preview-error"; error: Explained }
  | { type: "confirm"; value: boolean }
  | { type: "orphan"; value: boolean }
  | { type: "apply-start" }
  | { type: "apply-review" }
  | { type: "apply-done"; generation: number }
  | { type: "apply-error"; error: Explained }
  | { type: "reset" };

export function importReducer(s: ImportState, a: ImportAction): ImportState {
  switch (a.type) {
    case "preview-start":
      return { ...initialImport, step: "previewing" };
    case "preview-done":
      // A new preview always starts unconfirmed, with the orphan choice off.
      return { ...initialImport, step: "preview", preview: a.preview };
    case "preview-error":
      return { ...initialImport, error: a.error };
    case "confirm":
      return s.step === "preview" ? { ...s, confirmed: a.value } : s;
    case "orphan":
      return s.step === "preview" ? { ...s, orphan: a.value } : s;
    case "apply-start":
      return canApply(s) ? { ...s, step: "applying", error: undefined } : s;
    case "apply-review":
      return { ...s, step: "preview", error: undefined };
    case "apply-done":
      return { ...initialImport, step: "applied", appliedGeneration: a.generation };
    case "apply-error":
      // A stale preview must be made again; other refusals keep the preview for another try.
      return a.error.kind === "stale" ? { ...initialImport, error: a.error } : { ...s, step: "preview", confirmed: false, error: a.error };
    case "reset":
      return initialImport;
  }
}

export function canApply(s: ImportState): boolean {
  return s.step === "preview" && s.confirmed && !!s.preview;
}

export function importCounts(changes: ImportChange[]): { create: number; update: number; delete: number; credential: number } {
  const out = { create: 0, update: 0, delete: 0, credential: 0 };
  for (const c of changes) {
    out[c.operation]++;
    if (c.credential_changed) out.credential++;
  }
  return out;
}

/** Whether the preview deletes or changes providers, where the orphan choice matters. */
export function importTouchesProviders(changes: ImportChange[]): boolean {
  return changes.some((c) => c.entity === "providers" && c.operation !== "create");
}

// ---- audit ----

const OPS: Record<string, string> = {
  "bootstrap-import": "first-start import",
  import: "import",
  create: "created",
  update: "changed",
  delete: "deleted",
  test: "tested",
  "test-candidate": "tested (unsaved)",
  save: "saved",
};

export function auditOperation(op: string): string {
  return OPS[op] ?? op;
}

/** A settings history row's entity in the singular: "switch", "indexer", "provider". */
export function auditEntity(entity: string): string {
  return entity === "switches" ? "switch" : entity.replace(/s$/, "");
}

// ---- the record editor ----

export type EditorState = {
  key: string;
  kind: string;
  enabled: boolean;
  priority: string;
  config: FieldValues;
  secretAction: SecretAction;
  secretValue: string;
  /** Providers: fail active downloads if this save disables the provider. Off unless ticked. */
  orphan: boolean;
};

/** The fields the editor shows for a kind: kind has its own selector, and a provider's order is set by the order
 * controls, not typed. */
export function editorFields(entity: Entity, ent: SchemaEntity, kind: string): SchemaField[] {
  return fieldsFor(ent.config, kind).filter((f) => f.name !== "kind" && !(entity === "providers" && f.name === "order"));
}

export const PROVIDER_SERVICES: Record<string, { label: string; url: string }> = {
  realdebrid: { label: "Real-Debrid", url: "https://api.real-debrid.com/rest/1.0" },
  alldebrid: { label: "AllDebrid", url: "https://api.alldebrid.com/v4" },
  torbox: { label: "TorBox", url: "https://api.torbox.app/v1" },
};

/** TMDb's public addresses, which Setup's TMDb step stores for the two metadata sources it creates. */
export const METADATA_SERVICES: Record<string, string> = {
  tmdb: "https://api.themoviedb.org/3",
  "tmdb-images": "https://image.tmdb.org/t/p",
};

/** The public address of the built-in indexers that have one, prefilled in a new indexer's form (the record still
 * starts disabled). Zilean is self-hosted and DMM's feed path depends on the providers, so neither has an entry. */
export const INDEXER_SERVICES: Record<string, { url: string }> = {
  torrentio: { url: "https://torrentio.strem.fun" },
  seadex: { url: "https://seadex.moe" },
};

/** The address a new record of this kind starts with, "" when the service has no public one. */
export function serviceURL(entity: Entity, kind: string): string {
  const services: Record<string, { url: string }> = entity === "providers" ? PROVIDER_SERVICES : entity === "indexers" ? INDEXER_SERVICES : {};
  return services[kind]?.url ?? "";
}

/**
 * Whether a saved record has an advanced value of its own, so its editor opens Show advanced: a value set and not its
 * default (a provider's URL is the service's; an indexer's required URL is always set and does not count), or an
 * indexer priority other than 0.
 */
export function hasAdvancedValues(entity: Entity, ent: SchemaEntity, record: SettingsRecord): boolean {
  if (entity === "budgets") return true;
  if (entity === "indexers" && (record.priority ?? 0) !== 0) return true;
  const kind = record.kind ?? (typeof record.config?.kind === "string" ? record.config.kind : "");
  const set = (fields: SchemaField[], config: Record<string, unknown>): boolean =>
    fields.some((f) => {
      const v = config[f.name];
      if (f.type === "object") return set(f.fields ?? [], v && typeof v === "object" ? (v as Record<string, unknown>) : {});
      if (v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0) || (typeof v === "object" && Object.keys(v).length === 0)) return false;
      if (f.name === "url") return entity === "providers" && v !== PROVIDER_SERVICES[kind]?.url;
      return f.default === undefined || String(v) !== f.default;
    });
  return set(editorFields(entity, ent, kind), (record.config ?? {}) as Record<string, unknown>);
}

/** newKind: the kind a new record starts as (the add-indexer sheet's Torznab & built-in sources), else the schema's first. */
export function editorFrom(entity: Entity, ent: SchemaEntity, record?: SettingsRecord, newKind?: string): EditorState {
  const kind = record?.kind ?? (typeof record?.config?.kind === "string" ? (record.config.kind as string) : (newKind ?? ent.kinds?.[0] ?? ""));
  return {
    key: record?.key ?? (entity === "budgets" ? "" : kind),
    kind: entity === "budgets" ? "" : kind,
    // New sources default to disabled: test first, then enable.
    enabled: record?.enabled ?? false,
    priority: String(record?.priority ?? 0),
    config: formFromConfig(editorFields(entity, ent, kind), (record?.config ?? (entity === "providers" || serviceURL(entity, kind) ? { url: serviceURL(entity, kind) } : {})) as Record<string, unknown>),
    secretAction: "keep",
    secretValue: "",
    orphan: false,
  };
}

/** The write input for an editor state, or the field errors that stop it. */
export function editorInput(
  entity: Entity,
  ent: SchemaEntity,
  st: EditorState,
  generation: number,
  record: SettingsRecord | undefined,
  nextOrder: number,
): { input?: WriteInput; errors: Record<string, string> } {
  const { config, errors } = configFromForm(editorFields(entity, ent, st.kind), st.config, record?.config as Record<string, unknown> | undefined);
  if (!record) {
    if (!st.key.trim()) errors.key = "required";
    else if (!/^[a-z0-9][a-z0-9._-]*$/i.test(st.key.trim())) errors.key = "letters, digits, dot, dash and underscore";
  }
  if (entity !== "budgets") {
    if (!st.kind) errors.kind = "required";
    config.kind = st.kind;
  }
  if (entity === "providers") config.order = record?.order ?? (record?.config?.order as number | undefined) ?? nextOrder;
  let priority: number | undefined;
  if (entity === "indexers") {
    if (!/^\d+$/.test(st.priority.trim())) errors.priority = "a whole number, 0 or more";
    else priority = Number(st.priority.trim());
  }
  if (st.secretAction === "replace" && st.secretValue === "") errors.secret = "enter the new credential, or keep the stored one";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      entity,
      generation,
      record,
      key: st.key.trim(),
      enabled: entity === "budgets" ? undefined : st.enabled,
      priority,
      config,
      secret: { action: st.secretAction, value: st.secretValue },
      orphan: st.orphan,
    },
  };
}

// ---- the editor session ----

/** What an open editor edits, fixed when it opens: the entity, the record (absent for a create) and the generation
 * the draft was read at. Saves, deletes and candidate tests use that generation, never a newer overview's, so a change
 * made elsewhere meanwhile answers 409 instead of being overwritten by the draft. Only the operator's explicit
 * "reload, keep my changes" after a conflict moves it (rebaseEditor). */
export type EditorSession = { entity: Entity; record?: SettingsRecord; generation: number; kind?: string };

/** kind: the kind a new record starts as. */
export function openEditor(entity: Entity, record: SettingsRecord | undefined, generation: number, kind?: string): EditorSession {
  return kind && !record ? { entity, record, generation, kind } : { entity, record, generation };
}

/** The editor's React key: another entity or record never reuses an open editor's draft. The generation is left out
 * so that keeping a draft over a reload does not reset it. */
export function editorKey(s: EditorSession): string {
  return `${s.entity}:${s.record ? `id-${s.record.id ?? s.record.key}` : `new${s.kind ? `-${s.kind}` : ""}`}`;
}

/** Why an editor may not write from this panel: it was opened for another entity (its record id means another
 * record there). */
export function editorMismatch(s: EditorSession, panel: Entity): Explained | undefined {
  if (s.entity === panel) return undefined;
  return {
    kind: "other",
    title: "This form belongs to another tab",
    message: `It was opened for the ${s.entity} tab and cannot save or delete from the ${panel} tab. Close it and open the record again.`,
  };
}

/** The write input for the session's record at the session's generation, or why it is refused. */
export function sessionInput(
  s: EditorSession,
  panel: Entity,
  ent: SchemaEntity,
  st: EditorState,
  nextOrder: number,
): { input?: WriteInput; errors: Record<string, string>; refused?: Explained } {
  const refused = editorMismatch(s, panel);
  if (refused) return { errors: {}, refused };
  return editorInput(s.entity, ent, st, s.generation, s.record, nextOrder);
}

/** The session record's DELETE path at the session's generation, or why it is refused. */
export function sessionDelete(s: EditorSession, panel: Entity, orphan: boolean): { path?: string; refused?: Explained } {
  const refused = editorMismatch(s, panel);
  if (refused) return { refused };
  if (!s.record?.id) return {};
  return { path: deletePath(s.entity, s.record.id, s.generation, orphan) };
}

/** After a 409, the session over freshly read settings: the current record (found by id) at the current generation.
 * The editor then either starts a new draft from it (discard) or keeps the operator's draft (keep), whose save then
 * replaces what changed elsewhere. Undefined when the record was deleted elsewhere. */
export function rebaseEditor(s: EditorSession, o: SettingsOverview): EditorSession | undefined {
  if (!s.record) return { ...s, generation: o.generation };
  const record = o[s.entity].find((r) => (s.record!.id !== undefined ? r.id === s.record!.id : r.key === s.record!.key));
  return record ? { entity: s.entity, record, generation: o.generation } : undefined;
}

export const RECORD_GONE: Explained = {
  kind: "not_found",
  title: "Deleted elsewhere",
  message: "This record was deleted after the form opened (another tab, another admin or an import). Nothing was saved; close the form.",
};

/** Where focus goes when an editor closes: a record's Edit button (by key) or the list's Add button. */
export type EditorReturn = { edit: string } | "add";

/** The control that gets focus back when an editor closes (Close, Cancel, Save, Delete or the other-tab notice):
 * the Edit button of its record, or Add for a new one. After a delete, the next record's Edit button (else the
 * previous one's, else Add); keys is the panel's list as shown, which may still hold the deleted record. A form
 * that belonged to another tab, or whose record is no longer listed, returns to Add. */
export function editorReturn(s: EditorSession, panel: Entity, keys: string[], deleted = false): EditorReturn {
  const key = s.record?.key;
  if (key === undefined || s.entity !== panel) return "add";
  const i = keys.indexOf(key);
  if (i < 0) return "add";
  if (!deleted) return { edit: key };
  const next = keys[i + 1] ?? keys[i - 1];
  return next !== undefined ? { edit: next } : "add";
}
