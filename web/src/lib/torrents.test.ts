import { describe, expect, it } from "vitest";
import { fmtTime } from "./format";
import { deleteScope, providerProblem, deleteSelection, filterTorrents, groupKeys, refSummary, restoreScope, sortTorrents, usageDetail, type DeletePlan, type Torrent, type TorrentReport } from "./torrents";

const t = (provider: string, id: string, name: string, bytes: number, added: string, extra: Partial<Torrent> = {}): Torrent => ({
  provider,
  id,
  hash: id.padStart(40, "a"),
  name,
  bytes,
  added,
  status: "downloaded",
  state: "downloaded",
  progress: 100,
  files: 1,
  linked_files: 0,
  usage: "unused",
  added_by_theatarr: false,
  ...extra,
});

const A = t("rd", "1", "Alpha.2020.1080p", 300, "2026-10-01T00:00:00Z");
const B = t("rd", "2", "Beta.2021.2160p", 900, "2026-10-20T00:00:00Z", { usage: "used", refs: { link: 2, arr_file: 1 } });
const C = t("tb", "3", "Gamma.2019.720p", 100, "2026-09-01T00:00:00Z", { usage: "unknown", reasons: ["not in the registry yet"] });

const report: TorrentReport = {
  generated_at: "2026-10-29T12:00:00Z",
  providers: [
    { provider: "rd", kind: "realdebrid", ok: true, count: 2, bytes: 1200, usage: { used: 1, unused: 1, unknown: 0, unused_bytes: 300, unknown_bytes: 0 }, torrents: [A, B] },
    { provider: "tb", kind: "torbox", ok: true, count: 1, bytes: 100, usage: { used: 0, unused: 0, unknown: 1, unused_bytes: 0, unknown_bytes: 100 }, torrents: [C] },
  ],
  unused: [{ provider: "rd", id: "1" }],
  duplicate_hash: [],
  duplicate_title: [{ key: "k", label: "Alpha (2020)", members: [{ provider: "rd", id: "1" }, { provider: "tb", id: "3" }] }],
  failed: [],
  stalled: [],
  largest: [{ provider: "rd", id: "2" }],
};

describe("filters", () => {
  it("by provider, group and text", () => {
    expect(filterTorrents(report, "", "", "").map((x) => x.id)).toEqual(["1", "2", "3"]);
    expect(filterTorrents(report, "tb", "", "").map((x) => x.id)).toEqual(["3"]);
    expect(filterTorrents(report, "", "unused", "").map((x) => x.id)).toEqual(["1"]);
    expect(filterTorrents(report, "", "duplicate_title", "").map((x) => x.id)).toEqual(["1", "3"]);
    expect(filterTorrents(report, "", "", "beta").map((x) => x.id)).toEqual(["2"]);
    expect(filterTorrents(report, "", "", " 2160P  beta ").map((x) => x.id)).toEqual(["2"]);
    expect(groupKeys(report, "")).toBeNull();
  });
  it("sorts by size, age and name", () => {
    expect(sortTorrents([A, B, C], "size").map((x) => x.id)).toEqual(["2", "1", "3"]);
    expect(sortTorrents([A, B, C], "age").map((x) => x.id)).toEqual(["3", "1", "2"]);
    expect(sortTorrents([C, B, A], "name").map((x) => x.id)).toEqual(["1", "2", "3"]);
    expect(sortTorrents([A, B, C], "size", false).map((x) => x.id)).toEqual(["3", "1", "2"]);
  });
});

describe("usage", () => {
  it("names the references and the reasons", () => {
    expect(refSummary({ link: 2, arr_file: 1 })).toBe("1 arr file, 2 library links");
    expect(usageDetail(B)).toContain("2 library links");
    expect(usageDetail(C)).toBe("not in the registry yet");
    expect(usageDetail(A)).toMatch(/No file/);
  });
});

describe("delete", () => {
  it("sends the chosen torrents with the ticked overrides", () => {
    const sel = deleteSelection([A, B], { "rd:2": "o1-x" }, false);
    expect(sel).toEqual([
      { provider: "rd", id: "1", hash: A.hash },
      { provider: "rd", id: "2", hash: B.hash, override: true, override_token: "o1-x" },
    ]);
    expect(deleteSelection([A], {}, true)[0].allow_recent).toBe(true);
  });
  it("says what it deletes, the backup and the shared-account warning", () => {
    const plan: DeletePlan = { items: [], count: 2, bytes: 2048, refused: 1, digest: "d", confirmation_token: "d1", warning: "Debrid accounts are shared", deleted: 0, failed: 0 };
    const s = deleteScope(plan);
    expect(s.title).toContain("2 torrents");
    expect(s.does.join(" ")).toMatch(/full backup/);
    expect(s.does).toContain("Debrid accounts are shared");
    expect(s.keeps[0]).toMatch(/1 chosen torrent is refused/);
    expect(s.blocked).toBeUndefined();
    expect(deleteScope({ ...plan, count: 0 }).blocked).toBeTruthy();
  });
  it("restore preview wording", () => {
    const s = restoreScope({ backup_id: "b", items: [], counts: { present: 2, readd: 1, skip_invalid: 1 }, readd: 1, resume: 0, bytes: 10, confirmation_token: "r1" });
    expect(s.title).toBe("Restore 1 torrent");
    expect(s.keeps.join(" ")).toMatch(/1 skipped/);
    expect(restoreScope({ backup_id: "b", items: [], counts: {}, readd: 0, resume: 0, bytes: 0, confirmation_token: "r1" }).blocked).toBeTruthy();
    const r = restoreScope({ backup_id: "b", items: [], counts: { resume: 1 }, readd: 0, resume: 1, bytes: 0, confirmation_token: "r1" });
    expect(r.blocked).toBeUndefined();
    expect(r.does.join(" ")).toMatch(/Finish the file selection of 1 torrent/);
  });
});

describe("an unreadable provider account", () => {
  const now = new Date("2026-11-08T09:00:00Z");
  it("names the provider once, says why in one sentence and gives the next try in local time", () => {
    const raw = "rd: gateway: backing off: provider:rd: backing off: 8 failures in a row, last: transport error (retry at 2026-11-08T09:59:37Z)";
    const p = providerProblem({ provider: "rd", kind: "realdebrid", error: raw }, now);
    expect(p.name).toBe("Real-Debrid (rd)");
    expect(p.message).toBe(`Not asked for now: paused after 8 failed requests in a row (last: transport error). Next try ${fmtTime("2026-11-08T09:59:37Z", now)}.`);
    expect(p.message).not.toMatch(/rd:|gateway|T09:59:37Z/);
    expect(p.raw).toBe(raw);
  });
  it("reads a rate-limit pause as the provider asking to slow down, not as repeated failures", () => {
    // The gateway's own text for a 429 with Retry-After, on a target and on a budget.
    for (const raw of [
      "rd: gateway: backing off: provider:rd: backing off: HTTP 429, Retry-After 5m0s (retry at 2026-11-08T09:05:00Z)",
      "rd: gateway: backing off: provider:rd: budget rd backing off: HTTP 429, Retry-After 5m0s (retry at 2026-11-08T09:05:00Z)",
    ]) {
      const message = providerProblem({ provider: "rd", kind: "realdebrid", error: raw }, now).message;
      expect(message).toBe(`Not asked for now: the provider asked to slow down (HTTP 429). Next try ${fmtTime("2026-11-08T09:05:00Z", now)}.`);
    }
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: HTTP 503, Retry-After 1m0s" }, now).message).toBe("Not asked for now: the provider asked to slow down (HTTP 503).");
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: HTTP 429 (retry at 2026-11-08T09:05:00Z)" }, now).message).toMatch(/^Not asked for now: the provider asked to slow down \(HTTP 429\)\. Next try /);
  });
  it("keeps a counted streak of 503s as failures and says only paused for an unknown back-off", () => {
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: 3 failures in a row, last: HTTP 503 (retry at 2026-11-08T09:05:00Z)" }, now).message)
      .toBe(`Not asked for now: paused after 3 failed requests in a row (last: HTTP 503). Next try ${fmtTime("2026-11-08T09:05:00Z", now)}.`);
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: manual (retry at 2026-11-08T09:05:00Z)" }, now).message)
      .toBe(`Paused for now. Next try ${fmtTime("2026-11-08T09:05:00Z", now)}.`);
  });
  it("keeps other errors' text, without the provider's own prefix", () => {
    expect(providerProblem({ provider: "ad", kind: "alldebrid", error: "ad: 401 unauthorized" }, now).message).toBe("Its torrent list could not be read: 401 unauthorized.");
    expect(providerProblem({ provider: "tb", kind: "torbox", error: "tb: gateway: budget exhausted: provider:tb: daily cap reached (retry at 2026-11-09T00:00:00Z)" }, now).message).toMatch(/^Not asked for now: its request limit is used up\. Next try /);
  });
});
