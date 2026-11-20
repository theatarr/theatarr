import { describe, expect, it } from "vitest";
import { fmtTime } from "./format";
import { deleteScope, providerProblem, restoreActionWords, usageWord, deleteSelection, filterTorrents, groupCounts, groupKeys, hashesTogether, refSummary, restoreScope, servedByLine, sortTorrents, usageDetail, type DeletePlan, type Torrent, type TorrentReport } from "./torrents";

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
const B = t("rd", "2", "Beta.2021.2160p", 900, "2026-10-20T00:00:00Z", { usage: "used", refs: { link: 2, download: 1 } });
const C = t("tb", "3", "Gamma.2019.720p", 100, "2026-09-01T00:00:00Z", { usage: "unknown", reasons: ["not in the registry yet"] });

const report: TorrentReport = {
  generated_at: "2026-10-29T12:00:00Z",
  providers: [
    { provider: "rd", kind: "realdebrid", ok: true, as_of: "2026-10-29T11:00:00Z", account_torrents: 2, unique_hashes: 2, bytes: 1200, usage: { used: 1, unused: 1, unknown: 0, unused_bytes: 300, unknown_bytes: 0 }, torrents: [A, B] },
    { provider: "tb", kind: "torbox", ok: true, as_of: "2026-10-29T11:00:00Z", account_torrents: 1, unique_hashes: 1, bytes: 100, usage: { used: 0, unused: 0, unknown: 1, unused_bytes: 0, unknown_bytes: 100 }, torrents: [C] },
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
  it("finds an episode number inside a pack whose label shows a range", () => {
    const pack = t("rd", "9", "[SoM] Dragon Ball Z 001-291", 900, "2026-10-01T00:00:00Z", { parsed: "Dragon Ball Z 1–12, 14–291" });
    const r = { ...report, providers: [{ ...report.providers[0], torrents: [A, pack] }] };
    expect(filterTorrents(r, "", "", "dragon 155").map((x) => x.id)).toEqual(["9"]);
    expect(filterTorrents(r, "", "", "dragon 12").map((x) => x.id)).toEqual(["9"]);
    expect(filterTorrents(r, "", "", "dragon 999").map((x) => x.id)).toEqual([]);
    // The range as the row shows it finds it too.
    expect(filterTorrents(r, "", "", "14–291").map((x) => x.id)).toEqual(["9"]);
  });
  it("finds an episode inside a season's run of episodes shown as a range", () => {
    const run = t("rd", "8", "All.American.S07E01-E13.1080p.WEB", 900, "2026-10-01T00:00:00Z", { parsed: "All American S07E01–E04, E06–E13, E15" });
    const r = { ...report, providers: [{ ...report.providers[0], torrents: [A, run] }] };
    expect(filterTorrents(r, "", "", "S07E05").map((x) => x.id)).toEqual([]);
    for (const q of ["S07E02", "s07e10", "All American S07E15", "S07E01–E04", "S07E01E02", "S07E01E02E03E04E06"]) expect(filterTorrents(r, "", "", q).map((x) => x.id)).toEqual(["8"]);
    // A gap without a range is a list too.
    const pair = t("rd", "7", "All.American.S07.Pair.1080p.WEB", 900, "2026-10-01T00:00:00Z", { parsed: "All American S07E01, E03" });
    const r2 = { ...report, providers: [{ ...report.providers[0], torrents: [A, pair] }] };
    for (const q of ["S07E03", "S07E01E03"]) expect(filterTorrents(r2, "", "", q).map((x) => x.id)).toEqual(["7"]);
    expect(filterTorrents(r2, "", "", "S07E02").map((x) => x.id)).toEqual([]);
  });
  it("splits a hash held twice on one account from a hash on several accounts, and counts each for the chosen account", () => {
    const h = "d".repeat(40);
    const rd1 = t("rd", "11", "Dup.2020.1080p", 500, "2026-10-01T00:00:00Z", { hash: h });
    const rd2 = t("rd", "12", "Dup.2020.1080p", 500, "2026-10-02T00:00:00Z", { hash: h });
    const ad = t("ad", "13", "Dup.2020.1080p", 500, "2026-10-03T00:00:00Z", { hash: h });
    const r: TorrentReport = {
      ...report,
      providers: [{ ...report.providers[0], torrents: [A, rd1, rd2] }, { ...report.providers[1], provider: "ad", kind: "alldebrid", torrents: [ad] }],
      duplicate_hash: [{ key: h, label: "Dup", members: [{ provider: "rd", id: "11" }, { provider: "rd", id: "12" }, { provider: "ad", id: "13" }] }],
    };
    expect(filterTorrents(r, "", "duplicate_hash", "").map((x) => x.id)).toEqual(["11", "12"]);
    expect(filterTorrents(r, "ad", "duplicate_hash", "")).toEqual([]);
    expect(filterTorrents(r, "ad", "several_accounts", "").map((x) => x.id)).toEqual(["13"]);
    expect(filterTorrents(r, "", "several_accounts", "").map((x) => x.id)).toEqual(["11", "12", "13"]);
    expect(groupCounts(r, "rd")).toMatchObject({ "": 3, duplicate_hash: 2, several_accounts: 2 });
    expect(groupCounts(r, "ad")).toMatchObject({ "": 1, duplicate_hash: 0, several_accounts: 1 });
    expect(groupCounts(r, "")).toMatchObject({ "": 4, duplicate_hash: 2, several_accounts: 3, unused: 1 });
  });
  it("keeps one hash's copies together, by account and id, the hashes in the chosen order", () => {
    const x = (p: string, id: string, hash: string, bytes: number) => t(p, id, "N", bytes, "2026-10-01T00:00:00Z", { hash });
    const big = "b".repeat(40), small = "c".repeat(40);
    const list = sortTorrents([x("rd", "2", small, 10), x("tb", "5", big, 900), x("rd", "9", big, 100), x("rd", "3", big, 100), x("rd", "1", small, 20)], "size");
    expect(hashesTogether(list).map((y) => `${y.provider}:${y.id}`)).toEqual(["rd:3", "rd:9", "tb:5", "rd:1", "rd:2"]);
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
    expect(refSummary({ link: 2, download: 1 })).toBe("1 download, 2 library links");
    expect(usageDetail(B)).toContain("2 library links");
    expect(usageDetail(C)).toBe("not in the registry yet");
    expect(usageDetail(A)).toMatch(/No file/);
  });

  it("says when a failed copy has a complete copy elsewhere, and only then", () => {
    expect(servedByLine({ served_by: ["tb"] })).toBe("This copy failed. Another complete copy is on TB.");
    expect(servedByLine({ served_by: ["rd", "tb"] })).toBe("This copy failed. Another complete copy is on RD and TB.");
    expect(servedByLine({})).toBe("");
    expect(usageDetail({ ...B, served_by: ["tb"] }).split("\n")[0]).toBe("This copy failed. Another complete copy is on TB.");
    expect(usageDetail(B)).not.toContain("This copy failed");
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
  it("says what it deletes, the backup and the warning", () => {
    const plan: DeletePlan = { items: [], count: 2, bytes: 2048, refused: 1, digest: "d", confirmation_token: "d1", warning: "A full backup of every provider is taken first", deleted: 0, failed: 0 };
    const s = deleteScope(plan);
    expect(s.title).toContain("2 torrents");
    // One backup sentence, saying what is guaranteed.
    expect(s.does.filter((l) => /backup/i.test(l ?? ""))).toEqual(["A backup is taken first. If a torrent due for deletion can't be backed up, nothing is deleted."]);
    expect(s.keeps[0]).toMatch(/1 chosen torrent is refused/);
    expect(s.blocked).toBeUndefined();
    expect(deleteScope({ ...plan, count: 0 }).blocked).toBe("Nothing chosen can be deleted as it is.");
  });
  it("restore preview actions in words", () => {
    expect(restoreActionWords("readd")).toBe("Re-add");
    expect(restoreActionWords("skip_invalid")).toBe("Not a valid hash");
    expect(restoreActionWords("resume")).toBe("Finish the selection");
    expect(restoreActionWords("other")).toBe("other");
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
    const p = providerProblem({ provider: "rd", error: raw }, "RD", now);
    expect(p.message).toBe(`RD is not asked for now: paused after 8 failed requests in a row (last: transport error). Next try ${fmtTime("2026-11-08T09:59:37Z", now)}.`);
    expect(p.message).not.toMatch(/rd:|gateway|T09:59:37Z/);
    expect(p.raw).toBe(raw);
  });
  it("reads a rate-limit pause as the provider asking to slow down, not as repeated failures", () => {
    // The gateway's own text for a 429 with Retry-After, on a target and on a budget.
    for (const raw of [
      "rd: gateway: backing off: provider:rd: backing off: HTTP 429, Retry-After 5m0s (retry at 2026-11-08T09:05:00Z)",
      "rd: gateway: backing off: provider:rd: budget rd backing off: HTTP 429, Retry-After 5m0s (retry at 2026-11-08T09:05:00Z)",
    ]) {
      const message = providerProblem({ provider: "rd", error: raw }, "RD", now).message;
      expect(message).toBe(`RD is not asked for now: the provider asked to slow down (HTTP 429). Next try ${fmtTime("2026-11-08T09:05:00Z", now)}.`);
    }
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: HTTP 503, Retry-After 1m0s" }, "RD", now).message).toBe("RD is not asked for now: the provider asked to slow down (HTTP 503).");
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: HTTP 429 (retry at 2026-11-08T09:05:00Z)" }, "RD", now).message).toMatch(/^RD is not asked for now: the provider asked to slow down \(HTTP 429\)\. Next try /);
  });
  it("keeps a counted streak of 503s as failures and says only paused for an unknown back-off", () => {
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: 3 failures in a row, last: HTTP 503 (retry at 2026-11-08T09:05:00Z)" }, "RD", now).message)
      .toBe(`RD is not asked for now: paused after 3 failed requests in a row (last: HTTP 503). Next try ${fmtTime("2026-11-08T09:05:00Z", now)}.`);
    expect(providerProblem({ provider: "rd", error: "rd: gateway: backing off: provider:rd: backing off: manual (retry at 2026-11-08T09:05:00Z)" }, "RD", now).message)
      .toBe(`RD is paused for now. Next try ${fmtTime("2026-11-08T09:05:00Z", now)}.`);
  });
  it("keeps other errors' text, without the provider's own prefix", () => {
    expect(providerProblem({ provider: "ad", error: "ad: 401 unauthorized" }, "AD", now).message).toBe("Could not read AD just now: 401 unauthorized.");
    expect(providerProblem({ provider: "tb", error: "tb: gateway: budget exhausted: provider:tb: daily cap reached (retry at 2026-11-09T00:00:00Z)" }, "TB", now).message).toMatch(/^TB is not asked for now: its request limit is used up\. Next try /);
  });
  it("says the earlier record is still shown, with its time", () => {
    expect(providerProblem({ provider: "rd", error: "rd: HTTP 500", as_of: "2026-11-08T08:00:00Z" }, "RD", now).message)
      .toBe(`Could not read RD just now: HTTP 500. Showing the record as of ${fmtTime("2026-11-08T08:00:00Z", now)}.`);
  });
});

describe("usage words", () => {
  it("says not worked out for a torrent without a stored answer, unknown for missing data", () => {
    expect(usageWord("unknown", ["its usage is worked out after the next provider sync; Refresh works it out now"])).toBe("not worked out");
    expect(usageWord("unknown", ["not in the registry yet"])).toBe("unknown");
    expect(usageWord("unused")).toBe("unused");
  });
});
