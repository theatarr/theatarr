// One library's status at a glance (docs/design/ui.md "Library views"): the one colour rule for the posters' bars and
// dots and the index table's pills, and the words their tooltips and accessible names give. "Not released yet" is the
// badge's own rule (upcomingRelease), so a bar or pill never reads Missing while the badge reads Upcoming.
import type { Schemas } from "@/api/client";
import type { TitleRow } from "@/api/library";
import { chipWords, copyBar } from "@/lib/copies";
import { upcomingRelease } from "@/lib/library";

type VersionBrief = Schemas["VersionBrief"];

/** ok green, bad red, progress accent (striped), warn amber, idle grey. */
export type CopyTone = "ok" | "bad" | "progress" | "warn" | "idle";

/** The facts of the title the rule reads: its kind and, for a movie, its release days. */
export type ToneTitle = Pick<TitleRow, "kind" | "release_date" | "home_release">;

/**
 * A library's tone. A blocked download in hand is warn. A movie: a file is ok; else a download in hand is progress;
 * else not monitored or not released yet is idle; else bad. A series: nothing aired yet is idle; a monitored, aired
 * episode missing is bad; else episodes held is ok; else episodes in hand is progress; else idle.
 */
export function copyTone(v: VersionBrief, t: ToneTitle, day?: string): CopyTone {
  if (v.acquisition?.stage === "blocked") return "warn";
  if (t.kind === "series") {
    const c = v.coverage;
    if (!c || c.total === 0) return "idle";
    if ((c.missing ?? 0) > 0) return "bad";
    if (c.have > 0) return "ok";
    return (c.in_flight ?? 0) > 0 ? "progress" : "idle";
  }
  if (v.has_file) return "ok";
  if (v.acquisition) return "progress";
  if (v.monitored === false || upcomingRelease(t, day)) return "idle";
  return "bad";
}

const WORST: CopyTone[] = ["bad", "warn", "progress", "ok", "idle"];

/** The whole title's tone: the worst of its libraries' tones (bad, warn, progress, ok, idle). */
export function worstTone(tones: readonly CopyTone[]): CopyTone {
  return WORST.find((w) => tones.includes(w)) ?? "idle";
}

/** A library's status in words, without its label: "13 of 17 episodes linked, 2 in progress", "missing", "not monitored". */
export function copyWords(v: VersionBrief, t: ToneTitle, day?: string): string {
  const blocked = v.acquisition?.stage === "blocked";
  if (t.kind === "series") {
    if (!v.coverage || v.coverage.total === 0) return ["nothing aired yet", blocked ? "download blocked" : "", v.monitored === false ? "not monitored" : ""].filter(Boolean).join(", ");
    return [copyBar(v, "series").label, blocked ? "download blocked" : "", v.monitored === false ? "not monitored" : ""].filter(Boolean).join(", ");
  }
  if (blocked) return v.has_file ? "has a file, upgrade blocked" : "download blocked";
  if (v.has_file) return "has a file";
  if (v.acquisition) return chipWords(v.acquisition).toLowerCase();
  if (v.monitored === false) return "not monitored";
  if (upcomingRelease(t, day)) return "not released yet";
  return "missing";
}
