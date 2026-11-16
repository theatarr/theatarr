// Upgrade words (docs/design/manage.md, Words): one vocabulary for the grid, the title page, season headers and episodes,
// for managers. The API's states map onto it (UPGRADE_WORDS); "Below cutoff" is never shown: it stays an internal
// state and filter key. Scores and the cutoff ("You have: …", "Upgrades stop at: …") live in the Why? explanation
// (components/title/upgrade-why.tsx, whyLines). People without "see files" get availability and resolution only, since
// the API leaves states, scores and releases out for them.
import type { Schemas } from "@/api/client";
import type { Cutoff, UpgradeInfo, VersionControl } from "@/api/manage";
import { fmtAgo, fmtNumber } from "./format";
import { dayWords, failureWords } from "./title-facts";

type HistoryEvent = Schemas["HistoryEvent"];

export type Tone = "positive" | "warning" | "negative" | "neutral" | "info";

/** An upgrade state from the API (UpgradeInfo.state; a list row's upgrade_state). */
export type UpgradeState = UpgradeInfo["state"];

/**
 * The longer wording of each state, the line under the Why? explanation's heading (badges and the heading say the
 * quality word, upgradeBadge: Upgrade found, Preferred quality not met or Preferred quality met).
 * label is the wording; short is its main clause.
 */
export const UPGRADE_WORDS: Record<Exclude<UpgradeState, "none">, { label: string; short: string; tone: Tone }> = {
  // The preferred target (the profile's cutoff) is reached.
  cutoff_met: { label: "Preferred quality met", short: "Preferred quality met", tone: "positive" },
  // An accepted replacement was found.
  upgrade_available: { label: "Upgrade found", short: "Upgrade found", tone: "info" },
  // Below the target, no replacement found.
  below_cutoff: { label: "Preferred quality not met; no better copy found", short: "Preferred quality not met", tone: "warning" },
};

/** The short label of a copy that holds a file below its preferred quality when nothing better has been found. */
export const BELOW_PREFERRED = "Preferred quality not met";

/** The quality word of a copy at its preferred quality. */
export const PREFERRED_MET = "Preferred quality met";

/**
 * The quality word for an upgrade state (docs/design/ui.md "Status words"), said beside the status (Available, Partly
 * available) and never in its place; null for none or no state. Three words: "Upgrade found" once a better release
 * was actually found, "Preferred quality met" at the preferred quality, and "Preferred quality not met" (never a
 * warning) for every other copy with a file: nothing better has been found for its profile. The longer wording is the
 * line under Why?'s heading (UPGRADE_WORDS).
 */
export function upgradeBadge(u: Pick<UpgradeInfo, "state"> | undefined | null): { label: string; short: string; tone: Tone } | null {
  if (!u?.state || u.state === "none") return null;
  const words = UPGRADE_WORDS[u.state];
  if (!words) return null;
  if (u.state === "upgrade_available") return { ...words, label: "Upgrade found", short: "Upgrade found" };
  if (u.state === "cutoff_met") return { ...words, label: PREFERRED_MET, short: PREFERRED_MET };
  return { ...words, label: BELOW_PREFERRED, short: BELOW_PREFERRED, tone: "neutral" };
}

/** Why?'s first line: the file held, "You have: Remux-2160p, score 8,575."; "" without a quality. */
export function haveLine(quality: string | undefined | null, score: number | undefined | null): string {
  if (!quality) return "";
  return score !== undefined && score !== null ? `You have: ${quality}, score ${fmtNumber(score)}.` : `You have: ${quality}.`;
}

/** Why?'s second line: where upgrades stop, in the profile's own names, "Upgrades stop at: Remux-2160p and a score of
 *  10,000."; "Upgrades are off in this library's profile." when it allows none; "" without a cutoff. */
export function stopLine(cutoff: Cutoff | undefined | null): string {
  if (!cutoff) return "";
  if (!cutoff.upgrades) return "Upgrades are off in this library's profile.";
  const score = cutoff.score !== undefined && cutoff.score !== null ? fmtNumber(cutoff.score) : "";
  if (!cutoff.quality) return score ? `Upgrades stop at a score of ${score}.` : "";
  return score ? `Upgrades stop at: ${cutoff.quality} and a score of ${score}.` : `Upgrades stop at: ${cutoff.quality}.`;
}

/**
 * Why?'s verdict, from the server's quality_met (the stored quality-cutoff judging; quality names are never compared
 * here, since a cutoff can be a group): "Below the wanted quality.", or "The quality is reached, the score is not."
 * when the score is known to fall short; "" when the server does not say (a prefer-SeaDex library).
 */
export function verdictLine(u: Pick<UpgradeInfo, "quality_met">, score: number | undefined | null, cutoff: Cutoff | undefined | null): string {
  if (u.quality_met === false) return "Below the wanted quality.";
  const short = score !== undefined && score !== null && cutoff?.score !== undefined && cutoff.score !== null && score < cutoff.score;
  return u.quality_met === true && short ? "The quality is reached, the score is not." : "";
}

/** What came of a release in the library's history on the page: its newest download event (grabbed, imported or
 *  failed), undefined when it was never downloaded. */
export function releaseOutcome(history: readonly HistoryEvent[], release: string): HistoryEvent | undefined {
  let top: HistoryEvent | undefined;
  for (const h of history) {
    if (h.release_title !== release || (h.event !== "grabbed" && h.event !== "failed" && h.event !== "imported" && h.event !== "upgraded")) continue;
    if (!top || h.date > top.date) top = h;
  }
  return top;
}

/**
 * The better release seen and what came of it, or that none was seen: "Better release found 4 d ago:
 * <release> (WEBDL-2160p, score 6,200; yours 4,500). Not downloaded yet."; when the library's history (given) has a
 * failed download of it: "Better release found Nov 12: … Tried Nov 12: not cached on RD, AD and TB. Your file is
 * unchanged." (the failure as the history row says it, failureWords); without the history nothing is said about
 * downloading. "No better release seen yet (last search 2 d ago)." otherwise.
 */
export function nextLine(u: UpgradeInfo | undefined | null, now: Date = new Date(), history?: readonly HistoryEvent[] | null): string {
  if (!u || (u.state !== "below_cutoff" && u.state !== "upgrade_available")) return "";
  const n = u.next;
  if (n) {
    const facts = [n.quality, `score ${fmtNumber(n.score)}`].filter(Boolean).join(", ");
    const yours = u.score !== undefined && u.score !== null ? `; yours ${fmtNumber(u.score)}` : "";
    const what = `${n.release} (${facts}${yours})`;
    const last = history ? releaseOutcome(history, n.release) : undefined;
    if (last?.event === "failed") {
      const why = failureWords(last);
      return `Better release found ${dayWords(n.seen_at, now)}: ${what}. Tried ${dayWords(last.date, now)}${why ? `: ${why}` : ""}. Your file is unchanged.`;
    }
    return `Better release found ${fmtAgo(n.seen_at, now)}: ${what}.${history && !last ? " Not downloaded yet." : ""}`;
  }
  if (u.seen && u.seen > 0) return `A better release was seen for ${fmtNumber(u.seen)} of ${fmtNumber(u.below)} episodes.`;
  const last = u.last_search ? ` (last search ${fmtAgo(u.last_search, now)})` : "";
  return `No better release seen yet${last}.`;
}

/** Why?'s three lines: the file held, where upgrades stop, and the verdict with what was found (nextLine). */
export function whyLines(u: UpgradeInfo, score: number | undefined | null, cutoff: Cutoff | undefined | null, history?: readonly HistoryEvent[] | null, now: Date = new Date()): string[] {
  const third = [verdictLine(u, score, cutoff), nextLine(u, now, history)].filter(Boolean).join(" ");
  return [haveLine(u.quality, score), stopLine(cutoff), third].filter(Boolean);
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
