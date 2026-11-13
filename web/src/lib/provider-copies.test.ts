import { describe, expect, it } from "vitest";
import { copiesUnfinished, copyReasons, otherProvidersLine, queuedWords, type ProviderCopy } from "./provider-copies";
import { fmtTime } from "./format";

const now = new Date("2026-11-13T14:00:00Z");
const c = (provider: string, state: ProviderCopy["state"], at = "2026-11-13T12:00:00Z", message = ""): ProviderCopy => ({ provider, state, updated_at: at, message });

describe("provider copies", () => {
  it("leaves out only an added copy this surface shows under Cached on", () => {
    expect(otherProvidersLine([c("tb", "added")], ["tb"], now)).toBe("");
    expect(otherProvidersLine([c("tb", "added"), c("rd", "not_cached")], ["tb"], now)).toBe("RD not cached, 2 h ago");
    expect(otherProvidersLine([c("ad", "adding", "2026-11-13T13:59:30Z"), c("rd", "refused")], [], now)).toMatch(/^AD adding… · RD refused, /);
    // Found on the account still downloading: not in Cached on, so said.
    expect(otherProvidersLine([c("ad", "added", "2026-11-13T12:00:00Z", "Already on AD, still downloading (12%)")], [], now)).toBe("AD still downloading (12%), 2 h ago");
    // The Torrents row has no Cached on: every result shows.
    expect(otherProvidersLine([c("tb", "added", "2026-11-13T12:00:00Z", "Added to TB")], undefined, now)).toBe("TB added, 2 h ago");
  });
  it("labels and matches a custom provider key by its code, as Cached on does", () => {
    const home = (state: ProviderCopy["state"]) => ({ ...c("torbox-home", state), code: "tb" });
    expect(otherProvidersLine([home("added")], ["tb"], now)).toBe("");
    expect(otherProvidersLine([home("not_cached")], ["tb"], now)).toBe("TB not cached, 2 h ago");
  });
  it("polls while a copy is unfinished", () => {
    expect(copiesUnfinished([c("rd", "checking")])).toBe(true);
    expect(copiesUnfinished([c("rd", "failed"), c("tb", "added")])).toBe(false);
    expect(copiesUnfinished(undefined)).toBe(false);
  });
  it("says what the action queued, following the current copies", () => {
    expect(queuedWords([])).toBe("Every provider already holds it.");
    expect(queuedWords([c("ad", "queued"), c("tb", "queued")])).toBe("Adding to AD, TB. The result shows here when it ends.");
    expect(queuedWords([c("ad", "queued"), c("tb", "queued")], [c("ad", "not_cached"), c("tb", "checking")])).toBe("Adding to TB. The result shows here when it ends.");
    expect(queuedWords([c("ad", "queued")], [c("ad", "added")])).toBe("");
  });
  it("shows a hold's end in the browser's zone, from the time the server sends", () => {
    const held = { ...c("rd", "skipped", "2026-11-13T12:00:00Z", "Skipped: Real-Debrid is holding adds"), until: "2026-11-13T14:05:00Z" };
    expect(copyReasons([held])).toBe(`RD: Skipped: Real-Debrid is holding adds until ${fmtTime("2026-11-13T14:05:00Z")}`);
  });
});
