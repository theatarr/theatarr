import { describe, expect, it } from "vitest";
import { mediaLine } from "./media";
import { availabilityFilters, posterStatus } from "./library";
import { UPGRADE_WORDS, availableIn, belowCount, cutoffLine, nextLine, plainUpgrade, resolutionWords, shortfall, sourceWords, tierWords, upgradeBadge, whyOf } from "./upgrade";

const cutoff = { quality: "Remux-2160p", score: 10000, upgrades: true };

describe("upgrade words", () => {
  it("uses one vocabulary for every state", () => {
    const table: [string, string][] = [
      ["cutoff_met", "Preferred quality met"],
      ["best_available", "Best available"],
      ["best_source_seen", "Best source seen so far"],
      ["higher_source_exists", "No suitable release found yet, though a better source exists"],
      ["upgrade_available", "Upgrade found"],
      ["below_cutoff", "Preferred quality not met; no better copy found"],
    ];
    for (const [state, label] of table) {
      expect(UPGRADE_WORDS[state as keyof typeof UPGRADE_WORDS].label).toBe(label);
      const badge = upgradeBadge({ state: state as keyof typeof UPGRADE_WORDS });
      // The quality word, never the status word Available.
      expect(badge?.label).toBe(state === "upgrade_available" ? "Upgrade found" : state === "cutoff_met" ? "Preferred quality met" : "Best quality known");
      expect(badge?.short).toBe(badge?.label);
      // Best quality known is never a warning.
      if (badge?.label === "Best quality known") expect(["neutral", "positive"]).toContain(badge.tone);
      // The short form is the wording's main clause.
      expect(label.startsWith(UPGRADE_WORDS[state as keyof typeof UPGRADE_WORDS].short)).toBe(true);
    }
    expect(Object.keys(UPGRADE_WORDS).sort()).toEqual(table.map(([s]) => s).sort());
    expect(upgradeBadge({ state: "none" })).toBeNull();
    // Why? is backed by ceiling evidence for the ceiling states.
    expect(Object.entries(UPGRADE_WORDS).filter(([, w]) => w.why).map(([s]) => s).sort()).toEqual(["best_available", "best_source_seen", "higher_source_exists"]);
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
    expect(tierWords("uhd_bd")).toBe("4K Blu-ray");
  });
  it("explains Why", () => {
    const now = new Date("2026-10-26T12:00:00Z");
    const base = { quality: "WEBDL-2160p", cutoff: { quality: "Remux-2160p", upgrades: true, score: 10000 }, score: 1500, effective_cutoff: "WEB 2160p" };
    const ceiling = {
      tier: "web_2160" as const,
      label: "2160p WEB",
      checked_at: "2026-10-25T12:00:00Z",
      next_check_at: "2026-10-27T12:00:00Z",
      evidence: [{ source: "arr_history", source_kind: "arr_history", positive_only: true, tier: "web_2160" as const, example: "Alpha.2160p.WEB-DL", count: 1, first_seen_at: "2026-07-01T12:00:00Z", last_seen_at: "2026-07-01T12:00:00Z" }],
      searches: [{ searched_at: "2026-09-01T12:00:00Z", source: "search", best_tier: "web_2160", indexers: [{ indexer: "disc-feed", source_kind: "torznab", season: -1, complete: true }, { indexer: "dmm", source_kind: "dmm", season: -1, complete: false }] }],
    };
    const known = whyOf({ ...base, state: "best_available", ceiling: { ...ceiling, status: "known" } }, now);
    // The heading is the badge's quality word; the state's longer wording is the line under it.
    expect(known.headline).toBe("Best quality known");
    expect(known.detail).toBe("Best available");
    expect(known.current).toBe("4K WEB");
    expect(known.preferred).toBe("4K Remux");
    expect(known.reachable).toBe("4K WEB");
    expect(known.evidence).toBe("Known: searches over at least 90 days found nothing above 4K WEB.");
    expect(known.checked).toBe("Checked 1 d ago; next check Oct 27.");
    expect(known.coverage).toEqual(["Sep 1: disc-feed, dmm (partial or cached)"]);
    // The source in words, its raw key kept for the tooltip; positive-only evidence in plain words.
    expect(known.sightings[0]).toEqual({ text: "4K WEB · Sonarr/Radarr history: Alpha.2160p.WEB-DL · last seen Jul 1 · seen, not confirmed", source: "arr_history" });
    expect(known.override).toBeUndefined();
    const likely = whyOf({ ...base, state: "best_source_seen", ceiling: { ...ceiling, status: "likely" } }, now);
    expect(likely.headline).toBe("Best quality known");
    expect(likely.detail).toBe("Best source seen so far");
    // A better source known: the heading never contradicts the badge that opened it.
    const higher = whyOf({ ...base, state: "higher_source_exists", ceiling: { ...ceiling, status: "known" } }, now);
    expect([higher.headline, higher.detail]).toEqual(["Best quality known", "No suitable release found yet, though a better source exists"]);
    expect(whyOf({ ...base, state: "upgrade_available" }, now)).toMatchObject({ headline: "Upgrade found", detail: undefined });
    expect(likely.evidence).toMatch(/^Provisional/);
    const manual = whyOf(
      { ...base, state: "best_available", ceiling: { ...ceiling, status: "override", override: { tier: "web_2160", note: "No disc exists", by: 3, at: "2026-10-20T12:00:00Z" } } },
      now,
      (id) => (id === 3 ? "Nick" : `user #${id}`),
    );
    expect(manual.evidence).toBe("Set by hand to 4K WEB.");
    expect(manual.override).toBe("Set to 4K WEB by Nick on Oct 20: “No disc exists”.");
    // No ceiling: only current against preferred.
    const plain = whyOf({ ...base, state: "below_cutoff" }, now);
    expect(plain.headline).toBe("Best quality known");
    expect(plain.detail).toBe("Preferred quality not met; no better copy found");
    expect(plain.evidence).toBeUndefined();
    expect(plain.coverage).toEqual([]);
  });
  it("writes the cutoff line", () => {
    expect(cutoffLine("Bluray-1080p", 6302, cutoff)).toBe("Bluray-1080p · profile score 6,302 / 10,000 · cutoff Remux-2160p");
    // A user: no scores at all.
    expect(cutoffLine("Bluray-1080p", undefined, { quality: "Remux-2160p", upgrades: true })).toBe("Bluray-1080p · cutoff Remux-2160p");
    expect(cutoffLine("WEBDL-2160p", 1500, { ...cutoff, upgrades: false })).toContain("upgrades off");
  });
  it("says what falls short", () => {
    expect(shortfall("Bluray-1080p", 6302, cutoff)).toBe("the quality is not Remux-2160p yet and the profile score is 3,698 short");
    expect(shortfall("Remux-2160p", 12000, cutoff)).toBe("");
  });
  it("says what Theatarr would take next, or that nothing was seen", () => {
    const now = new Date("2026-10-26T12:00:00Z");
    expect(nextLine({ state: "below_cutoff", last_search: "2026-10-23T12:00:00Z" }, now)).toBe("No better release seen yet; last search 3 d ago.");
    expect(nextLine({ state: "below_cutoff" }, now)).toBe("No better release seen yet.");
    const line = nextLine({
      state: "upgrade_available",
      next: { release: "X.S01.Remux", quality: "Bluray-1080p Remux", score: 5000, reason: "better format score: 5000 over 1400", source: "rss", seen_at: "2026-10-26T10:00:00Z" },
    }, now);
    expect(line).toBe("Would take X.S01.Remux (Bluray-1080p Remux · 5,000): better format score: 5000 over 1400. Seen 2 h ago by Theatarr's RSS.");
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
      "1920x1080 · x265 10-bit · Opus 2 · jpn/eng · subs eng/ara",
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
