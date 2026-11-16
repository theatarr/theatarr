// One library's status at a glance (docs/design/ui.md "Library views"): the one colour rule for the posters' bars and
// dots and the index table's pills, and the words their tooltips and accessible names give. "Not released yet" is the
// badge's own rule (upcomingRelease), so a bar or pill never reads Missing while the badge reads Upcoming.
import type { Schemas } from "@/api/client";
import type { TitleRow } from "@/api/library";
import { chipWords, copyBar } from "@/lib/copies";
import { fmtNumber } from "@/lib/format";
import { notAiredYet, undatedUpcoming, upcomingRelease, type PosterStatus } from "@/lib/library";
import { unmonitoredCoverage } from "@/lib/title-facts";

type VersionBrief = Schemas["VersionBrief"];

/** ok green, bad red, progress accent (striped), warn amber, idle grey. */
export type CopyTone = "ok" | "bad" | "progress" | "warn" | "idle";

/** The facts of the title the rule reads: its kind and, for a movie, its release days. */
export type ToneTitle = Pick<TitleRow, "kind" | "release_date" | "home_release"> & Partial<Pick<TitleRow, "status" | "in_cinemas" | "digital_release" | "physical_release">>;

/** A download in hand for the library is blocked. A row summarises all of a library's downloads (the most advanced
 *  stage, blocked first), so for a series it does not say which episodes the blocked one covers: no counts are given
 *  then, and nothing is drawn as in progress. */
export function hasBlocked(v: VersionBrief): boolean {
  return v.acquisition?.stage === "blocked";
}

/**
 * A library's tone. A movie: a file is ok (a blocked upgrade too); else a blocked download is warn; else a download in
 * hand is progress; else not monitored or not released yet is idle; else bad. A series: nothing aired yet is idle; a
 * monitored, aired episode missing in a monitored library is bad, or warn when a download is blocked; else episodes
 * held is ok; else episodes in hand (none blocked) is progress; else idle.
 */
export function copyTone(v: VersionBrief, t: ToneTitle, day?: string): CopyTone {
  const blocked = hasBlocked(v);
  if (t.kind === "series") {
    const c = v.coverage;
    if (!c || c.total === 0) return "idle";
    if (v.monitored !== false && (c.missing ?? 0) > 0) return blocked ? "warn" : "bad";
    if (c.have > 0) return "ok";
    return !blocked && (c.in_flight ?? 0) > 0 ? "progress" : "idle";
  }
  if (v.has_file) return "ok";
  if (blocked) return "warn";
  if (v.acquisition) return "progress";
  if (v.monitored === false || upcomingRelease(t, day) || undatedUpcoming(t)) return "idle";
  return "bad";
}

/** A tone as a badge's (Badge tone): the title page's status words and the libraries window use the same colours. */
export const TONE: Record<CopyTone, "positive" | "negative" | "info" | "warning" | "neutral"> = {
  ok: "positive", bad: "negative", progress: "info", warn: "warning", idle: "neutral",
};

/** The tone of the badge's own status (its dot's colour), for a whole-title bar that must agree with the badge. */
export function badgeTone(s: PosterStatus | undefined): CopyTone {
  switch (s) {
    case "available":
      return "ok";
    case "partly_available":
      return "warn";
    case "wanted":
      return "bad";
    case "some_episodes":
    case "getting_ready":
      return "progress";
  }
  return "idle";
}

/** A library's status in words, without its label: "13 of 17 episodes linked, 2 in progress", "missing", "not monitored". */
export function copyWords(v: VersionBrief, t: ToneTitle, day?: string): string {
  const blocked = hasBlocked(v);
  const off = v.monitored === false ? "not monitored" : "";
  if (t.kind === "series") {
    // Empty coverage is "nothing aired yet" only when the badge reads Upcoming (notAiredYet, the same evidence);
    // otherwise the episodes are not known yet.
    if (!v.coverage || v.coverage.total === 0) {
      const none = notAiredYet({ ...t, versions: [v] }, day) ? "nothing aired yet" : "no episode information yet";
      return [none, blocked ? "a download is blocked" : "", off].filter(Boolean).join(", ");
    }
    const bar = copyBar(v, "series");
    const linked = `${bar.have} of ${bar.total} episode${bar.total === 1 ? "" : "s"} linked`;
    const going = blocked ? "a download is blocked" : bar.flight ? `${bar.flight} in progress` : "";
    return [linked, going, off].filter(Boolean).join(", ");
  }
  if (v.has_file) return blocked ? "has a file, upgrade download blocked" : "has a file";
  if (blocked) return "download blocked";
  if (v.acquisition) return chipWords(v.acquisition).toLowerCase();
  if (v.monitored === false) return "not monitored";
  if (upcomingRelease(t, day) || undatedUpcoming(t)) return "not released yet";
  return "missing";
}

/**
 * A monitored series library that holds every monitored episode but not every episode, so "Available" beside "6/22"
 * does not read as a contradiction: "6 of 6 monitored · 22 episodes", or, while monitored episodes are still to air
 * (the monitored count then includes them), "6 of 22 episodes · 16 not monitored"; a library monitoring none
 * "6 of 22 episodes · none monitored". "" when it holds them all or misses a monitored one. For callers who see files
 * (the monitored count is theirs).
 */
export function monitoredHeldWords(v: VersionBrief): string {
  const c = v.coverage;
  if (!c || v.monitored === false || (c.missing ?? 0) > 0 || c.have >= c.total) return "";
  const monitored = c.monitored ?? 0;
  const none = unmonitoredCoverage({ ...c, monitored }, true);
  if (none) return none;
  if (!c.upcoming) return `${fmtNumber(monitored)} of ${fmtNumber(monitored)} monitored · ${fmtNumber(c.total)} episodes`;
  return `${fmtNumber(c.have)} of ${fmtNumber(c.total)} episodes · ${fmtNumber(c.total - c.have)} not monitored`;
}
