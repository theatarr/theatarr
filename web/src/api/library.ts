// Library reads (docs/design/ui.md): the grids (GET /titles, paged), their facet counts, the title page,
// a series' episodes and the Home rows. Types come from the generated schema.
import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "./auth";
import { api, qs, type Schemas } from "./client";
import type { Get } from "./hooks";
import { qk, titleChanged } from "./query-keys";
import { withDistinctLabels } from "@/lib/copies";

export type TitleRow = Schemas["TitleRow"];
export type TitlePage = Schemas["TitlePage"];
export type TitleDetail = Schemas["TitleDetail"];
export type Version = Schemas["Version"];
export type HistoryEvent = Schemas["HistoryEvent"];
export type TitleFacets = Schemas["TitleFacets"];
export type EpisodeList = Schemas["EpisodeList"];
export type Episode = Schemas["Episode"];
export type Discover = Schemas["Discover"];
export type DiscoverRow = Schemas["DiscoverRow"];
export type DiscoverItem = DiscoverRow["items"][number];
export type Availability = Schemas["Availability"];

/** The grid's query: GET /titles parameters, as the URL keeps them. */
export type GridQuery = {
  kind?: "movie" | "series";
  anime?: boolean;
  q?: string;
  with?: string;
  without?: string;
  state?: string;
  availability?: string;
  genre?: string;
  quality?: string;
  provider?: string;
  year_from?: number;
  year_to?: number;
  monitored?: boolean;
  sort?: string;
  /** The copy (version key) completion, quality and activity sorts read. */
  sort_version?: string;
  order?: string;
};

/** Titles per page of the grid: enough to fill a large screen twice. */
export const GRID_PAGE = 60;

export function useTitleGrid(q: GridQuery) {
  return useInfiniteQuery({
    queryKey: qk.titles.grid(q),
    queryFn: ({ pageParam, signal }) =>
      api<TitlePage>("/titles" + qs({ ...q, limit: GRID_PAGE, offset: pageParam }), { signal }),
    initialPageParam: 0,
    getNextPageParam: (last) => {
      const next = (last.offset ?? 0) + (last.items?.length ?? 0);
      return next < (last.total ?? 0) && (last.items?.length ?? 0) > 0 ? next : undefined;
    },
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  });
}

export const useFacets = (kind?: string, anime?: boolean) =>
  useQuery({
    queryKey: qk.titles.facets(kind, anime),
    queryFn: () => api<TitleFacets>("/titles/facets" + qs({ kind, anime })),
    staleTime: 60_000,
  });

// A title's copies shown together get labels that tell same-named ones apart (lib/copies distinctLabels).
const titleLabels = (d: TitleDetail): TitleDetail => (d.versions ? { ...d, versions: withDistinctLabels(d.versions) as TitleDetail["versions"] } : d);
const episodeLabels = (l: EpisodeList): EpisodeList => (l.versions ? { ...l, versions: withDistinctLabels(l.versions) } : l);

export const useTitle = (id: string, enabled = true) =>
  useQuery({ queryKey: qk.title(id), queryFn: () => api<TitleDetail>(`/titles/${encodeURIComponent(id)}`), enabled, select: titleLabels });

export const useEpisodes = (id: string, season?: number, enabled = true) =>
  useQuery({
    queryKey: qk.episodes(id, season),
    queryFn: () => api<EpisodeList>(`/titles/${encodeURIComponent(id)}/episodes` + qs({ season })),
    enabled,
    select: episodeLabels,
  });

/** One Home row in full (GET /discover/{row}), paged as the grid scrolls: every kind, the row's own order. */
export function useDiscoverRow(row: string) {
  return useInfiniteQuery({
    queryKey: qk.discover.row(row),
    queryFn: ({ pageParam, signal }) => api<DiscoverRow>(`/discover/${encodeURIComponent(row)}` + qs({ limit: GRID_PAGE, offset: pageParam || undefined }), { signal }),
    initialPageParam: 0,
    getNextPageParam: (last, all) => {
      const seen = all.reduce((n, p) => n + p.items.length, 0);
      return last.items.length > 0 && seen < last.total ? seen : undefined;
    },
    staleTime: 30_000,
  });
}

export const useDiscover = () =>
  useQuery({ queryKey: qk.discover.home, queryFn: () => api<Discover>("/discover"), staleTime: 30_000 });

export type JournalPage = Get<"/journal">;
export const useTitleJournal = (id: string, enabled: boolean) =>
  useQuery({
    queryKey: qk.journal.title(id),
    queryFn: () => api<JournalPage>("/journal" + qs({ title: id, limit: 50 })),
    enabled,
  });

export function useRefreshTitle(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api<{ job_id?: number }>(`/titles/${encodeURIComponent(id)}/refresh`, { method: "POST" }),
    onSuccess: () => {
      // The job runs within seconds; look again a little later.
      window.setTimeout(() => titleChanged(id).forEach((queryKey) => void qc.invalidateQueries({ queryKey })), 4000);
    },
  });
}

export type Perms = {
  seeFiles: boolean;
  admin: boolean;
  add: boolean;
  manage: boolean;
  request: boolean;
  manageRequests: boolean;
  users: boolean;
  ownKeys: boolean;
};

/** Permissions from GET /auth/me (docs/design/users.md). The server enforces them; the UI only hides what would fail. */
export function permsOf(p: readonly string[] | undefined): Perms {
  const has = (n: string) => (p ?? []).includes(n);
  return {
    seeFiles: has("see_files"),
    admin: has("admin"),
    add: has("add"),
    manage: has("manage"),
    request: has("request"),
    manageRequests: has("manage_requests"),
    users: has("users"),
    ownKeys: has("own_keys"),
  };
}

export function usePerms(): Perms {
  const me = useAuth();
  return permsOf(me.data?.permissions);
}
