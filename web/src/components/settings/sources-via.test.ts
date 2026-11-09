import { describe, expect, it } from "vitest";
import type { SettingsRecord } from "@/api/settings";
import type { CardigannDefinition } from "@/api/cardigann";
import { rssProblemsOf, via } from "./sources";

describe("via", () => {
  it("names the catalog definition, and says when it cannot feed the RSS sync", () => {
    const r = { key: "ix", kind: "cardigann", config: { kind: "cardigann", definition: "1337x" } } as unknown as SettingsRecord;
    expect(via(r)).toBe("Catalog · 1337x");
    expect(via(r, { other: "x" })).toBe("Catalog · 1337x");
    expect(via(r, { "1337x": "the site maps no movie or TV category" })).toBe("Catalog · 1337x · no RSS: the site maps no movie or TV category");
    const t = { key: "p", kind: "torznab", config: { kind: "torznab", url: "http://prowlarr:9696/1/api" } } as unknown as SettingsRecord;
    expect(via(t, { "1337x": "x" })).toBe("Torznab · prowlarr:9696");
  });

  it("collects the catalog's definitions without RSS", () => {
    const defs = [
      { id: "tpb", rss: true },
      { id: "music", rss: false, rss_problem: "the site maps no movie or TV category" },
      { id: "old" },
    ] as unknown as CardigannDefinition[];
    expect(rssProblemsOf(defs)).toEqual({ music: "the site maps no movie or TV category" });
    expect(rssProblemsOf(undefined)).toEqual({});
  });
});
