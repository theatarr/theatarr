import { describe, expect, it } from "vitest";
import { auditDiff, auditValue, changeLine, redactAudit, REDACTED } from "./audit-diff";

// Rows as GET /settings/audit serves them: the store's records with secret as "set" or "unset".
const seadexBefore = { id: 5, key: "seadex", created_at: "2026-10-19T09:00:00Z", updated_at: "2026-10-19T09:00:00Z", entity: "budgets", enabled: true, config: { requests: 10, per: "1m0s", burst: 0, hourly: 0, daily: 0 }, secret: "" };
const seadexAfter = { ...seadexBefore, updated_at: "2026-10-26T07:00:00Z", config: { ...seadexBefore.config, hourly: 500 } };

describe("auditDiff", () => {
  it("finds a changed value inside the config object, without the timestamps", () => {
    expect(auditDiff(seadexBefore, seadexAfter)).toEqual([{ path: "config.hourly", op: "changed", before: "0", after: "500" }]);
    expect(auditDiff(seadexBefore, seadexAfter).map(changeLine)).toEqual(["config.hourly 0 → 500"]);
  });
  it("walks nested objects and reports added and removed fields", () => {
    const before = { enabled: true, config: { url: "https://a.example", pacing: { adds_per_minute: 10, cooldown: "10m" }, budget: "rd-api" } };
    const after = { enabled: false, config: { url: "https://a.example", pacing: { adds_per_minute: 5, backoff_max: "1h" }, page_cap: 200 } };
    expect(auditDiff(before, after)).toEqual([
      { path: "enabled", op: "changed", before: "true", after: "false" },
      { path: "config.pacing.adds_per_minute", op: "changed", before: "10", after: "5" },
      { path: "config.pacing.backoff_max", op: "added", after: '"1h"' },
      { path: "config.pacing.cooldown", op: "removed", before: '"10m"' },
      { path: "config.page_cap", op: "added", after: "200" },
      { path: "config.budget", op: "removed", before: '"rd-api"' },
    ]);
  });
  it("makes every field added on a create and removed on a delete; a test row shows its outcome", () => {
    // The entry names the record: id, key and entity are not listed as changes.
    const created = auditDiff(null, { id: 9, key: "x", entity: "budgets", enabled: true, config: { requests: 1, per: "1s" } });
    expect(created.map((c) => [c.path, c.op])).toEqual([["enabled", "added"], ["config.requests", "added"], ["config.per", "added"]]);
    expect(auditDiff({ key: "x", enabled: true, config: {} }, null)).toEqual([
      { path: "enabled", op: "removed", before: "true" },
      { path: "config", op: "removed", before: "{}" },
    ]);
    expect(auditDiff(null, { status: "failed" }).map(changeLine)).toEqual(['status "failed"']);
    expect(auditDiff(null, null)).toEqual([]);
  });
  it("compares lists whole and a type change as a change", () => {
    expect(auditDiff({ c: { l: ["a", "b"] } }, { c: { l: ["b", "a"] } })).toEqual([{ path: "c.l", op: "changed", before: '["a","b"]', after: '["b","a"]' }]);
    expect(auditDiff({ c: { pacing: { cooldown: "1m" } } }, { c: { pacing: null } })).toEqual([
      { path: "c.pacing", op: "changed", before: '{"cooldown":"1m"}', after: "null" },
    ]);
  });
  it("shows a credential change as changed, never with a value", () => {
    const changes = auditDiff({ key: "rd", secret: "set", config: { api_key_env: "THEATARR_RD_KEY" } }, { key: "rd", secret: "unset", config: { api_key_env: "THEATARR_RD_KEY2" } });
    expect(changes).toEqual([
      { path: "secret", op: "changed", secret: true },
      { path: "config.api_key_env", op: "changed", secret: true },
    ]);
    expect(changes.map(changeLine)).toEqual(["secret changed", "config.api_key_env changed"]);
    // A record that (wrongly) carried a value: still no value, anywhere.
    const leaky = auditDiff({ secret: "set" }, { secret: "s3cr3t-value", token: "tok-123", config: { password: "pw-1" } });
    expect(leaky.every((c) => c.secret && c.before === undefined && c.after === undefined)).toBe(true);
    expect(JSON.stringify(leaky)).not.toMatch(/s3cr3t-value|tok-123|pw-1/);
    // An unchanged marker is no change.
    expect(auditDiff({ secret: "set" }, { secret: "set" })).toEqual([]);
  });
});

describe("redactAudit", () => {
  it("keeps the presence markers and replaces anything else in a credential field, at any depth", () => {
    const r = redactAudit({ secret: "set", secret_set: true, clear_secret: true, config: { url: "u", token: "tok-123", list: [{ password: "pw-1" }] } });
    expect(r).toEqual({ secret: "set", secret_set: true, clear_secret: true, config: { url: "u", token: REDACTED, list: [{ password: REDACTED }] } });
    expect(redactAudit({ secret: "unset" })).toEqual({ secret: "unset" });
    expect(auditValue({ api_key: "k-9" })).toBe(`{"api_key":"${REDACTED}"}`);
  });
});
