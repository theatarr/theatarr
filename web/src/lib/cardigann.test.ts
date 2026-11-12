import { describe, expect, it } from "vitest";
import type { CardigannDefinition, ProwlarrProposal } from "@/api/cardigann";
import type { VersionRecord } from "@/api/settings-versions";
import { catalogLanguages, contentOf, filterCatalog, matchingLibraries, NO_FILTER, proposalWrite, setupForm, setupWrite, suggestedKey, writeBodies } from "./cardigann";

const def = (o: Partial<CardigannDefinition>): CardigannDefinition => ({
  id: "site",
  name: "Site",
  description: "",
  type: "public",
  privacy: "public",
  language: "en-US",
  protocol: "torrent",
  links: ["https://site.example/"],
  settings: [],
  categories: [2000],
  supported: true,
  status: "runs",
  ...o,
});

// Invented definitions: a public site, a private one with a form login, one behind Cloudflare and one that can't run.
const tpb = def({ id: "thepiratebay", name: "The Pirate Bay", categories: [2000, 5000], hosts: ["apibay.org"],
  settings: [{ name: "sort", label: "Sort", type: "select", default: "created", options: [{ value: "created", label: "created" }, { value: "seeders", label: "seeders" }] }] });
const privateSite = def({ id: "privatesite", name: "Private Site", type: "semi-private", privacy: "private", language: "fr-FR", status: "needs_account", categories: [5070],
  links: ["https://private.example/", "https://mirror.private.example/"],
  settings: [
    { name: "username", label: "Username", type: "text", required: true },
    { name: "password", label: "Password", type: "password", secret: true, required: true },
    { name: "freeleech", label: "Freeleech only", type: "checkbox", default: false },
    { name: "info_cookie", label: "How to get the cookie", type: "info_cookie", default: "Sign in..." },
  ] });
const cf = def({ id: "cfsite", name: "CF Site", status: "needs_flaresolverr" });
const broken = def({ id: "broken", name: "Broken", status: "unsupported", supported: false, problems: ["captcha login"] });

describe("catalog filters", () => {
  it("reads the content from the Newznab categories", () => {
    expect(contentOf([2010, 5070])).toEqual(["movies", "tv", "anime"]);
    expect(contentOf([8000])).toEqual([]);
  });
  it("hides what can't run here unless All is chosen, and filters by text, privacy, content and language", () => {
    const all = [tpb, privateSite, cf, broken];
    expect(filterCatalog(all, NO_FILTER).map((d) => d.id)).toEqual(["thepiratebay", "privatesite"]);
    expect(filterCatalog(all, { ...NO_FILTER, runsHere: false })).toHaveLength(4);
    expect(filterCatalog(all, { ...NO_FILTER, q: "PIRATE" }).map((d) => d.id)).toEqual(["thepiratebay"]);
    expect(filterCatalog(all, { ...NO_FILTER, privacy: "private" }).map((d) => d.id)).toEqual(["privatesite"]);
    expect(filterCatalog(all, { ...NO_FILTER, category: "anime" }).map((d) => d.id)).toEqual(["privatesite"]);
    expect(filterCatalog(all, { ...NO_FILTER, language: "fr" }).map((d) => d.id)).toEqual(["privatesite"]);
    expect(catalogLanguages(all)).toEqual(["en", "fr"]);
  });
});

describe("set-up form", () => {
  it("suggests a free key", () => {
    expect(suggestedKey("The.Pirate Bay", [])).toBe("the-pirate-bay");
    expect(suggestedKey("tpb", ["tpb", "tpb-2"])).toBe("tpb-3");
  });

  it("puts the account in the credential, sends only changed settings, and takes privacy and hosts from the definition", () => {
    const f = setupForm(privateSite, []);
    expect(f.url).toBe("https://private.example/");
    f.values = { ...f.values, username: "invented-user", password: "invented-pass", freeleech: true };
    f.url = "https://mirror.private.example/";
    const { write, errors } = setupWrite(privateSite, f, { indexers: [], budgets: [] });
    expect(errors).toEqual({});
    expect(write!.config).toEqual({ kind: "cardigann", definition: "privatesite", url: "https://mirror.private.example/", privacy: "private", budget: "privatesite", settings: { freeleech: "true" } });
    expect(JSON.parse(write!.secret!)).toEqual({ username: "invented-user", password: "invented-pass" });
    expect(write!.budget).toEqual({ requests: 10, per: "1m" });
  });

  it("drops a setting left at its default and lists the extra hosts", () => {
    const { write } = setupWrite(tpb, setupForm(tpb, []), { indexers: [], budgets: [] });
    expect(write!.config).toEqual({ kind: "cardigann", definition: "thepiratebay", url: "https://site.example/", privacy: "public", budget: "thepiratebay", hosts: ["apibay.org"] });
    expect(write!.secret).toBeUndefined();
  });

  it("refuses a missing required setting, a taken key, an address that is not the definition's and a bad budget", () => {
    const f = { ...setupForm(privateSite, []), key: "dmm", url: "https://elsewhere.example/", requests: "0", per: "soon" };
    const { write, errors } = setupWrite(privateSite, f, { indexers: ["dmm"], budgets: [] });
    expect(write).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["key", "per", "requests", "setting.password", "setting.username", "url"]);
  });
});

describe("writes", () => {
  it("writes the budget first and the indexer on with its libraries, reusing a budget that exists", () => {
    const { write } = setupWrite(tpb, setupForm(tpb, []), { indexers: [], budgets: [] });
    const fresh = writeBodies(write!, 4, false, true, ["movies", "tv"]);
    expect(fresh.budget).toEqual({ generation: 4, key: "thepiratebay", config: { requests: 10, per: "1m" } });
    expect(fresh.indexer(5)).toMatchObject({ generation: 5, key: "thepiratebay", enabled: true, priority: 10, versions: ["movies", "tv"] });
    expect(writeBodies(write!, 4, true).budget).toBeUndefined();
    // No library ticked: no versions field; off when asked.
    expect(writeBodies(write!, 4, true, false).indexer(5)).not.toHaveProperty("versions");
    expect(writeBodies(write!, 4, true, false).indexer(5).enabled).toBe(false);
  });

  it("turns only a proposed Prowlarr indexer into writes, with its own budget as a number", () => {
    const proposed: ProwlarrProposal = {
      prowlarr: { id: 9, name: "The Pirate Bay", definition_name: "thepiratebay", privacy: "public", enabled: true },
      status: "proposed",
      key: "thepiratebay",
      indexer: { kind: "cardigann", definition: "thepiratebay", url: "https://thepiratebay.org/", privacy: "public", budget: "thepiratebay", settings: { sort: "seeders" }, hosts: ["apibay.org"] },
      budget: { requests: "10", per: "1m" },
    };
    const w = proposalWrite(proposed, 12)!;
    expect(w.budget).toEqual({ requests: 10, per: "1m" });
    expect(w.config).toEqual({ kind: "cardigann", definition: "thepiratebay", url: "https://thepiratebay.org/", privacy: "public", budget: "thepiratebay", settings: { sort: "seeders" }, hosts: ["apibay.org"] });
    expect(writeBodies(w, 4, false).indexer(5)).toMatchObject({ enabled: true, priority: 12 });
    expect(proposalWrite({ ...proposed, status: "no_definition" }, 12)).toBeUndefined();
    expect(proposalWrite({ ...proposed, status: "usenet" }, 12)).toBeUndefined();
  });
});

describe("matchingLibraries", () => {
  const lib = (key: string, kind: "movie" | "series", anime: boolean | null = null): VersionRecord =>
    ({ id: 1, key, created_at: "", updated_at: "", roots: [], config: { kind, anime, mode: "active" } }) as unknown as VersionRecord;
  const libs = [lib("movies", "movie"), lib("anime-movies", "movie", true), lib("tv", "series", false), lib("series", "series"), lib("anime", "series", true)];

  it("goes by a catalog site's categories", () => {
    expect(matchingLibraries({ kind: "cardigann", categories: [2000, 2040] }, libs)).toEqual(["movies", "anime-movies"]);
    expect(matchingLibraries({ kind: "cardigann", categories: [5000, 5040] }, libs)).toEqual(["tv", "series", "anime"]);
    // A Nyaa-like site (anime only): the series libraries not marked non-anime, and the anime movie library.
    expect(matchingLibraries({ kind: "cardigann", categories: [5070] }, libs)).toEqual(["anime-movies", "series", "anime"]);
    expect(matchingLibraries({ kind: "cardigann", categories: [3000] }, libs)).toEqual([]);
  });

  it("ticks SeaDex for anime libraries and every library for the other sources", () => {
    expect(matchingLibraries({ kind: "seadex" }, libs)).toEqual(["movies", "anime-movies", "series", "anime"]);
    expect(matchingLibraries({ kind: "dmm" }, libs)).toEqual(libs.map((v) => v.key));
    expect(matchingLibraries({ kind: "torznab" }, libs)).toEqual(libs.map((v) => v.key));
  });
});
