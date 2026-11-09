import { describe, expect, it } from "vitest";
import type { RequestDetail } from "@/api/requests";
import { approvalWords, requestTimeline, FAILED_WORDS } from "./request-timeline";

const created = "2026-10-26T12:00:00Z";
const decided = "2026-10-26T13:00:00Z";
const updated = "2026-10-26T14:00:00Z";
const base: RequestDetail = {
  id: 1, kind: "movie", anime: false, name: "Alpha", seasons: [], mine: true,
  status: "pending", created_at: created, updated_at: updated,
  versions: [{ key: "movie-web", label: "WEB", available: false, grab_state: {mode:"active",grab:true,router_enabled:true,router_forced_off:false,will_grab:true} }],
  approved_at_creation: false, progress: [],
};

describe("request timeline", () => {
  it("waits for approval with no invented acquisition timestamp", () => {
    const steps = requestTimeline(base);
    expect(steps.map((s) => s.label)).toEqual(["Requested", "Awaiting approval", "Getting ready", "Available"]);
    expect(steps[1]).toMatchObject({ state: "current", words: "Waiting for approval", at: created });
    expect(steps[2]).toMatchObject({ state: "todo", at: undefined });
    expect(steps[3].at).toBeUndefined();
  });
  it("skips the approval wait for approval at creation", () => {
    const steps = requestTimeline({ ...base, status: "approved", approved_at_creation: true, decided_at: created });
    expect(steps.map((s) => s.key)).toEqual(["requested", "ready", "available"]);
    expect(steps[1]).toMatchObject({ state: "current", at: created });
  });
  it.each(["approved", "processing"])("keeps %s in Getting ready", (status) => {
    const steps = requestTimeline({ ...base, status: status as RequestDetail["status"], decided_at: decided });
    expect(steps[2]).toMatchObject({ state: "current", at: decided });
    expect(steps[3].state).toBe("todo");
  });
  it.each(["declined", "canceled"])("ends on the %s branch with the recorded time and note", (status) => {
    const steps = requestTimeline({ ...base, status: status as RequestDetail["status"], decided_at: decided, reason: "Not this time" });
    expect(steps.map((s) => s.key)).toEqual(["requested", "approval", status]);
    expect(steps[2]).toMatchObject({ state: "stopped", at: decided, words: "Not this time" });
  });
  it("uses the recorded available status time only when every requested copy is ready", () => {
    const ready = { ...base, status: "available" as const, versions: [{ ...base.versions[0], available: true }] };
    expect(requestTimeline(ready)[3]).toMatchObject({ state: "done", at: updated });
    expect(requestTimeline({ ...ready, status: "processing" })[3]).toMatchObject({ state: "done", at: undefined });
    expect(requestTimeline({ ...ready, versions: base.versions })[3].state).toBe("todo");
  });
  it("says plainly that a failed request did not work and a manager can look at it, before any paused words", () => {
    for (const fetching of [true, false]) {
      const step = requestTimeline({ ...base, status: "failed", decided_at: decided }, fetching)[2];
      expect(step).toMatchObject({ label: "Did not work", state: "stopped", at: decided, words: FAILED_WORDS });
      expect(step.words).toMatch(/did not work.*manager/i);
      expect(step.words).not.toMatch(/fetched|downloads are off/);
    }
  });
  it("explains active and paused approval without mode jargon", () => {
    expect(approvalWords(false)).toBe("Approval records this request; automatic downloads are paused");
    expect(approvalWords(true)).toContain("searches for a release that matches");
  });
});

it.each(["approved", "processing"])("shows an honest paused %s timeline", (status) => {
  const step = requestTimeline({ ...base, status: status as RequestDetail["status"], decided_at: decided }, false)[2];
  expect(step).toMatchObject({ label: "Not being fetched", state: "current", at: decided });
  expect(step.words).toContain("downloads are off");
});
it("keeps completed acquisition completed even if grabbing is now off", () => {
  const r = { ...base, status: "available" as const, versions: [{ ...base.versions[0], available: true }] };
  expect(requestTimeline(r, false)[2]).toMatchObject({ label: "Getting ready", state: "done" });
});
