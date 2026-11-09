import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { readiness } from "./title-layout";
import {
  findRequest,
  applicableRequestVersionKeys,
  approvalNotice,
  indexRequests,
  moreSeasonsOnly,
  requestableSeasons,
  requestAction,
  requestDoneWords,
  requestLink,
  requestStateWords,
  requestVersions,
  seasonsToSend,
  settledWords,
  versionFetching,
} from "./request-state";

const pending = { id: 7, status: "pending", kind: "movie", tmdb_id: 101, title_id: null };
const approved = { id: 9, status: "approved", kind: "series", tmdb_id: 202, title_id: 4 };
const declined = { id: 3, status: "declined", kind: "movie", tmdb_id: 303, title_id: 5 };

describe("request state", () => {
  it("finds the caller's open request by title id or by kind and TMDb id", () => {
    const idx = indexRequests([pending, approved, declined]);
    expect(findRequest(idx, { kind: "movie", tmdb_id: 101 })?.id).toBe(7);
    expect(findRequest(idx, { kind: "series", title_id: 4 })?.id).toBe(9);
    expect(findRequest(idx, { kind: "series", tmdb_id: 202 })?.id).toBe(9);
    // A movie and a series can share a TMDb id; closed requests are not "my request".
    expect(findRequest(idx, { kind: "series", tmdb_id: 101 })).toBeUndefined();
    expect(findRequest(idx, { kind: "movie", title_id: 5, tmdb_id: 303 })).toBeUndefined();
    expect(findRequest(indexRequests(undefined), { kind: "movie", tmdb_id: 101 })).toBeUndefined();
  });
  it("keeps the newest of several open requests (the API lists newest first)", () => {
    const idx = indexRequests([{ ...pending, id: 12 }, pending]);
    expect(findRequest(idx, { kind: "movie", tmdb_id: 101 })?.id).toBe(12);
  });
  it("offers View request, Available or Request", () => {
    expect(requestAction({ mine: pending, requestable: 2 })).toEqual({
      kind: "view-request",
      id: 7,
      status: "pending",
      to: "/requests?request=7",
      label: "View request",
    });
    // Nothing left to ask for: never a dead end. With a library title it links there.
    expect(requestAction({ requestable: 0, titleId: 5 })).toEqual({ kind: "available", to: "/title/5", label: "View availability" });
    expect(requestAction({ requestable: 0 })).toEqual({ kind: "available", label: "Available" });
    expect(requestAction({ requestable: 1, titleId: 5 }).kind).toBe("request");
    // Still loading the options: Request, which the sheet settles.
    expect(requestAction({}).kind).toBe("request");
  });
  it("links and words", () => {
    expect(requestLink(42)).toBe("/requests?request=42");
    expect(requestStateWords("pending")).toMatch(/waiting for approval/);
    expect(requestStateWords("processing")).toMatch(/getting ready/);
  });
  it("asks for the versions the title lacks, and series versions with seasons left", () => {
    const opts = [
      { key: "movie-web", label: "4K WEB", fits: true, on: true },
      { key: "movie-remux", label: "4K Remux", fits: true, on: false },
      { key: "anime-1080p", fits: false, on: false },
    ];
    expect(requestVersions(opts, undefined)).toEqual([{ key: "movie-remux", label: "4K Remux" }]);
    expect(requestVersions(opts, ["movie-web"])).toEqual([]);
    expect(applicableRequestVersionKeys(opts, ["movie-web"])).toEqual(["movie-web"]);
    // The account's set fits nothing: every version that fits.
    expect(requestVersions(opts, ["anime-1080p"]).map((v) => v.key)).toEqual(["movie-remux"]);
    const series = [{ key: "tv-2160p", label: "4K", fits: true, on: true, open_seasons: [3, 4] }];
    const wanted = requestVersions(series, undefined);
    expect(wanted).toEqual([{ key: "tv-2160p", label: "4K", more: [3, 4] }]);
    expect(moreSeasonsOnly(wanted)).toBe(true);
    expect(requestVersions([{ ...series[0], open_seasons: [] }], undefined)).toEqual([]);
    expect(moreSeasonsOnly([])).toBe(false);
  });
  it("offers only the seasons some wanted version still lacks", () => {
    const more = [{ key: "tv-2160p", label: "4K", more: [3] }, { key: "tv-1080p", label: "1080p", more: [2, 3] }];
    expect(requestableSeasons(more, [1, 2, 3])).toEqual([2, 3]);
    // A version the title lacks takes every season.
    expect(requestableSeasons([...more, { key: "tv-web", label: "WEB" }], [1, 2, 3])).toEqual([1, 2, 3]);
    // All seasons picked and all requestable: send none (every season); otherwise the picked ones.
    expect(seasonsToSend([1, 2, 3], [1, 2, 3], [1, 2, 3])).toBeUndefined();
    expect(seasonsToSend([2, 3], [2, 3], [1, 2, 3])).toEqual([2, 3]);
    expect(seasonsToSend([1], [1, 2], [1, 2])).toEqual([1]);
  });
  it("calls a settled title Available only when it can be watched", () => {
    expect(settledWords("positive").badge).toBe("Available");
    expect(settledWords("partial").badge).toBe("Getting ready");
    expect(settledWords("neutral")).toMatchObject({ badge: "Waiting for a copy", tone: "neutral" });
    expect(settledWords(undefined).badge).toBe("Waiting for a copy");
  });
  it("names the copies the account can ask for, so the sentence is not about the whole title", () => {
    const w = settledWords("neutral", false, ["4K Remux"]);
    expect(w.sentence).toBe("What you can ask for (4K Remux) is already monitored but not ready to watch yet, and it is not being fetched yet.");
    expect(w.sentence).not.toMatch(/nothing is ready/);
    expect(settledWords("partial", true, ["4K Remux", "4K WEB"]).sentence).toMatch(/^What you can ask for \(4K Remux and 4K WEB\) is already monitored; some/);
    expect(settledWords("positive", true, []).sentence).toBe("Everything you can ask for is in the library.");
  });
  it("says on its way only when a copy is being fetched", () => {
    const gates = (mode: string, grab = true, router_enabled = true) => ({ control: { grab_state: { mode, grab, router_enabled, router_forced_off: false, will_grab: mode === "active" && grab && router_enabled } } });
    expect(versionFetching([gates("active")])).toBe(true);
    expect(versionFetching([gates("shadow")])).toBe(false);
    expect(versionFetching([gates("active", false)])).toBe(false);
    expect(versionFetching([gates("active", true, false)])).toBe(false);
    expect(versionFetching([gates("shadow"), gates("active")])).toBe(true);
    expect(versionFetching([{}])).toBe(true);
    const shadow = settledWords("partial", versionFetching([gates("shadow")]));
    expect(shadow.badge).toBe("Partly available");
    expect(shadow.sentence).not.toMatch(/on its way|being looked for/);
    expect(settledWords("neutral", false)).toMatchObject({ badge: "Not available yet", tone: "neutral" });
    expect(settledWords("neutral", false).sentence).toMatch(/not being fetched/);
  });
  it("uses only the account's applicable copies for request readiness", () => {
    const opts = [
      { key: "movie-web", fits: true, on: true },
      { key: "movie-remux", fits: true, on: true },
    ];
    const versions = [
      { id: 1, key: "movie-web", has_file: true, current: { quality: "WEBDL-2160p" } },
      { id: 2, key: "movie-remux", has_file: false, state: "wanted" },
    ] as Version[];
    const keys = new Set(applicableRequestVersionKeys(opts, ["movie-remux"]));
    expect(readiness("movie", versions.filter((v) => keys.has(v.key ?? ""))).tone).toBe("neutral");
    expect(readiness("movie", versions).tone).toBe("positive");
  });
});

it.each(["approved", "processing"])("does not promise progress for %s while grabbing is off", (status) => {
  expect(requestStateWords(status, false)).toContain("not being fetched");
  expect(requestStateWords(status, false)).not.toContain("getting ready");
});

 it("uses the server grab decision for title and request copies", () => {
  const state = {mode:"active",grab:true,router_enabled:true,router_forced_off:false,will_grab:false};
  expect(versionFetching([{grab_state:state}])).toBe(false);
  expect(versionFetching([{control:{grab_state:state}}])).toBe(false);
 });

describe("request sheet words", () => {
  it("promises auto-approval only for the account's own ask", () => {
    expect(approvalNotice(true)).toContain("If someone already asked for more of this title, a manager approves it.");
    expect(approvalNotice(false)).toBe("Your request needs approval from a manager.");
  });
  it("says why a joined request was not approved", () => {
    expect(requestDoneWords({ approved: true, merged: true })).toMatch(/^Approved automatically/);
    expect(requestDoneWords({ approved: false, merged: true, not_approved: "asks_more" })).toBe(
      "Someone asked for this already, for more than you did, so a manager approves it. You are on that request too.",
    );
    expect(requestDoneWords({ approved: false, merged: true })).toBe("Someone asked for this already; you are on that request too.");
    expect(requestDoneWords({ approved: false, merged: false })).toMatch(/^Sent\./);
  });
  it("tells an account that manages requests it can approve, not to wait for a manager", () => {
    const notice = approvalNotice(true, true);
    expect(notice).toContain("it waits for approval; you can approve it in Requests.");
    expect(notice).not.toContain("a manager approves it");
    const done = requestDoneWords({ approved: false, merged: true, not_approved: "asks_more" }, true);
    expect(done).toContain("so it waits for approval; you can approve it in Requests.");
    expect(done).not.toContain("a manager approves it");
    expect(approvalNotice(true, false)).toContain("a manager approves it.");
  });
});
