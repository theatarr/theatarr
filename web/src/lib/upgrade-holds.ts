// Upgrade approval words (docs/design/manage.md "Upgrades waiting"): what a held upgrade would replace, whether it can be
// approved now, and how it was decided. Pure, so the copy card and the Requests tab say the same and vitest pins it.
import type { UpgradeHold } from "@/api/upgrades";
import { fmtNumber } from "./format";
import type { Tone } from "./upgrade";

export const HOLD_HEADLINE = "Upgrade found, waiting for approval";

export const HOLD_STATE: Record<UpgradeHold["state"], { label: string; tone: Tone }> = {
  pending: { label: "Waiting for approval", tone: "warning" },
  approved: { label: "Approved", tone: "positive" },
  declined: { label: "Declined", tone: "negative" },
  expired: { label: "Expired", tone: "neutral" },
  replaced: { label: "Replaced by a better release", tone: "neutral" },
};

function qualityScore(quality: string | undefined, score: number | null | undefined): string {
  const q = quality || "unknown quality";
  return score === undefined || score === null ? q : `${q} · score ${fmtNumber(score)}`;
}

/** "S01E01–S01E10" for a run of episodes, "S01E03" for one, "" for a movie. */
export function episodesText(eps: string[] | undefined): string {
  if (!eps || eps.length === 0) return "";
  return eps.length === 1 ? eps[0] : `${eps[0]}–${eps[eps.length - 1]}`;
}

/** The comparison: what is held against what it would replace. */
export function holdLines(h: UpgradeHold): { held: string; replaces: string } {
  const files = h.current.files > 1 ? `${fmtNumber(h.current.files)} episode files (${qualityScore(h.current.quality, h.current.score)})` : qualityScore(h.current.quality, h.current.score);
  return { held: qualityScore(h.quality, h.score), replaces: files };
}

/** The pending holds of one copy (version key) of a title, newest first as the API sends them. */
export function holdsFor(holds: UpgradeHold[] | undefined, version: string | undefined): UpgradeHold[] {
  if (!holds || !version) return [];
  return holds.filter((h) => h.state === "pending" && h.version === version);
}

/** Whether Approve may be pressed, and why not; a mutation in flight disables both buttons. */
export function approveState(h: UpgradeHold, busy = false): { approve: boolean; decline: boolean; reason?: string } {
  if (h.state !== "pending") return { approve: false, decline: false, reason: HOLD_STATE[h.state]?.label };
  if (busy) return { approve: false, decline: false };
  if (!h.can_approve) return { approve: false, decline: true, reason: h.approve_reason || "Approval would not download anything now" };
  return { approve: true, decline: true };
}

/** "Approved by Sam (session)", "Declined by Sam: too big", "Expired: a newer file arrived"; "" while pending. */
export function decisionLine(h: UpgradeHold): string {
  if (h.state === "pending") return "";
  const who = h.decided_by ? h.decided_by.display_name || h.decided_by.username : "";
  const via = h.decided_via ? ` (${h.decided_via})` : "";
  const note = h.note ? `: ${h.note}` : "";
  switch (h.state) {
    case "approved":
      return `Approved${who ? ` by ${who}` : ""}${via}${h.route_outcome ? ` · ${h.route_outcome}` : ""}`;
    case "declined":
      return `Declined${who ? ` by ${who}` : ""}${via}${note}`;
    default:
      return `${HOLD_STATE[h.state]?.label ?? h.state}${note}`;
  }
}
