import { describe, expect, it } from "vitest";
import { copiesUnfinished, copyReasons, otherProvidersLine, queuedWords, type ProviderCopy } from "./provider-copies";
import { fmtTime } from "./format";

const now = new Date("2026-11-13T14:00:00Z");
const c = (provider: string, state: ProviderCopy["state"], at = "2026-11-13T12:00:00Z", message = ""): ProviderCopy => ({ provider, state, updated_at: at, message });

describe("provider copies", () => {
  it("lists only what Cached on does not show, with the newest time", () => {
    expect(otherProvidersLine([c("tb", "added")], now)).toBe("");
    expect(otherProvidersLine([c("tb", "added"), c("rd", "not_cached")], now)).toBe("RD not cached, 2 h ago");
    expect(otherProvidersLine([c("ad", "adding", "2026-11-13T13:59:30Z"), c("rd", "refused")], now)).toMatch(/^AD adding… · RD refused, /);
  });
  it("polls while a copy is unfinished", () => {
    expect(copiesUnfinished([c("rd", "checking")])).toBe(true);
    expect(copiesUnfinished([c("rd", "failed"), c("tb", "added")])).toBe(false);
    expect(copiesUnfinished(undefined)).toBe(false);
  });
  it("says what the action queued", () => {
    expect(queuedWords([])).toBe("Every provider already holds it.");
    expect(queuedWords([c("ad", "queued"), c("tb", "queued")])).toBe("Adding to AD, TB. The result shows here when it ends.");
  });
  it("shows a hold's end in the browser's zone, from the time the server sends", () => {
    const held = { ...c("rd", "skipped", "2026-11-13T12:00:00Z", "Skipped: Real-Debrid is holding adds"), until: "2026-11-13T14:05:00Z" };
    expect(copyReasons([held])).toBe(`RD: Skipped: Real-Debrid is holding adds until ${fmtTime("2026-11-13T14:05:00Z")}`);
  });
});
