// Every TanStack Query key in one place. Reads, mutations and the live event handlers build their keys here, so an
// invalidation always matches what a screen cached. Title ids are strings in every key (a route param is a string,
// an API answer a number): qk.title(5) and qk.title("5") are the same key.
import type { GridQuery } from "./library";

type Id = string | number;
const id = (v: Id) => String(v);

export const qk = {
  me: ["me"] as const,
  status: ["status"] as const,
  gateway: ["gateway"] as const,
  readyz: ["readyz"] as const,
  health: ["health"] as const,
  jobs: {
    all: ["jobs"] as const,
    state: (state?: string, limit?: number) => ["jobs", state ?? "", limit ?? ""] as const,
    filtered: (f: unknown) => ["jobs", "filtered", f] as const,
    kind: (kind: string) => ["jobs", "kind", kind] as const,
  },
  errors: {
    all: ["errors"] as const,
    list: (f: unknown) => ["errors", "list", f] as const,
    entry: (e: Id) => ["errors", "entry", id(e)] as const,
    newCount: ["errors", "new-count"] as const,
  },
  imports: { all: ["imports"] as const, list: (limit: number) => ["imports", limit] as const },
  migrate: {
    list: ["migrate", "plans"] as const,
    plan: (plan: Id) => ["migrate", "plan", id(plan)] as const,
  },
  policy: { all: ["policy"] as const, validate: ["policy", "validate"] as const },
  keys: ["keys"] as const,
  shadow: {
    summary: ["shadow", "summary"] as const,
    diffs: (f: unknown) => ["shadow", "diffs", f] as const,
    diff: (d: string) => ["shadow", "diff", d] as const,
  },
  anime: {
    all: ["anime"] as const,
    franchises: ["anime", "franchises"] as const,
    franchise: (slug: string) => ["anime", "franchise", slug] as const,
    review: (state: string, franchise?: string) => ["anime", "review", state, franchise ?? ""] as const,
    playlists: ["anime", "playlists"] as const,
    uncovered: ["anime", "uncovered"] as const,
  },
  registry: {
    list: (q: string, state: string, offset: number) => ["registry", q, state, offset] as const,
    content: (hash: string) => ["registry", "content", hash] as const,
  },
  torrents: {
    all: ["torrents"] as const,
    report: ["torrents", "report"] as const,
    backups: ["torrents", "backups"] as const,
    journal: (limit: number) => ["torrents", "journal", limit] as const,
  },
  nowPlaying: ["nowplaying"] as const,
  sources: ["sources"] as const,
  /** The settings admin API (docs/design/settings.md): every write invalidates settings.all. */
  settings: {
    all: ["settings"] as const,
    overview: ["settings", "overview"] as const,
    schema: ["settings", "schema"] as const,
    audit: (before: number, limit: number) => ["settings", "audit", "desc", before, limit] as const,
    /** The definition catalog and its review holds (docs/design/cardigann.md). */
    cardigann: {
      all: ["settings", "cardigann"] as const,
      catalog: ["settings", "cardigann", "catalog"] as const,
      holds: ["settings", "cardigann", "holds"] as const,
    },
  },
  logFiles: ["log-files"] as const,
  logLevel: ["log-level"] as const,
  logs: (level: string, component: string) => ["logs", level, component] as const,
  journal: {
    all: ["journal"] as const,
    list: (f: unknown) => ["journal", "list", f] as const,
    entry: (e: string) => ["journal", "entry", e] as const,
    title: (t: Id) => ["journal", "title", id(t)] as const,
  },

  /** Every list of titles: the grids, the facets, the admin table. */
  titles: {
    all: ["titles"] as const,
    grid: (q: GridQuery) => ["titles", "grid", q] as const,
    facets: (kind?: string, anime?: boolean) => ["titles", "facets", kind ?? "", anime ?? ""] as const,
    table: (params: unknown) => ["titles", "table", params] as const,
  },
  /** One title: its page, its episodes and anything else keyed under it. */
  title: (t: Id) => ["title", id(t)] as const,
  episodes: (t: Id, season?: number) => ["title", id(t), "episodes", season ?? "all"] as const,
  // Not under title(): a live answer is an indexer search, which an invalidation of the title must never re-run.
  releases: (t: Id, version: string, live: boolean) => ["releases", id(t), version, live] as const,
  /** One interactive search (one opening of the window, one library and scope): never refetched by an invalidation.
   *  interactive(t, session) is every library's answer of one opening. */
  liveSearch: (t: Id, version: string, session: number, scope: string) => ["releases", id(t), "interactive", session, version, scope] as const,
  interactive: (t: Id, session: number) => ["releases", id(t), "interactive", session] as const,
  discover: {
    all: ["discover"] as const,
    home: ["discover", "home"] as const,
    row: (row: string) => ["discover", "row", row] as const,
  },
  search: {
    all: ["search"] as const,
    part: (scope: string, q: string, kind: string, limit: number) => ["search", scope, q, kind, limit] as const,
    pages: (q: string, kind: string, page: number) => ["search", "library-pages", q, kind, page] as const,
  },
  addOptions: {
    all: ["add-options"] as const,
    one: (kind?: string, tmdb?: number, title?: Id, anime?: boolean) =>
      ["add-options", kind ?? "", tmdb ?? "", title === undefined ? "" : id(title), anime ?? false] as const,
  },
  activity: { all: ["activity"] as const },
  calendar: { all: ["calendar"] as const },
};

/** What changes when a title's versions, seasons or files change: refresh every view of it. */
export const titleChanged = (t?: Id) =>
  [qk.titles.all, qk.discover.all, qk.search.all, qk.addOptions.all, ...(t === undefined ? [] : [qk.title(t)])] as const;
