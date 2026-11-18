import { describe, expect, it } from "vitest";
import { mediaLine } from "./media";
import { availabilityFilters, posterStatus } from "./library";
import { UPGRADE_WORDS, availableIn, belowCount, haveLine, nextLine, plainUpgrade, reasonWords, resolutionWords, sourceWords, stopLine, upgradeBadge, verdictLine, whyLines } from "./upgrade";

const cutoff = { quality: "Remux-2160p", score: 10000, upgrades: true };

describe("upgrade words", () => {
  it("uses one vocabulary for every state", () => {
    const table: [string, string][] = [
      ["cutoff_met", "Preferred quality met"],
      ["upgrade_available", "Upgrade found"],
      ["below_cutoff", "Preferred quality not met; no better copy found"],
    ];
    for (const [state, label] of table) {
      expect(UPGRADE_WORDS[state as keyof typeof UPGRADE_WORDS].label).toBe(label);
      const badge = upgradeBadge({ state: state as keyof typeof UPGRADE_WORDS });
      // The quality word, never the status word Available.
      expect(badge?.label).toBe(state === "upgrade_available" ? "Upgrade found" : state === "cutoff_met" ? "Preferred quality met" : "Preferred quality not met");
      expect(badge?.short).toBe(badge?.label);
      // Preferred quality not met is never a warning.
      if (badge?.label === "Preferred quality not met") expect(badge.tone).toBe("neutral");
      // The short form is the wording's main clause.
      expect(label.startsWith(UPGRADE_WORDS[state as keyof typeof UPGRADE_WORDS].short)).toBe(true);
    }
    expect(Object.keys(UPGRADE_WORDS).sort()).toEqual(table.map(([s]) => s).sort());
    expect(upgradeBadge({ state: "none" })).toBeNull();
    // A pack that keeps some episodes' files: the badge says so.
    const next = { release: "Show.S01.2160p", score: 1, source: "search" as const, seen_at: "2026-11-01T00:00:00Z" };
    expect(upgradeBadge({ state: "upgrade_available", next: { ...next, reason: "better quality: A over B; 2 episode(s) keep their file" } })?.label).toBe("Better pack found");
    expect(upgradeBadge({ state: "upgrade_available", next: { ...next, reason: "better quality: A over B" } })?.label).toBe("Upgrade found");
  });
  it("reads Preferred quality met when only the score is short", () => {
    // The wanted quality held, the score under the target, nothing better found: met, positive.
    expect(upgradeBadge({ state: "below_cutoff", quality_met: true })).toEqual({ ...UPGRADE_WORDS.cutoff_met, label: "Preferred quality met", short: "Preferred quality met" });
    // The quality itself short, or not judged (prefer-SeaDex): unchanged.
    expect(upgradeBadge({ state: "below_cutoff", quality_met: false })?.label).toBe("Preferred quality not met");
    expect(upgradeBadge({ state: "below_cutoff" })?.label).toBe("Preferred quality not met");
    // Upgrade found still wins.
    expect(upgradeBadge({ state: "upgrade_available", quality_met: true })?.label).toBe("Upgrade found");
    // No count of score-only shortfalls and no found line beside it; Why? keeps its three lines.
    const u = { state: "below_cutoff" as const, quality: "Remux-2160p", quality_met: true, below: 23, last_search: "2026-11-13T12:00:00Z" };
    expect(belowCount(u)).toBe("");
    expect(nextLine(u)).toBe("");
    expect(belowCount({ ...u, quality_met: false })).toBe("23 episodes under preferred quality");
    expect(whyLines(u, 8575, cutoff, null, new Date("2026-11-15T12:00:00Z"))).toEqual([
      "You have: Remux-2160p, score 8,575.",
      "Upgrades stop at: Remux-2160p and a score of 10,000.",
      "The quality is reached, the score is not. No better release seen yet (last search 2 d ago).",
    ]);
  });
  it("never says Below cutoff to anyone", () => {
    const words = [
      ...Object.values(UPGRADE_WORDS).flatMap((w) => [w.label, w.short]),
      ...availabilityFilters(true).flatMap((a) => [a.label, a.hint]),
      belowCount({ state: "below_cutoff", below: 3, seen: 1 }),
      plainUpgrade({ state: "below_cutoff", cutoff: { quality: "Remux-2160p", upgrades: true } }),
    ];
    for (const w of words) expect(w).not.toMatch(/below cutoff|at cutoff/i);
    // No status filter is about quality: Preferred quality not met is gone (O1).
    expect(availabilityFilters(true).map((a) => a.label)).not.toContain("Preferred quality not met");
  });
  it("keeps the poster's status word whatever the upgrade state", () => {
    expect(posterStatus("available")).toBe("available");
    expect(posterStatus("partial")).toBe("partly_available");
    // A family account reads watchability.
    expect(posterStatus("available", true)).toBe("available");
  });
  it("says sources in words", () => {
    expect(sourceWords("WEBDL-2160p")).toBe("4K WEB");
    expect(sourceWords("Remux-2160p")).toBe("4K Remux");
    expect(sourceWords("Bluray-2160p Remux")).toBe("4K Remux");
    expect(sourceWords("Bluray-1080p")).toBe("1080p Blu-ray");
    expect(sourceWords("WEBRip-1080p")).toBe("1080p WEB");
    expect(sourceWords("HDTV-720p")).toBe("720p TV");
    expect(sourceWords("DVD")).toBe("DVD");
    expect(sourceWords("WEB 2160p")).toBe("4K WEB");
    expect(sourceWords("Unknown")).toBe("Unknown");
    expect(sourceWords(undefined)).toBe("");
  });
  it("explains Why in three plain lines", () => {
    const now = new Date("2026-11-15T12:00:00Z");
    const seen = "2026-11-11T10:00:00Z";
    // The quality is reached, the score is not.
    expect(whyLines({ state: "below_cutoff", quality: "Remux-2160p", quality_met: true, last_search: "2026-11-13T12:00:00Z" }, 8575, cutoff, null, now)).toEqual([
      "You have: Remux-2160p, score 8,575.",
      "Upgrades stop at: Remux-2160p and a score of 10,000.",
      "The quality is reached, the score is not. No better release seen yet (last search 2 d ago).",
    ]);
    // Below the wanted quality: the server's judging, never a comparison of names (a cutoff can be a group).
    const group = { quality: "WEB 2160p", score: 10000, upgrades: true };
    expect(whyLines({ state: "below_cutoff", quality: "WEBDL-1080p", quality_met: false }, 600, group, null, now)).toEqual([
      "You have: WEBDL-1080p, score 600.",
      "Upgrades stop at: WEB 2160p and a score of 10,000.",
      "Below the wanted quality. No better release seen yet.",
    ]);
    // A prefer-SeaDex library holding the release it recommends: the server does not judge the quality, so no
    // verdict; nothing is said about SeaDex.
    expect(whyLines({ state: "below_cutoff", quality: "Bluray-1080p", last_search: "2026-11-08T12:00:00Z" }, 6200, cutoff, null, now)).toEqual([
      "You have: Bluray-1080p, score 6,200.",
      "Upgrades stop at: Remux-2160p and a score of 10,000.",
      "No better release seen yet (last search 7 d ago).",
    ]);
    // A score never recorded, a profile without upgrades.
    expect(haveLine("DVD", undefined)).toBe("You have: DVD.");
    expect(stopLine({ ...cutoff, upgrades: false })).toBe("Upgrades are off in this library's profile.");
    expect(stopLine(null)).toBe("");
    // The score at the cutoff: the quality verdict alone does not claim a score shortfall.
    expect(verdictLine({ quality_met: true }, 10000, cutoff)).toBe("");
    // A better release found, with the reason stored with the search that saw it (about the file it was judged
    // against: for a series not the row's representative score, which may be another episode's).
    const u = { state: "upgrade_available" as const, quality: "WEBDL-2160p", score: 9000, next: { release: "Conclave.2024.2160p.WEB-DL", quality: "WEBDL-2160p", score: 6200, reason: "better format score: 6200 over 4500", source: "search" as const, seen_at: seen } };
    const found = "Better release found 4 d ago: Conclave.2024.2160p.WEB-DL (WEBDL-2160p, score 6,200): better format score, 6,200 over 4,500.";
    // No attempt on the page (the history there is capped): nothing is claimed about downloading.
    expect(nextLine(u, now, [])).toBe(found);
    expect(nextLine(u, now, [])).not.toContain("9,000");
    const failed = { event: "failed" as const, date: "2026-11-12T09:00:00Z", release_title: "Conclave.2024.2160p.WEB-DL", reason: "Not cached on RD, AD and TB", missed_on: ["rd", "ad", "tb"] };
    const grabbed = { event: "grabbed" as const, date: "2026-11-12T08:00:00Z", release_title: "Conclave.2024.2160p.WEB-DL" };
    expect(nextLine(u, now, [failed, grabbed])).toBe(
      "Better release found Nov 11: Conclave.2024.2160p.WEB-DL (WEBDL-2160p, score 6,200): better format score, 6,200 over 4,500. Tried Nov 12: not cached on RD, AD and TB. Your file is unchanged.",
    );
    // A download in hand for it: nothing said about downloading; without the history neither.
    expect(nextLine(u, now, [grabbed])).toBe(found);
    expect(nextLine(u, now)).toBe(found);
    expect(nextLine({ ...u, next: { ...u.next, reason: undefined } }, now)).toBe("Better release found 4 d ago: Conclave.2024.2160p.WEB-DL (WEBDL-2160p, score 6,200).");
    expect(reasonWords("fills 2 episode(s) with no file")).toBe("fills 2 episodes with no file");
    expect(reasonWords("better quality: Bluray-1080p over WEBDL-1080p")).toBe("better quality, Bluray-1080p over WEBDL-1080p");
    // One kept episode in the singular; an older reason's pack-policy suffix is left out.
    expect(reasonWords("fills 1 episode(s) with no file; 1 episode(s) keep their file (packs: any_upgradable)")).toBe("fills 1 episode with no file; 1 episode keeps its file");
    expect(reasonWords("better quality: Bluray-2160p over WEBDL-2160p; 2 episode(s) keep their file (packs: threshold)")).toBe("better quality, Bluray-2160p over WEBDL-2160p; 2 episodes keep their file");
    expect(nextLine({ state: "cutoff_met" }, now)).toBe("");
    // A pack that keeps some episodes' files was judged once for the whole pack: pack-level words.
    const mixed = { ...u, next: { ...u.next, release: "Show.S01.2160p.WEB-DL", reason: "better quality: WEBDL-2160p over WEBDL-1080p; 18 episode(s) keep their file" } };
    expect(nextLine(mixed, now, [])).toBe(
      "Better pack found for some episodes 4 d ago: Show.S01.2160p.WEB-DL (WEBDL-2160p, score 6,200): better quality, WEBDL-2160p over WEBDL-1080p; 18 episodes keep their file.",
    );
  });
  it("keeps users to plain words", () => {
    expect(plainUpgrade({ state: "below_cutoff", cutoff: { quality: "Remux-2160p", upgrades: true } })).toBe(
      "Preferred quality (4K Remux) not met; no better copy found.",
    );
    expect(plainUpgrade({ state: "upgrade_available" })).toBe("Upgrade found.");
  });
  it("counts episodes", () => {
    expect(belowCount({ state: "below_cutoff", below: 23, seen: 0 })).toBe("23 episodes under preferred quality");
    expect(belowCount({ state: "upgrade_available", below: 12, seen: 1 })).toBe("12 episodes under preferred quality · 1 upgrade found");
  });
});

describe("media line", () => {
  it("joins what is known and drops repeated subtitle languages", () => {
    expect(mediaLine({ resolution: "1920x1080", video_codec: "x265", video_bit_depth: 10, audio_codec: "Opus", audio_channels: 2, audio_languages: "jpn/eng", subtitles: "eng/eng/ara" })).toBe(
      "1920x1080 · x265 10-bit · Opus 2.0 · Japanese, English · subtitles English, Arabic",
    );
    expect(mediaLine(undefined)).toBe("");
  });
});

describe("plain words for accounts without see files", () => {
  it("says the resolution, never the source", () => {
    expect(availableIn("2160p")).toBe("Available in 4K");
    expect(availableIn("1080p")).toBe("Available in 1080p");
    expect(availableIn("Remux-2160p")).toBe("Available in 4K");
    expect(availableIn(undefined)).toBe("Available");
    expect(resolutionWords("DVD")).toBe("");
  });
  it("leaves the cutoff out", () => {
    expect(availabilityFilters(false).map((a) => a.id)).toEqual(["missing", "partial", "available"]);
    expect(availabilityFilters(false).some((a) => /cutoff/i.test(a.label + a.hint))).toBe(false);
  });
});
