// Upgrade words (docs/design/manage.md, Words): one vocabulary for the grid, the title page, season headers and episodes,
// for managers. The API's states map onto it (UPGRADE_WORDS); "Below cutoff" is never shown: it stays an internal
// state and filter key. Scores and the cutoff arithmetic ("Bluray-1080p · 6,302 / 10,000 · cutoff Remux-2160p") live
// in the Why? explanation (components/title/upgrade-why.tsx). People without "see files" get availability and
// resolution only, since the API leaves states, scores and releases out for them.
import type { Cutoff, UpgradeInfo, VersionControl } from "@/api/manage";
import { evidenceSourceWords, POSITIVE_ONLY_WORDS } from "./evidence-words";
import { fmtAgo, fmtDay, fmtNumber } from "./format";

export type Tone = "positive" | "warning" | "negative" | "neutral" | "info";

/** An upgrade state from the API (UpgradeInfo.state; a list row's upgrade_state). */
export type UpgradeState = UpgradeInfo["state"];

/**
 * The longer wording of each state, the line under the Why? explanation's heading (badges and the heading say the
 * quality word, upgradeBadge: Upgrade found, Best quality known or Preferred quality met).
 * label is the wording; short is its main clause; why marks the states a Why? explanation backs with source-ceiling evidence.
 */
export const UPGRADE_WORDS: Record<Exclude<UpgradeState, "none">, { label: string; short: string; tone: Tone; why: boolean }> = {
  // The preferred target (the profile's cutoff) is reached.
  cutoff_met: { label: "Preferred quality met", short: "Preferred quality met", tone: "positive", why: false },
  // A known or manually set source ceiling is reached.
  best_available: { label: "Best available", short: "Best available", tone: "positive", why: true },
  // A provisional (likely) ceiling is reached.
  best_source_seen: { label: "Best source seen so far", short: "Best source seen so far", tone: "neutral", why: true },
  // A higher source is known, and no accepted replacement was found.
  higher_source_exists: { label: "No suitable release found yet, though a better source exists", short: "No suitable release found", tone: "info", why: true },
  // An accepted replacement was found.
  upgrade_available: { label: "Upgrade found", short: "Upgrade found", tone: "info", why: false },
  // Below the target, no replacement found.
  below_cutoff: { label: "Preferred quality not met; no better copy found", short: "Preferred quality not met", tone: "warning", why: false },
};

/** The short label of a copy that holds a file when nothing better has been found for its profile. */
export const BEST_KNOWN = "Best quality known";

/** The quality word of a copy at its preferred quality. */
export const PREFERRED_MET = "Preferred quality met";

/**
 * The quality word for an upgrade state (docs/design/ui.md "Status words"), said beside the status (Available, Partly
 * available) and never in its place; null for none or no state. Three words: "Upgrade found" once a better release
 * was actually found, "Preferred quality met" at the preferred quality, and "Best quality known" (never a warning) for
 * every other copy with a file: nothing better has been found for its profile. The longer wording is the line under
 * Why?'s heading (UPGRADE_WORDS).
 */
export function upgradeBadge(u: Pick<UpgradeInfo, "state"> | undefined | null): { label: string; short: string; tone: Tone; why: boolean } | null {
  if (!u?.state || u.state === "none") return null;
  const words = UPGRADE_WORDS[u.state];
  if (!words) return null;
  if (u.state === "upgrade_available") return { ...words, label: "Upgrade found", short: "Upgrade found" };
  if (u.state === "cutoff_met") return { ...words, label: PREFERRED_MET, short: PREFERRED_MET };
  return { ...words, label: BEST_KNOWN, short: BEST_KNOWN, tone: words.tone === "positive" ? "positive" : "neutral" };
}

/** "Bluray-1080p · profile score 6,302 / 10,000 · cutoff Remux-2160p"; the score part only when known. */
export function cutoffLine(quality: string | undefined, score: number | undefined | null, cutoff: Cutoff | undefined | null): string {
  const parts: string[] = [];
  if (quality) parts.push(quality);
  if (score !== undefined && score !== null) {
    parts.push(cutoff?.score !== undefined && cutoff.score !== null ? `profile score ${fmtNumber(score)} / ${fmtNumber(cutoff.score)}` : `profile score ${fmtNumber(score)}`);
  } else if (cutoff?.score !== undefined && cutoff.score !== null && cutoff.score > 0) {
    parts.push(`cutoff score ${fmtNumber(cutoff.score)}`);
  }
  if (cutoff?.quality) parts.push(`cutoff ${cutoff.quality}`);
  if (cutoff && !cutoff.upgrades) parts.push("upgrades off");
  return parts.join(" · ");
}

const SOURCE: Record<string, string> = {
  search: "Theatarr's search",
  rss: "Theatarr's RSS",
};

/** Why the item misses its preferred quality, in words: which part falls short ("" when neither does). */
export function shortfall(quality: string | undefined, score: number | undefined | null, cutoff: Cutoff | undefined | null): string {
  if (!cutoff) return "";
  const parts: string[] = [];
  if (cutoff.quality && quality && quality !== cutoff.quality) parts.push(`the quality is not ${cutoff.quality} yet`);
  if (score !== undefined && score !== null && cutoff.score !== undefined && cutoff.score !== null && score < cutoff.score) {
    parts.push(`the profile score is ${fmtNumber(cutoff.score - score)} short`);
  }
  return parts.join(" and ");
}

/** What Theatarr would take next and why, or why there is nothing to take. */
export function nextLine(u: UpgradeInfo | undefined | null, now: Date = new Date()): string {
  if (!u || (u.state !== "below_cutoff" && u.state !== "upgrade_available")) return "";
  const n = u.next;
  if (n) {
    const q = [n.quality, fmtNumber(n.score)].filter(Boolean).join(" · ");
    const why = n.reason ? `: ${n.reason}` : "";
    return `Would take ${n.release} (${q})${why}. Seen ${fmtAgo(n.seen_at, now)} by ${SOURCE[n.source] ?? n.source}.`;
  }
  if (u.seen && u.seen > 0) return `A better release was seen for ${fmtNumber(u.seen)} of ${fmtNumber(u.below)} episodes.`;
  const last = u.last_search ? `; last search ${fmtAgo(u.last_search, now)}` : "";
  return `No better release seen yet${last}.`;
}

/** For people without "see files": one sentence with no release names or scores. */
export function plainUpgrade(u: UpgradeInfo | undefined | null): string {
  switch (u?.state) {
    case "below_cutoff":
      return u.cutoff?.quality ? `Preferred quality (${sourceWords(u.cutoff.quality)}) not met; no better copy found.` : "Preferred quality not met; no better copy found.";
    case "upgrade_available":
      return "Upgrade found.";
    case "cutoff_met":
      return "Preferred quality met.";
  }
  return "";
}

/** Episodes under the preferred quality in a series version: "23 episodes under preferred quality · 1 upgrade found". */
export function belowCount(u: UpgradeInfo | undefined | null): string {
  if (!u?.below) return "";
  const seen = u.seen ? ` · ${fmtNumber(u.seen)} upgrade${u.seen === 1 ? "" : "s"} found` : "";
  return `${fmtNumber(u.below)} episode${u.below === 1 ? "" : "s"} under preferred quality${seen}`;
}

/** Why a control is off, or "" when it is on. */
export function editNote(c: VersionControl | undefined | null): string {
  if (!c) return "";
  return c.can_edit ? "" : c.edit_reason ?? "";
}

export function searchNote(c: VersionControl | undefined | null): string {
  if (!c) return "";
  return c.can_search ? "" : c.search_reason ?? "";
}

/** "4K", "1080p": a resolution in words, from a quality or the bare resolution the API gives accounts without "see files". */
export function resolutionWords(quality: string | undefined): string {
  const resolution = quality?.match(/(2160|1080|720|576|480)p?/i)?.[1];
  if (!resolution) return "";
  return resolution === "2160" ? "4K" : `${resolution}p`;
}

/** Availability wording for accounts without file access: "Available in 4K", "Available in 1080p", "Available". */
export function availableIn(quality: string | undefined): string {
  const r = resolutionWords(quality);
  return r ? `Available in ${r}` : "Available";
}

/**
 * A quality in words, resolution and source: "4K WEB" (WEBDL-2160p), "4K Remux" (Remux-2160p, Bluray-2160p Remux),
 * "1080p Blu-ray", "720p TV", "DVD". The quality as given when neither part is recognised. For managers; accounts
 * without "see files" get resolutionWords only.
 */
export function sourceWords(quality: string | undefined | null): string {
  if (!quality) return "";
  const res = resolutionWords(quality);
  const q = quality.toLowerCase();
  const src = /remux/.test(q)
    ? "Remux"
    : /blu-?ray|\bbd\b|uhd/.test(q)
      ? "Blu-ray"
      : /web/.test(q)
        ? "WEB"
        : /dvd/.test(q)
          ? "DVD"
          : /hdtv|sdtv|\btv\b|raw-hd/.test(q)
            ? "TV"
            : "";
  if (!res && !src) return quality;
  if (src === "DVD" && !res) return "DVD";
  return [res, src].filter(Boolean).join(" ");
}

const TIER_WORDS: Record<string, string> = {
  tv: "TV",
  dvd: "DVD",
  web_1080: "1080p WEB",
  bd: "1080p Blu-ray",
  web_2160: "4K WEB",
  uhd_bd: "4K Blu-ray",
};

/** A source-ceiling tier in words ("4K Blu-ray"); the tier key when unknown. */
export function tierWords(tier: string | undefined | null): string {
  return tier ? TIER_WORDS[tier] ?? tier : "";
}

/** What the Why? explanation says, as lines (lib so it is tested; upgrade-why.tsx lays it out). */
export type Why = {
  /** The quality word, as the badge that opened Why? says it (upgradeBadge). */
  headline: string;
  /** The state's longer wording (UPGRADE_WORDS), when it says more than the headline. */
  detail?: string;
  current: string;
  preferred: string;
  /** With a source ceiling: the best the profile can reach under it. */
  reachable?: string;
  evidence?: string;
  checked?: string;
  coverage: string[];
  /** Each sighting in words, with the raw source for its tooltip. */
  sightings: { text: string; source: string }[];
  override?: string;
};

/**
 * The Why? explanation of an upgrade state: current against preferred source, the ceiling's evidence kind and date,
 * search coverage, sightings and any manual override (who, when, note). by names the override's author (a user id
 * when the caller cannot look names up).
 */
export function whyOf(u: UpgradeInfo, now: Date = new Date(), by?: (id: number) => string): Why {
  const w = u.state === "none" ? undefined : UPGRADE_WORDS[u.state];
  const b = upgradeBadge(u);
  const c = u.ceiling;
  const out: Why = {
    headline: b?.label ?? "",
    detail: w && w.label !== b?.label ? w.label : undefined,
    current: sourceWords(u.quality) || "No file",
    preferred: sourceWords(u.cutoff?.quality) || "Not set",
    coverage: [],
    sightings: [],
  };
  if (!c) return out;
  if (u.effective_cutoff) out.reachable = sourceWords(u.effective_cutoff);
  const label = tierWords(c.tier) || c.label;
  switch (c.status) {
    case "known":
      out.evidence = `Known: searches over at least 90 days found nothing above ${label}.`;
      break;
    case "likely":
      out.evidence = `Provisional: releases up to ${label} have been seen; that nothing better exists is not established yet.`;
      break;
    case "override":
      out.evidence = `Set by hand to ${label}.`;
      break;
    default:
      out.evidence = "No source evidence yet.";
  }
  out.checked = `Checked ${fmtAgo(c.checked_at, now)}; next check ${fmtDay(c.next_check_at, now)}.`;
  for (const s of c.searches ?? []) {
    const ix = s.indexers.map((x) => x.indexer + (x.complete ? "" : " (partial or cached)")).join(", ");
    out.coverage.push(`${fmtDay(s.searched_at, now)}: ${ix || "no successful indexer answers"}`);
  }
  for (const e of c.evidence ?? []) {
    out.sightings.push({
      text: `${tierWords(e.tier)} · ${evidenceSourceWords(e.source)}${e.example ? `: ${e.example}` : ""} · last seen ${fmtDay(e.last_seen_at, now)}${e.positive_only ? ` · ${POSITIVE_ONLY_WORDS}` : ""}`,
      source: e.source,
    });
  }
  const o = c.override;
  if (o) {
    const who = o.by !== undefined && o.by !== null ? (by?.(o.by) ?? `user #${o.by}`) : "someone";
    const until = o.expires_at ? `; until ${fmtDay(o.expires_at, now)}` : "";
    out.override = `Set to ${tierWords(o.tier)} by ${who} on ${fmtDay(o.at, now)}${until}${o.note ? `: “${o.note}”` : ""}.`;
  }
  return out;
}
