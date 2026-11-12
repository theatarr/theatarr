// The live status signal in the top bar (admins): VPN egress and failed jobs that need attention (not resolved, retried
// or dismissed), from the event stream (lib/live-status.ts), as a dot and its words ("VPN down"). A dropped stream
// shows as Reconnecting for its first seconds, then Disconnected, never as the last snapshot. Links to System › Status.
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useLive } from "@/api/events";
import { liveSummary, RECONNECT_GRACE, type LiveTone } from "@/lib/live-status";
import { cn } from "@/lib/utils";

const DOT: Record<LiveTone, string> = {
  ok: "bg-[var(--ok)]",
  warn: "bg-[var(--warn)]",
  bad: "bg-[var(--bad)]",
  idle: "bg-[var(--ink-4)]",
  stale: "border-[1.5px] border-[var(--ink-3)] bg-transparent",
};

export function StatusDot() {
  const live = useLive();
  const s = liveSummary(live);
  // Re-render when the grace ends, so a stream that stays down turns into Disconnected.
  const [, tick] = useState(0);
  const reconnecting = s.label === "Reconnecting";
  useEffect(() => {
    if (!reconnecting || !live.lostAt) return;
    const t = window.setTimeout(() => tick((n) => n + 1), Math.max(0, live.lostAt + RECONNECT_GRACE - Date.now()));
    return () => window.clearTimeout(t);
  }, [reconnecting, live.lostAt]);
  return (
    <Link
      to="/admin"
      data-testid="status-dot"
      data-tone={s.tone}
      title={`${s.label}: ${s.detail}`}
      aria-label={`Status: ${s.label}. ${s.detail}`}
      className={cn(
        "flex h-8 shrink-0 items-center gap-1.5 rounded-[5px] px-2 text-[12.5px] text-[var(--ink-2)] transition-colors hover:bg-[var(--hover)] hover:text-[var(--ink)]",
        s.tone === "bad" && "font-medium text-[var(--bad-ink)]",
      )}
    >
      <span aria-hidden className={cn("size-2 shrink-0 rounded-full", DOT[s.tone])} />
      <span className={cn("whitespace-nowrap", s.tone === "bad" ? "inline" : "hidden lg:inline")}>{s.label}</span>
    </Link>
  );
}
