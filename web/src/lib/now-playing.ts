// Now playing helpers (docs/design/now-playing.md): labels and tones for a playback, its health and its source.
import type { NowPlayingSession } from "@/api/nowplaying";

type Tone = "positive" | "warning" | "negative" | "info" | "neutral";

/** 1:02:03 or 12:34 from milliseconds. */
export function fmtClock(ms: number | undefined): string {
  const s = Math.max(0, Math.floor((ms ?? 0) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${sec}` : `${m}:${sec}`;
}

/** Position as a share of the duration, 0..1. */
export function progress(s: Pick<NowPlayingSession, "view_offset_ms" | "duration_ms">): number {
  if (!s.duration_ms) return 0;
  return Math.min(1, Math.max(0, s.view_offset_ms / s.duration_ms));
}

/** What plays: "Show · S01E02 · Title" for an episode, the title otherwise. */
export function itemLabel(s: Pick<NowPlayingSession, "type" | "title" | "show" | "season" | "episode">): string {
  if (s.type === "episode" && s.show) {
    const se = `S${String(s.season ?? 0).padStart(2, "0")}E${String(s.episode ?? 0).padStart(2, "0")}`;
    return [s.show, se, s.title].filter(Boolean).join(" · ");
  }
  return s.title;
}

const VERDICT: Record<string, { label: string; tone: Tone }> = {
  ok: { label: "Playing smoothly", tone: "positive" },
  paused: { label: "Paused", tone: "neutral" },
  buffering: { label: "Buffering", tone: "negative" },
  slow: { label: "Falling behind", tone: "warning" },
};

const CAUSE: Record<string, string> = {
  transcode: "Plex's transcoder",
  provider: "the debrid provider",
  mount: "the mount",
  network: "the network or player",
};

/** The health badge: label and tone, with the likeliest cause for a struggling playback. */
export function healthBadge(h: NowPlayingSession["health"]): { label: string; tone: Tone } {
  const v = VERDICT[h.verdict] ?? { label: h.verdict, tone: "neutral" as Tone };
  if (h.cause && (h.verdict === "buffering" || h.verdict === "slow")) {
    if (h.cause === "unknown") return { label: `${v.label}: cause unknown`, tone: v.tone };
    return { label: `${v.label}: likely ${CAUSE[h.cause] ?? h.cause}`, tone: v.tone };
  }
  return v;
}

/** How Plex streams it: direct play, direct stream (copy) or a transcode with its speed. */
export function streamLabel(s: Pick<NowPlayingSession, "decision" | "transcode">): string {
  const t = s.transcode;
  if (s.decision === "transcode" || (t && t.videoDecision === "transcode")) {
    return t?.speed ? `Transcode ${t.speed.toFixed(1)}x${t.throttled ? " (ahead)" : ""}` : "Transcode";
  }
  if (s.decision === "copy" || (t && t.videoDecision === "copy")) return "Direct stream";
  return "Direct play";
}

/** Where the player is. */
export function placeLabel(s: Pick<NowPlayingSession, "local" | "relayed" | "location">): string {
  if (s.relayed) return "Remote, relayed";
  if (s.local || s.location === "lan") return "Local";
  return "Remote";
}

/** Bytes per second as Mbit/s. */
export function fmtMbps(bytesPerSec: number | undefined): string {
  if (!bytesPerSec) return "0 Mbit/s";
  return `${((bytesPerSec * 8) / 1_000_000).toFixed(1)} Mbit/s`;
}

/** The pre-warm line: state and episode. */
export function prewarmText(p: NowPlayingSession["prewarm"]): { text: string; tone: Tone } | null {
  if (!p) return null;
  const ep = p.episode ? `${p.episode} ` : "";
  switch (p.state) {
    case "ok":
      return { text: `${ep}${p.detail || "warmed"}`, tone: "positive" };
    case "running":
      return { text: `${ep}warming…`, tone: "info" };
    case "caching":
      return { text: `${ep}${p.detail ?? "caching…"}`, tone: "info" };
    case "cached":
      return { text: `${ep}cached`, tone: "positive" };
    case "failed":
      return { text: `${ep}${p.detail ?? "failed"}`, tone: "negative" };
    default:
      return { text: p.detail ? `${ep}${p.detail}`.trim() : "skipped", tone: "neutral" };
  }
}
