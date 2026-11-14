import { describe, expect, it } from "vitest";
import type { SettingsRecord } from "@/api/settings";
import type { CardigannDefinition } from "@/api/cardigann";
import { rssProblemsOf, usedByWord, via, wantsCredential } from "./sources";
import { adapterWord } from "./record-form";
import { languageName } from "@/lib/cardigann";

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

describe("indexer words", () => {
  it("asks for a credential only where one is needed: not for keyless kinds or catalog sites without an account", () => {
    const cat = (definition: string) => ({ key: definition, kind: "cardigann", config: { kind: "cardigann", definition } }) as unknown as SettingsRecord;
    const keyless = new Set(["nyaa"]);
    expect(wantsCredential(cat("nyaa"), keyless)).toBe(false);
    expect(wantsCredential(cat("private-site"), keyless)).toBe(true);
    expect(wantsCredential({ key: "t", kind: "torrentio", config: { kind: "torrentio" } } as unknown as SettingsRecord)).toBe(false);
    expect(wantsCredential({ key: "p", kind: "torznab", config: { kind: "torznab" } } as unknown as SettingsRecord, keyless)).toBe(true);
  });

  it("names adapters, budget users and catalog languages in words", () => {
    expect(adapterWord("cardigann")).toBe("Catalog");
    expect(adapterWord("torznab")).toBe("Torznab");
    expect(adapterWord("future")).toBe("future");
    expect(["plex:server", "zurgapi:api", "zurglog:log", "rclonerc:rc", "client:decypharr", "indexer:nyaa", "odd"].map(usedByWord))
      .toEqual(["Plex server", "Zurg API", "Zurg log reader", "rclone remote control", "Download client decypharr", "Indexer nyaa", "odd"]);
    expect(languageName("uk-UA")).toBe("Ukrainian (Ukraine)");
    expect(languageName("en")).toBe("English");
    expect(languageName("not a tag")).toBe("not a tag");
  });
});

