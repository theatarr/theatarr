import { describe, expect, it } from "vitest";
import type { Schemas } from "@/api/client";
import { badgeTone, copyTone, copyWords } from "./library-status";
import { posterStatus } from "./library";

type V = Schemas["VersionBrief"];
const day = "2026-11-12";
const movie = { kind: "movie" as const, release_date: "2025-03-01" };
const series = { kind: "series" as const };
const acq = (stage: NonNullable<V["acquisition"]>["stage"], more: Partial<NonNullable<V["acquisition"]>> = {}) => ({
  stage, words: stage, download_id: 1, downloads: 1, updated_at: "2026-11-12T00:00:00Z", ...more,
});
const cov = (have: number, total: number, more: Partial<NonNullable<V["coverage"]>> = {}) => ({ have, total, aired: total, upcoming: 0, ...more });

describe("copyTone", () => {
  it("colours a movie library by the tone table", () => {
    expect(copyTone({ monitored: true, has_file: true }, movie, day)).toBe("ok");
    expect(copyTone({ monitored: true, has_file: false }, movie, day)).toBe("bad");
    expect(copyTone({ monitored: true, acquisition: acq("downloading", { progress: 37 }) }, movie, day)).toBe("progress");
    expect(copyTone({ monitored: true, acquisition: acq("blocked") }, movie, day)).toBe("warn");
    expect(copyTone({ monitored: false, has_file: false }, movie, day)).toBe("idle");
  });

  it("keeps a library with a file ok when only its upgrade download is blocked", () => {
    const v = { monitored: true, has_file: true, acquisition: acq("blocked", { upgrade: true }) };
    expect(copyTone(v, movie, day)).toBe("ok");
    expect(copyWords(v, movie, day)).toBe("has a file, upgrade download blocked");
    const s = { monitored: true, coverage: cov(17, 17), acquisition: acq("blocked", { upgrade: true }) };
    expect(copyTone(s, series)).toBe("ok");
  });

  it("reads not released yet as idle, not bad, by the badge's own rule", () => {
    const coming = { kind: "movie" as const, release_date: "2026-12-20" };
    const v = { monitored: true, has_file: false, state: "wanted" as const };
    expect(copyTone(v, coming, day)).toBe("idle");
    expect(copyWords(v, coming, day)).toBe("not released yet");
    // A digital release still to come wins over a past cinema date, for the badge and the bar alike.
    const home = { kind: "movie" as const, release_date: "2026-10-01", home_release: "2026-12-01" };
    expect(copyTone(v, home, day)).toBe("idle");
    expect(posterStatus("missing", true, { ...home, versions: [v] })).toBe("upcoming");
  });

  it("colours a series library by its episodes", () => {
    expect(copyTone({ monitored: true, coverage: cov(17, 17) }, series)).toBe("ok");
    expect(copyTone({ monitored: true, coverage: cov(13, 17, { missing: 4, in_flight: 2 }) }, series)).toBe("bad");
    expect(copyTone({ monitored: true, coverage: cov(13, 17, { missing: 4 }), acquisition: acq("blocked") }, series)).toBe("warn");
    expect(copyTone({ monitored: true, coverage: cov(0, 0, { upcoming: 8 }) }, series)).toBe("idle");
    expect(copyTone({ monitored: false, coverage: cov(0, 17) }, series)).toBe("idle");
    // An unmonitored library keeps its missing count from the server; it is not red.
    expect(copyTone({ monitored: false, coverage: cov(0, 17, { missing: 17 }) }, series)).toBe("idle");
    expect(copyTone({ monitored: false, coverage: cov(13, 17, { missing: 4 }) }, series)).toBe("ok");
    expect(copyTone({ monitored: false, coverage: cov(13, 17, { missing: 4 }), acquisition: acq("blocked") }, series)).toBe("ok");
    expect(copyWords({ monitored: false, coverage: cov(0, 17, { missing: 17 }) }, series)).toBe("0 of 17 episodes linked, not monitored");
    expect(copyTone({ monitored: true, coverage: cov(0, 17, { in_flight: 3 }) }, series)).toBe("progress");
  });

  it("gives a whole-title bar the badge's own tone", () => {
    expect(badgeTone("available")).toBe("ok");
    expect(badgeTone("unmonitored")).toBe("idle");
    // Partly available (a movie a monitored library lacks) is amber too.
    expect(badgeTone("partly_available")).toBe("warn");
    expect(badgeTone("wanted")).toBe("bad");
    expect(badgeTone("upcoming")).toBe("idle");
    expect(badgeTone(undefined)).toBe("idle");
  });
});

describe("copyWords", () => {
  it("says each library's status in words", () => {
    expect(copyWords({ monitored: true, coverage: cov(13, 17, { missing: 2, in_flight: 2 }) }, series)).toBe("13 of 17 episodes linked, 2 in progress");
    expect(copyWords({ monitored: true, has_file: false }, movie, day)).toBe("missing");
    expect(copyWords({ monitored: false, has_file: false }, movie, day)).toBe("not monitored");
    expect(copyWords({ monitored: true, acquisition: acq("downloading", { progress: 37 }) }, movie, day)).toBe("downloading 37%");
    expect(copyWords({ monitored: true, has_file: true }, movie, day)).toBe("has a file");
    expect(copyWords({ monitored: true, acquisition: acq("blocked") }, movie, day)).toBe("download blocked");
  });

  it("gives no counts for a series library with a blocked download: the row does not say which episodes it covers", () => {
    // One episode blocked, another downloading: not "both blocked", not "in progress".
    const mixed = { monitored: true, coverage: cov(13, 17, { missing: 4, in_flight: 2 }), acquisition: acq("blocked", { episodes: 2, stages: { blocked: 1, downloading: 1 } }) };
    expect(copyWords(mixed, series)).toBe("13 of 17 episodes linked, a download is blocked");
    expect(copyTone(mixed, series)).toBe("warn");
    // A blocked upgrade of a held episode, nothing missing: no warning.
    const held = { monitored: true, coverage: cov(17, 17, { in_flight: 1 }), acquisition: acq("blocked", { episodes: 1, upgrade: true }) };
    expect(copyTone(held, series)).toBe("ok");
    expect(copyWords(held, series)).toBe("17 of 17 episodes linked, a download is blocked");
    // The only download a blocked upgrade: never "in progress".
    const only = { monitored: true, coverage: cov(2, 3, { in_flight: 1 }), acquisition: acq("blocked", { episodes: 1, upgrade: true }) };
    expect(copyWords(only, series)).toBe("2 of 3 episodes linked, a download is blocked");
    expect(copyTone(only, series)).toBe("ok");
    expect(copyTone({ monitored: true, coverage: cov(0, 3, { in_flight: 1 }), acquisition: acq("blocked") }, series)).toBe("idle");
  });
});

describe("a series library with no episodes known", () => {
  const v = { monitored: true, coverage: { have: 0, total: 0, aired: 0, upcoming: 0 } };
  it("says nothing aired yet only when the badge reads Upcoming, else that the episodes are not known yet", () => {
    const coming = { kind: "series" as const, release_date: "2099-01-01", status: "Returning Series" };
    expect(copyWords(v, coming, day)).toBe("nothing aired yet");
    expect(posterStatus("missing", true, { ...coming, versions: [v] })).toBe("upcoming");
    const old = { kind: "series" as const, release_date: "2009-04-01", status: "Ended" };
    expect(copyWords(v, old, day)).toBe("no episode information yet");
    expect(posterStatus("missing", true, { ...old, versions: [v] })).toBe("not_available");
    expect(copyTone(v, old, day)).toBe("idle");
  });
});
