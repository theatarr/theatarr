import { describe, expect, it } from "vitest";
import { mediaLine } from "./media";
import { availabilityFilters, posterStatus, stateWords } from "./library";
import { UPGRADE_WORDS, availableIn, belowCount, cutoffLine, managedShort, nextLine, plainUpgrade, resolutionWords, shortfall, sourceWords, tierWords, upgradeBadge, whyOf } from "./upgrade";

const cutoff = { quality: "Remux-2160p", score: 10000, upgrades: true };

describe("upgrade words", () => {
  it("uses one vocabulary for every state", () => {
    const table: [string, string][] = [
      ["cutoff_met", "Preferred quality met"],
      ["best_available", "Best available"],
      ["best_source_seen", "Best source seen so far"],
      ["higher_source_exists", "No suitable release found; a better source is known to exist"],
      ["upgrade_available", "Upgrade found"],
      ["below_cutoff", "Preferred quality not met; no better copy found"],
    ];
    for (const [state, label] of table) {
      expect(UPGRADE_WORDS[state as keyof typeof UPGRADE_WORDS].label).toBe(label);
      const badge = upgradeBadge({ state: state as keyof typeof UPGRADE_WORDS });
      expect(badge?.label).toBe(state === "upgrade_available" ? "Upgrade found" : state === "cutoff_met" ? "Available" : "Best quality known");
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
      stateWords("filled", true).label,
      belowCount({ state: "below_cutoff", below: 3, seen: 1 }),
      plainUpgrade({ state: "below_cutoff", cutoff: { quality: "Remux-2160p", upgrades: true } }),
    ];
    for (const w of words) expect(w).not.toMatch(/below cutoff|at cutoff/i);
    expect(availabilityFilters(true).find((a) => a.id === "upgrade")?.label).toBe("Preferred quality not met");
  });
  it("puts a row's upgrade state on its poster", () => {
    expect(posterStatus("upgrade", false, false, "best_available")).toBe("best_known");
    expect(posterStatus("upgrade", false, false, "best_source_seen")).toBe("best_known");
    expect(posterStatus("upgrade", false, false, "higher_source_exists")).toBe("best_known");
    expect(posterStatus("upgrade", true, false, "upgrade_available")).toBe("upgrade");
    expect(posterStatus("upgrade", false, false, "upgrade_available")).toBe("upgrade");
    expect(posterStatus("upgrade", false, false, "below_cutoff")).toBe("best_known");
    expect(posterStatus("upgrade", false, false, "cutoff_met")).toBe("available");
    // An older server without upgrade_state.
    expect(posterStatus("upgrade", true)).toBe("upgrade");
    expect(posterStatus("upgrade", false)).toBe("best_known");
    expect(posterStatus("available", false, false, "best_available")).toBe("best_known");
    expect(posterStatus("available", false, false, "cutoff_met")).toBe("available");
    expect(posterStatus("available", false)).toBe("available");
    // A family account never sees an upgrade state.
    expect(posterStatus("upgrade", false, true, "best_available")).toBe("available");
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
    expect(known.headline).toBe("Best available");
    expect(known.current).toBe("4K WEB");
    expect(known.preferred).toBe("4K Remux");
    expect(known.reachable).toBe("4K WEB");
    expect(known.evidence).toBe("Known: searches over at least 90 days found nothing above 4K WEB.");
    expect(known.checked).toBe("Checked 1 d ago; next check 2026-10-27.");
    expect(known.coverage).toEqual(["2026-09-01: disc-feed, dmm (partial or cached)"]);
    expect(known.sightings[0]).toContain("Alpha.2160p.WEB-DL");
    expect(known.override).toBeUndefined();
    const likely = whyOf({ ...base, state: "best_source_seen", ceiling: { ...ceiling, status: "likely" } }, now);
    expect(likely.headline).toBe("Best source seen so far");
    expect(likely.evidence).toMatch(/^Provisional/);
    const manual = whyOf(
      { ...base, state: "best_available", ceiling: { ...ceiling, status: "override", override: { tier: "web_2160", note: "No disc exists", by: 3, at: "2026-10-20T12:00:00Z" } } },
      now,
      (id) => (id === 3 ? "Nick" : `user #${id}`),
    );
    expect(manual.evidence).toBe("Set by hand to 4K WEB.");
    expect(manual.override).toBe("Set to 4K WEB by Nick on 2026-10-20: “No disc exists”.");
    // No ceiling: only current against preferred.
    const plain = whyOf({ ...base, state: "below_cutoff" }, now);
    expect(plain.headline).toBe("Preferred quality not met; no better copy found");
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
  it("counts episodes and names the owner", () => {
    expect(belowCount({ state: "below_cutoff", below: 23, seen: 0 })).toBe("23 episodes under preferred quality");
    expect(belowCount({ state: "upgrade_available", below: 12, seen: 1 })).toBe("12 episodes under preferred quality · 1 upgrade found");
    expect(managedShort({ mode: "shadow", managed_by: "sonarr_anime_debrid", can_edit: false, can_search: false, live_releases: false })).toBe(
      "Managed by sonarr_anime_debrid until cutover",
    );
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
    expect(stateWords("filled", true, true)).toEqual({ label: "Available", tone: "positive" });
    expect(stateWords("filled", true).label).toBe("Available · preferred quality not met");
    expect(availabilityFilters(false).map((a) => a.id)).toEqual(["missing", "partial", "available"]);
    expect(availabilityFilters(false).some((a) => /cutoff/i.test(a.label + a.hint))).toBe(false);
    expect(availabilityFilters(true).map((a) => a.id)).toContain("upgrade");
  });
});
