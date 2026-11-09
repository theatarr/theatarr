import { describe, expect, it } from "vitest";
import { fmtClock, fmtMbps, healthBadge, itemLabel, placeLabel, prewarmText, progress, streamLabel } from "./now-playing";

describe("now playing", () => {
  it("clock and progress", () => {
    expect(fmtClock(61_000)).toBe("1:01");
    expect(fmtClock(3_723_000)).toBe("1:02:03");
    expect(fmtClock(undefined)).toBe("0:00");
    expect(progress({ view_offset_ms: 30, duration_ms: 120 })).toBe(0.25);
    expect(progress({ view_offset_ms: 30, duration_ms: 0 })).toBe(0);
  });
  it("labels the item", () => {
    expect(itemLabel({ type: "episode", title: "Pilot", show: "Show", season: 1, episode: 2 })).toBe("Show · S01E02 · Pilot");
    expect(itemLabel({ type: "movie", title: "Alpha" })).toBe("Alpha");
  });
  it("health with its cause", () => {
    expect(healthBadge({ verdict: "buffering", cause: "provider" })).toEqual({ label: "Buffering: likely the debrid provider", tone: "negative" });
    expect(healthBadge({ verdict: "ok" }).tone).toBe("positive");
    expect(healthBadge({ verdict: "paused", cause: "network" }).label).toBe("Paused");
  });
  it("stream and place", () => {
    expect(streamLabel({ decision: "transcode", transcode: { speed: 0.7, throttled: false } })).toBe("Transcode 0.7x");
    expect(streamLabel({ decision: "copy" })).toBe("Direct stream");
    expect(streamLabel({})).toBe("Direct play");
    expect(placeLabel({ local: false, relayed: true })).toBe("Remote, relayed");
    expect(placeLabel({ local: true, relayed: false })).toBe("Local");
    expect(fmtMbps(1_250_000)).toBe("10.0 Mbit/s");
  });
  it("pre-warm", () => {
    expect(prewarmText(undefined)).toBeNull();
    expect(prewarmText({ episode: "S01E03", state: "ok", at: "" })).toEqual({ text: "S01E03 warmed", tone: "positive" });
    expect(prewarmText({ episode: "S01E03", state: "failed", detail: "is unreadable", at: "" })?.tone).toBe("negative");
    expect(prewarmText({ episode: "S01E03", state: "caching", detail: "caching 42%", progress: 0.42, at: "" })).toEqual({
      text: "S01E03 caching 42%",
      tone: "info",
    });
    expect(prewarmText({ episode: "S01E03", state: "cached", at: "" })).toEqual({ text: "S01E03 cached", tone: "positive" });
  });
  it("shows why only the head was read", () => {
    const detail = "head read; not cached: 21.0 GB is over prewarm_max_size";
    expect(prewarmText({ episode: "S01E03", state: "ok", detail, at: "" })?.text).toBe(`S01E03 ${detail}`);
  });
  it("does not guess a cause when diagnostic evidence is missing", () => {
    expect(healthBadge({ verdict: "buffering", cause: "unknown" }).label).toBe("Buffering: cause unknown");
  });
});
