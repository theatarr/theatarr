import { describe, expect, it } from "vitest";
import { mediaLine } from "./media";
import { availabilityFilters, posterStatus } from "./library";
import { UPGRADE_WORDS, availableIn, belowCount, cutoffLine, nextLine, plainUpgrade, resolutionWords, reasonWords, shortfall, sourceWords, upgradeBadge, whyOf } from "./upgrade";

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
  it("explains Why", () => {
    const base = { quality: "WEBDL-2160p", cutoff: { quality: "Remux-2160p", upgrades: true, score: 10000 }, score: 1500 };
    // The heading is the badge's quality word; the state's longer wording is the line under it.
    const plain = whyOf({ ...base, state: "below_cutoff" });
    expect(plain).toEqual({ headline: "Preferred quality not met", detail: "Preferred quality not met; no better copy found", current: "4K WEB", preferred: "4K Remux" });
    expect(whyOf({ ...base, state: "upgrade_available" })).toMatchObject({ headline: "Upgrade found", detail: undefined });
  });
  it("writes the cutoff line", () => {
    expect(cutoffLine("Bluray-1080p", 6302, cutoff)).toBe("Bluray-1080p · profile score 6,302 / 10,000 · cutoff Remux-2160p");
    // A user: no scores at all.
    expect(cutoffLine("Bluray-1080p", undefined, { quality: "Remux-2160p", upgrades: true })).toBe("Bluray-1080p · cutoff Remux-2160p");
    expect(cutoffLine("WEBDL-2160p", 1500, { ...cutoff, upgrades: false })).toContain("upgrades off");
  });
  it("says what falls short", () => {
    // The score that falls short leads; a quality cutoff that is a group is not judged by name.
    expect(shortfall(6302, cutoff)).toBe("Score 6,302 of 10,000.");
    expect(shortfall(12000, cutoff)).toBe("");
    expect(shortfall(undefined, cutoff)).toBe("");
    // A cutoff score of zero or below still counts.
    expect(shortfall(-50, { ...cutoff, score: 0 })).toBe("Score -50 of 0.");
  });
  it("says what Theatarr would take next, or that nothing was seen", () => {
    const now = new Date("2026-10-26T12:00:00Z");
    expect(nextLine({ state: "below_cutoff", last_search: "2026-10-23T12:00:00Z" }, now)).toBe("No better release seen yet; last search 3 d ago.");
    expect(nextLine({ state: "below_cutoff" }, now)).toBe("No better release seen yet.");
    const line = nextLine({
      state: "upgrade_available",
      next: { release: "X.S01.Remux", quality: "Bluray-1080p Remux", score: 5000, reason: "better format score: 5000 over 1400", source: "rss", seen_at: "2026-10-26T10:00:00Z" },
    }, now);
    expect(line).toBe("Would take X.S01.Remux (Bluray-1080p Remux · 5,000); reason at that search: better format score: 5,000 over 1,400. Seen 2 h ago by Theatarr's RSS.");
    expect(reasonWords("fills 2 episode(s) with no file")).toBe("fills 2 episodes with no file");
    expect(reasonWords("fills 1 episode(s) with no file")).toBe("fills 1 episode with no file");
    expect(nextLine({ state: "cutoff_met" }, now)).toBe("");
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
      "1920x1080 · x265 10-bit · Opus 2 · Japanese, English · subtitles English, Arabic",
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
