// The add-indexer dialog's logic, kept out of the components so it is testable: catalog filters, the set-up form
// built from a definition's settings, the writes a set-up or a Prowlarr proposal turns into, and the libraries that
// suit a new indexer. Every indexer made here is saved on, with a budget of its own, and joins the libraries the form
// ticks in the same save (docs/design/settings.md, "Adding an indexer").
import type { CardigannDefinition, CardigannSetting, ProwlarrProposal } from "@/api/cardigann";
import type { VersionRecord } from "@/api/settings-versions";
import { wordMatcher } from "@/lib/words";

export type CatalogCategory = "movies" | "tv" | "anime";
export type CatalogFilter = { q: string; privacy: string; category: "" | CatalogCategory; language: string; runsHere: boolean };
export const NO_FILTER: CatalogFilter = { q: "", privacy: "", category: "", language: "", runsHere: true };

/** The content a definition maps to, from its Newznab categories: 2000s movies, 5000s TV, 5070 anime. */
export function contentOf(categories: number[]): CatalogCategory[] {
  const out: CatalogCategory[] = [];
  if (categories.some((c) => c >= 2000 && c < 3000)) out.push("movies");
  if (categories.some((c) => c >= 5000 && c < 6000)) out.push("tv");
  if (categories.includes(5070)) out.push("anime");
  return out;
}

/** Runs here: without an account, or with one the set-up form asks for. FlareSolverr and unsupported ones don't. */
export const runsHere = (d: CardigannDefinition) => d.status === "runs" || d.status === "needs_account";

export function filterCatalog(defs: CardigannDefinition[], f: CatalogFilter): CardigannDefinition[] {
  const match = wordMatcher(f.q);
  const lang = f.language.toLowerCase();
  return defs.filter(
    (d) =>
      match(d.id, d.name, d.description) &&
      (!f.privacy || d.privacy === f.privacy) &&
      (!f.category || contentOf(d.categories).includes(f.category)) &&
      (!lang || d.language.toLowerCase().startsWith(lang)) &&
      (!f.runsHere || runsHere(d)),
  );
}

/** The language prefixes in the catalog (en of en-US), for the language filter. */
export function catalogLanguages(defs: CardigannDefinition[]): string[] {
  return [...new Set(defs.map((d) => d.language.toLowerCase().split(/[-_]/)[0]).filter(Boolean))].sort();
}

export const isInfo = (s: CardigannSetting) => s.type.startsWith("info");

/** Settings that go in the credential: the definition's secret ones, plus the account name, which sits with them. */
export const inCredential = (s: CardigannSetting) => !!s.secret || s.name.toLowerCase() === "username";

export type SetupValues = Record<string, string | boolean>;

export type SetupForm = {
  key: string;
  url: string;
  priority: string;
  requests: string;
  per: string;
  values: SetupValues;
};

/** A key for a new indexer from a definition id: lower case, letters, digits and dashes. */
export function suggestedKey(id: string, taken: string[]): string {
  const base = id.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "indexer";
  let key = base;
  for (let i = 2; taken.includes(key); i++) key = `${base}-${i}`;
  return key;
}

function defaultValue(s: CardigannSetting): string | boolean {
  if (s.type === "checkbox") return s.default === true;
  if (typeof s.default === "string") return s.default;
  return s.type === "select" && s.options?.length ? s.options[0].value : "";
}

export function setupForm(d: CardigannDefinition, taken: string[]): SetupForm {
  const values: SetupValues = {};
  for (const s of d.settings) if (!isInfo(s)) values[s.name] = inCredential(s) ? "" : defaultValue(s);
  return { key: suggestedKey(d.id, taken), url: d.links[0] ?? "", priority: "10", requests: "10", per: "1m", values };
}

export type IndexerWrite = {
  key: string;
  priority: number;
  config: Record<string, unknown>;
  /** The credential, a JSON object of the secret settings; absent when the site needs none. */
  secret?: string;
  budget: { requests: number; per: string };
};

const KEY_RE = /^[a-z0-9][a-z0-9._-]*$/i;
const DURATION_RE = /^\d+(ms|s|m|h)$/;

/** The indexer and its own budget from a filled set-up form, or the field errors. Non-secret settings are sent only
 * when they differ from the definition's default; privacy is the definition's and the URL one of its links. */
export function setupWrite(d: CardigannDefinition, f: SetupForm, taken: { indexers: string[]; budgets: string[] }): { write?: IndexerWrite; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const key = f.key.trim();
  if (!KEY_RE.test(key)) errors.key = key ? "letters, digits, dot, dash and underscore" : "required";
  else if (taken.indexers.includes(key)) errors.key = `indexer ${key} already exists`;
  if (!d.links.includes(f.url)) errors.url = "pick one of the definition's site addresses";
  if (!/^\d+$/.test(f.priority.trim())) errors.priority = "a whole number, 0 or more";
  if (!/^[1-9]\d*$/.test(f.requests.trim())) errors.requests = "a whole number, 1 or more";
  if (!DURATION_RE.test(f.per.trim())) errors.per = "a duration such as 1m or 30s";
  const settings: Record<string, string> = {};
  const secret: Record<string, string> = {};
  for (const s of d.settings) {
    if (isInfo(s)) continue;
    const v = f.values[s.name];
    if (inCredential(s)) {
      const text = typeof v === "string" ? v : "";
      if (text !== "") secret[s.name] = text;
      else if (s.required) errors[`setting.${s.name}`] = "required";
      continue;
    }
    if (s.type === "checkbox") {
      if ((v === true) !== (s.default === true)) settings[s.name] = v === true ? "true" : "false";
      continue;
    }
    const text = typeof v === "string" ? v.trim() : "";
    if (text === "" && s.required) errors[`setting.${s.name}`] = "required";
    else if (text !== "" && text !== (s.default ?? "")) settings[s.name] = text;
  }
  if (Object.keys(errors).length > 0) return { errors };
  const config: Record<string, unknown> = { kind: "cardigann", definition: d.id, url: f.url, privacy: d.privacy, budget: key };
  if (Object.keys(settings).length > 0) config.settings = settings;
  if (d.hosts?.length) config.hosts = d.hosts;
  return {
    errors,
    write: {
      key,
      priority: Number(f.priority.trim()),
      config,
      secret: Object.keys(secret).length > 0 ? JSON.stringify(secret) : undefined,
      budget: { requests: Number(f.requests.trim()), per: f.per.trim() },
    },
  };
}

/** The writes for a ticked Prowlarr proposal: its indexer and its own budget. */
export function proposalWrite(p: ProwlarrProposal, priority: number): IndexerWrite | undefined {
  if (p.status !== "proposed" || !p.key || !p.indexer) return undefined;
  const { kind, definition, url, privacy, budget, settings, hosts } = p.indexer;
  const config: Record<string, unknown> = { kind, definition, url, privacy, budget };
  if (settings && Object.keys(settings).length > 0) config.settings = settings;
  if (hosts?.length) config.hosts = hosts;
  const requests = Number(p.budget?.requests ?? 10);
  return { key: p.key, priority, config, budget: { requests: Number.isFinite(requests) && requests > 0 ? requests : 10, per: p.budget?.per || "1m" } };
}

/** The settings API bodies, in the order they are sent: the budget first (the indexer refers to it), then the indexer,
 * on unless asked otherwise, with the libraries it joins. A budget that already exists under the key (left by an
 * earlier test) is reused. */
export function writeBodies(w: IndexerWrite, generation: number, budgetExists: boolean, enabled = true, versions: string[] = []) {
  const budget = budgetExists ? undefined : { generation, key: w.key, config: w.budget };
  const indexer = (gen: number): Record<string, unknown> => {
    const body: Record<string, unknown> = { generation: gen, key: w.key, config: w.config, enabled, priority: w.priority };
    if (w.secret !== undefined) body.secret = w.secret;
    if (versions.length > 0) body.versions = versions;
    return body;
  };
  return { budget, indexer };
}

/** The libraries that suit a new indexer, ticked by default. A catalog site goes by its categories: movies (2000s) suit
 * movie libraries, TV (5000s but 5070) series libraries, and anime (5070) series libraries that are not marked
 * non-anime and movie libraries marked anime. SeaDex suits the libraries not marked non-anime; every other source
 * (Torznab, Newznab, DMM, Torrentio, Zilean, a Prowlarr feed) suits every library. */
export function matchingLibraries(source: { kind: string; categories?: number[] }, libraries: VersionRecord[]): string[] {
  const suits = (v: VersionRecord): boolean => {
    const { kind, anime } = v.config;
    if (source.kind === "seadex") return anime !== false;
    if (source.kind !== "cardigann") return true;
    const cats = source.categories ?? [];
    const movies = cats.some((c) => c >= 2000 && c < 3000);
    const tv = cats.some((c) => c >= 5000 && c < 6000 && c !== 5070);
    const animeCat = cats.includes(5070);
    if (kind === "movie") return movies || (animeCat && anime === true);
    return tv || (animeCat && anime !== false);
  };
  return libraries.filter(suits).map((v) => v.key);
}

/** The Newznab category list as words. */
export function contentLabel(categories: number[]): string {
  const names: Record<CatalogCategory, string> = { movies: "Movies", tv: "TV", anime: "Anime" };
  return contentOf(categories).map((c) => names[c]).join(", ") || "Other";
}
