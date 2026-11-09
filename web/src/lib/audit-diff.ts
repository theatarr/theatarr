// The settings audit's field-level diff (components/settings/audit.tsx): which fields an entry changed, with the
// old and new value. Before/after are the server's redacted records (credentials as "set"/"unset" only); a
// credential field is still never shown with a value here, only as added, removed or changed, so nothing that
// looks like a credential reaches the page even if a record carried one.

/** Field names that hold, name or mark a credential: secret, clear_secret, secret_set, api_key_env, token, … */
const SECRET_FIELD = /(^|_)(secret|password|passkey|token|apikey|api_key)(_|$)/i;

/** The entry already names the record and its time: identity and timestamps are not listed as changes (they would
 * fill every create and every save). The full records still show them. */
const SKIP_TOP = new Set(["id", "key", "entity", "created_at", "updated_at"]);

export const REDACTED = "[redacted]";

export type AuditChange = {
  /** Dotted path from the record root, e.g. config.hourly. */
  path: string;
  op: "added" | "removed" | "changed";
  /** The values as short text; both absent for a credential field. */
  before?: string;
  after?: string;
  /** A credential field: shown as changed, never with a value. */
  secret?: boolean;
};

export function isSecretField(name: string): boolean {
  return SECRET_FIELD.test(name);
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/** A copy with every credential field's value replaced by [redacted], except the server's own presence markers
 * (set, unset, true, false), at any depth. For showing a whole record. */
export function redactAudit(v: unknown, name = ""): unknown {
  if (name && isSecretField(name)) {
    return v === "set" || v === "unset" || v === "" || typeof v === "boolean" || v == null ? v : REDACTED;
  }
  if (Array.isArray(v)) return v.map((x) => redactAudit(x));
  if (isObject(v)) return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, redactAudit(x, k)]));
  return v;
}

/** A value as one short line: strings quoted (so "" shows), objects and lists as compact JSON. */
export function auditValue(v: unknown): string {
  if (v === undefined) return "";
  return JSON.stringify(redactAudit(v));
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/** The fields that differ between before and after, depth first in the after record's order, then the removed
 * ones. Nested objects (config, pacing) are walked; lists compare whole. A missing side (a create, a delete, a test
 * row) makes every field added or removed. */
export function auditDiff(before: unknown, after: unknown): AuditChange[] {
  const out: AuditChange[] = [];
  walk(before ?? undefined, after ?? undefined, "", out);
  return out;
}

function walk(a: unknown, b: unknown, path: string, out: AuditChange[]) {
  const name = path.slice(path.lastIndexOf(".") + 1);
  if (path && isSecretField(name)) {
    if (same(a, b)) return;
    out.push({ path, op: a === undefined ? "added" : b === undefined ? "removed" : "changed", secret: true });
    return;
  }
  const ao = isObject(a), bo = isObject(b);
  if ((ao || a === undefined) && (bo || b === undefined) && (ao || bo)) {
    const x = (a ?? {}) as Record<string, unknown>;
    const y = (b ?? {}) as Record<string, unknown>;
    const keys = [...Object.keys(y), ...Object.keys(x).filter((k) => !(k in y))];
    if (keys.length === 0 && path) {
      // An empty object added or removed is still a change.
      out.push(a === undefined ? { path, op: "added", after: "{}" } : { path, op: "removed", before: "{}" });
      return;
    }
    for (const k of keys) {
      if (!path && SKIP_TOP.has(k)) continue;
      walk(x[k], y[k], path ? `${path}.${k}` : k, out);
    }
    return;
  }
  if (same(a, b)) return;
  if (a === undefined) out.push({ path, op: "added", after: auditValue(b) });
  else if (b === undefined) out.push({ path, op: "removed", before: auditValue(a) });
  else out.push({ path, op: "changed", before: auditValue(a), after: auditValue(b) });
}

/** One change as a line of text (the collapsed entry's summary, and the tests). */
export function changeLine(c: AuditChange): string {
  if (c.secret) return `${c.path} ${c.op}`;
  if (c.op === "added") return `${c.path} ${c.after}`;
  if (c.op === "removed") return `${c.path} removed (was ${c.before})`;
  return `${c.path} ${c.before} → ${c.after}`;
}
