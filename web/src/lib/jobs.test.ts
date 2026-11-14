import { describe, expect, it } from "vitest";
import { attentionCount, canDismiss, canRetry, groupJobs, jobEffect, jobName, jobNotes, needsAttention, type Job } from "./jobs";

const job = (j: Partial<Job>): Job => ({ id: 2, kind: "provider.sync", state: "failed", ...j });

describe("jobs", () => {
  it("notes", () => {
    expect(jobNotes(job({ resolved_by: 9 })).map((n) => n.label)).toEqual(["resolved by #9"]);
    expect(jobNotes(job({ retried_as: 7, dismissed_at: "2026-10-26T10:00:00Z" })).map((n) => n.key)).toEqual(["retried", "dismissed"]);
    expect(jobNotes(job({ state: "queued", retry_of: 2 })).map((n) => n.label)).toEqual(["retry of #2"]);
    expect(jobNotes(job({ state: "done", resolved_by: 3 }))).toEqual([]);
  });
  it("actions", () => {
    expect(canRetry(job({}))).toBe(true);
    expect(canRetry(job({ resolved_by: 4 }))).toBe(false);
    expect(canRetry(job({ retried_as: 4 }))).toBe(false);
    expect(canRetry(job({ state: "done" }))).toBe(false);
    expect(canDismiss(job({ resolved_by: 4 }))).toBe(true);
    expect(canDismiss(job({ dismissed_at: "2026-10-26T10:00:00Z" }))).toBe(false);
    expect(canDismiss(job({ state: "running" }))).toBe(false);
  });
  it("attention count", () => {
    expect(attentionCount({ failed: 3, attention: 0 })).toBe(0);
    expect(attentionCount({ failed: 3 })).toBe(3);
    expect(attentionCount(undefined)).toBe(0);
  });
  it("friendly names keep unknown kinds as they are", () => {
    expect(jobName("provider.sync")).toBe("Sync debrid holdings");
    expect(jobName("db.nightly")).toBe("Nightly database backup");
    expect(jobName("new.kind")).toBe("New kind");
    expect(jobName("router.copy")).toBe("Add a torrent to another provider");
    expect(jobName(undefined)).toBe("Job");
    expect(jobEffect("plex.scan")).not.toBe("");
    expect(jobEffect("new.kind")).toBe("");
    // The RSS job feeds new releases to Live libraries.
    expect(jobName("shadow.rss")).toBe("Check RSS feeds");
    expect(jobEffect("shadow.rss")).toMatch(/new releases for Live libraries may be missed\.$/);
  });
  it("groups attention first, active next, settled and done apart, each job once", () => {
    const failed = [job({ id: 1, attention: true }), job({ id: 2, resolved_by: 9, attention: false }), job({ id: 3, retried_as: 10 })];
    const latest = [
      job({ id: 10, state: "queued", run_at: "2026-10-26T10:05:00Z" }),
      job({ id: 11, state: "running", run_at: "2026-10-26T10:10:00Z" }),
      job({ id: 12, state: "queued", run_at: "2026-10-26T10:00:00Z" }),
      job({ id: 9, state: "done" }),
      job({ id: 1, attention: true }),
      job({ id: 13, dismissed_at: "2026-10-26T10:00:00Z" }),
    ];
    const g = groupJobs(failed, latest);
    expect(g.attention.map((j) => j.id)).toEqual([1]);
    expect(g.active.map((j) => j.id)).toEqual([11, 12, 10]);
    expect(g.settled.map((j) => j.id)).toEqual([2, 3, 13]);
    expect(g.done.map((j) => j.id)).toEqual([9]);
    expect(needsAttention(job({}))).toBe(true);
    expect(needsAttention(job({ state: "done" }))).toBe(false);
  });
});
