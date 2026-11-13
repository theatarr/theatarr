// One library's status at a glance (docs/design/ui.md "Library views"): the one colour rule for the posters' bars and
// dots and the index table's pills, and the words their tooltips and accessible names give. "Not released yet" is the
// badge's own rule (upcomingRelease), so a bar or pill never reads Missing while the badge reads Upcoming.
import type { Schemas } from "@/api/client";
import type { TitleRow } from "@/api/library";
import { chipWords, copyBar } from "@/lib/copies";
import { upcomingRelease, type PosterStatus } from "@/lib/library";

type VersionBrief = Schemas["VersionBrief"];

/** ok green, bad red, progress accent (striped), warn amber, idle grey. */
export type CopyTone = "ok" | "bad" | "progress" | "warn" | "idle";

/** The facts of the title the rule reads: its kind and, for a movie, its release days. */
export type ToneTitle = Pick<TitleRow, "kind" | "release_date" | "home_release">;

/** A blocked download in hand for what the library lacks; a blocked upgrade of a file it holds is not. */
function blockedMissing(v: VersionBrief, t: ToneTitle): boolean {
  const a = v.acquisition;
  return a?.stage === "blocked" && !(t.kind === "series" ? a.upgrade : v.has_file);
}

/**
 * A library's tone. A blocked download for what it lacks is warn (a blocked upgrade of a held file is not). A movie: a
 * file is ok; else a download in hand is progress; else not monitored or not released yet is idle; else bad. A series:
 * nothing aired yet is idle; a monitored, aired episode missing in a monitored library is bad; else episodes held is
 * ok; else episodes in hand is progress; else idle.
 */
export function copyTone(v: VersionBrief, t: ToneTitle, day?: string): CopyTone {
  if (blockedMissing(v, t)) return "warn";
  if (t.kind === "series") {
    const c = v.coverage;
    if (!c || c.total === 0) return "idle";
    if (v.monitored !== false && (c.missing ?? 0) > 0) return "bad";
    if (c.have > 0) return "ok";
    return (c.in_flight ?? 0) > 0 ? "progress" : "idle";
  }
  if (v.has_file) return "ok";
  if (v.acquisition) return "progress";
  if (v.monitored === false || upcomingRelease(t, day)) return "idle";
  return "bad";
}

/** The tone of the badge's own status, for a whole-title bar that must agree with the badge. */
export function badgeTone(s: PosterStatus | undefined): CopyTone {
  switch (s) {
    case "available":
    case "best_known":
    case "upgrade":
    case "some_episodes":
      return "ok";
    case "wanted":
      return "bad";
    case "getting_ready":
      return "progress";
  }
  return "idle";
}

/** A library's status in words, without its label: "13 of 17 episodes linked, 2 in progress", "missing", "not monitored". */
export function copyWords(v: VersionBrief, t: ToneTitle, day?: string): string {
  const blocked = blockedMissing(v, t);
  const upgradeBlocked = v.acquisition?.stage === "blocked" && !blocked;
  const off = v.monitored === false ? "not monitored" : "";
  if (t.kind === "series") {
    if (!v.coverage || v.coverage.total === 0) return ["nothing aired yet", blocked ? "download blocked" : "", off].filter(Boolean).join(", ");
    const bar = copyBar(v, "series");
    const linked = `${bar.have} of ${bar.total} episode${bar.total === 1 ? "" : "s"} linked`;
    const flight = !bar.flight ? "" : blocked ? `${bar.flight} with a blocked download` : `${bar.flight} in progress`;
    return [linked, flight, blocked && !bar.flight ? "download blocked" : "", upgradeBlocked ? "upgrade download blocked" : "", off].filter(Boolean).join(", ");
  }
  if (blocked) return "download blocked";
  if (v.has_file) return upgradeBlocked ? "has a file, upgrade download blocked" : "has a file";
  if (v.acquisition) return chipWords(v.acquisition).toLowerCase();
  if (v.monitored === false) return "not monitored";
  if (upcomingRelease(t, day)) return "not released yet";
  return "missing";
}
