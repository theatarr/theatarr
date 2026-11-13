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
    expect(posterStatus("missing", false, false, undefined, { ...home, versions: [v] })).toBe("upcoming");
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
    expect(copyTone({ monitored: false, coverage: cov(13, 17, { missing: 4 }), acquisition: acq("blocked") }, series)).toBe("warn");
    expect(copyWords({ monitored: false, coverage: cov(0, 17, { missing: 17 }) }, series)).toBe("0 of 17 episodes linked, not monitored");
    expect(copyTone({ monitored: true, coverage: cov(0, 17, { in_flight: 3 }) }, series)).toBe("progress");
  });

  it("gives a whole-title bar the badge's own tone", () => {
    expect(badgeTone("available")).toBe("ok");
    expect(badgeTone("best_known")).toBe("ok");
    expect(badgeTone("upgrade")).toBe("ok");
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

  it("does not call a blocked series download in progress", () => {
    const v = { monitored: true, coverage: cov(13, 17, { missing: 4, in_flight: 2 }), acquisition: acq("blocked", { episodes: 2 }) };
    expect(copyWords(v, series)).toBe("13 of 17 episodes linked, 2 with a blocked download");
  });
});
