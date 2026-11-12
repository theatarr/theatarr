// The top bar's status dot (components/shell/status-dot.tsx), from the live event stream: "VPN up" only on a current
// positive answer, that is vpn_ok true in a gateway snapshot received on the open connection (the server's vpn_ok is
// already false when an egress check is stale). A dropped stream says so instead of repeating its last snapshot, and
// a snapshot from an earlier connection counts as stale until the new one arrives. For its first 15 seconds a drop
// reads "Reconnecting" (a settings save drops the stream briefly), then "Disconnected".
import type { Live } from "@/api/events";
import { attentionCount } from "./jobs";

export type LiveTone = "ok" | "warn" | "bad" | "idle" | "stale";
export type LiveSummary = { tone: LiveTone; label: string; detail: string };

/** How long a dropped stream reads "Reconnecting" before "Disconnected": a settings save drops it for a few seconds. */
export const RECONNECT_GRACE = 15_000;

export function liveSummary(live: Live, now = Date.now()): LiveSummary {
  const g = live.gateway;
  if (!live.connected) {
    if (!g && !live.lostAt) return { tone: "idle", label: "Connecting", detail: "Opening the live event stream." };
    if (live.lostAt && now - live.lostAt < RECONNECT_GRACE)
      return { tone: "idle", label: "Reconnecting", detail: "The live event stream dropped and is reconnecting." };
    return {
      tone: "stale",
      label: "Disconnected",
      detail: "The live event stream is down, so VPN and job state are unknown. It reconnects on its own.",
    };
  }
  const current = g !== undefined && live.gatewayAt !== undefined && (live.connectedAt === undefined || live.gatewayAt >= live.connectedAt);
  if (!current) return { tone: "idle", label: "Checking", detail: "Waiting for the first egress snapshot." };
  const vpn = g.egress?.vpn_ok;
  const mode = g.egress?.mode;
  const optional = mode === "off" || mode === "monitor-only";
  if (vpn === false && !optional) return { tone: "bad", label: "VPN down", detail: "An egress check failed or is stale." };
  const jobsCurrent = live.jobsAt !== undefined && (live.connectedAt === undefined || live.jobsAt >= live.connectedAt);
  const failed = jobsCurrent ? attentionCount(live.jobCounts) : 0;
  const parts = [mode === "off" ? "VPN guard off" : mode === "monitor-only" ? "VPN monitor-only" : vpn === true ? "VPN up" : "VPN unknown"];
  if (failed > 0) parts.push(`${failed} failed job${failed === 1 ? "" : "s"}`);
  const tone: LiveTone = optional || vpn !== true || failed > 0 ? "warn" : "ok";
  return {
    tone,
    label: parts.join(" · "),
    detail: optional ? "Requests do not require a VPN check. Live." : vpn === true ? "Every egress path has a fresh good check. Live." : "The gateway reported no VPN state. Live.",
  };
}
