// Season and episode management (docs/design/manage.md): the episode detail, episode-scoped releases, season and
// episode monitoring and search.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import type { Releases, SearchOutcome } from "./add";

export type UpgradeInfo = Schemas["UpgradeInfo"];
export type UpgradeNext = Schemas["UpgradeNext"];
export type Cutoff = Schemas["Cutoff"];
export type VersionControl = Schemas["VersionControl"];
export type MediaInfo = Schemas["MediaInfo"];
export type EpisodeDetail = Schemas["EpisodeDetail"];
export type EpisodeVersionDetail = EpisodeDetail["versions"][number];
export type EpisodeFile = Schemas["EpisodeFile"];
export type ManageOutcome = Schemas["ManageOutcome"];

export const useEpisode = (titleId: number, eid: number | null) =>
  useQuery({
    queryKey: ["title", String(titleId), "episode", eid],
    queryFn: () => api<EpisodeDetail>(`/titles/${titleId}/episodes/${eid}`),
    enabled: eid !== null && eid > 0,
  });

export function useEpisodeReleases(titleId: number, eid: number, version: string | undefined, live: boolean, enabled: boolean) {
  return useQuery({
    queryKey: ["releases", titleId, "episode", eid, version ?? "", live],
    queryFn: () => api<Releases>(`/titles/${titleId}/episodes/${eid}/releases` + qs({ version, live: live ? 1 : undefined })),
    enabled,
    // A live answer is a fresh indexer search: never refetched or retried on its own.
    staleTime: live ? Infinity : 30_000,
    refetchOnWindowFocus: !live,
    ...(live ? { retry: false } : {}),
  });
}

function useInvalidateTitle(titleId: number) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["title", String(titleId)] });
    void qc.invalidateQueries({ queryKey: ["title", titleId] });
    void qc.invalidateQueries({ queryKey: ["titles"] });
  };
}

export type MonitorTarget = { season?: number; episode_ids?: number[]; versions?: string[]; monitored: boolean };

/** Monitor or unmonitor a season (season) or episodes (episode_ids). */
export function useMonitor(titleId: number) {
  const done = useInvalidateTitle(titleId);
  return useMutation({
    mutationFn: ({ season, episode_ids, versions, monitored }: MonitorTarget) =>
      season !== undefined
        ? api<ManageOutcome>(`/titles/${titleId}/seasons/${season}/monitor`, { method: "PUT", body: { monitored, versions } })
        : api<ManageOutcome>(`/titles/${titleId}/episodes/monitor`, { method: "PUT", body: { monitored, versions, episode_ids } }),
    onSettled: done,
  });
}

export type SearchTarget = { season?: number; episode_ids?: number[]; versions?: string[] };

/** Search a season (season) or episodes (episode_ids) now. */
export function useScopedSearch(titleId: number) {
  return useMutation({
    mutationFn: ({ season, episode_ids, versions }: SearchTarget) =>
      season !== undefined
        ? api<SearchOutcome>(`/titles/${titleId}/seasons/${season}/search`, { method: "POST", body: { versions } })
        : api<SearchOutcome>(`/titles/${titleId}/episodes/search`, { method: "POST", body: { versions, episode_ids } }),
  });
}

export type SourceCeiling = Schemas["SourceCeiling"];
export type CeilingOverrideRequest = Schemas["CeilingOverrideRequest"];

export type MinimumAvailability = Schemas["MinimumAvailabilityRequest"]["minimum_availability"];
export type SeriesType = Schemas["SeriesTypeRequest"]["series_type"];

/** A movie's minimum availability in one library, as an explicit choice (POST /titles/minimum-availability). */
export function useMinimumAvailability(titleId: number) {
  const done = useInvalidateTitle(titleId);
  return useMutation({
    mutationFn: ({ version, value }: { version: string; value: MinimumAvailability }) =>
      api<Schemas["TitleChoiceResult"]>("/titles/minimum-availability", { method: "POST", body: { title_ids: [titleId], versions: [version], minimum_availability: value } }),
    onSettled: done,
  });
}

/** A series' type (POST /titles/series-type). */
export function useSeriesType(titleId: number) {
  const done = useInvalidateTitle(titleId);
  return useMutation({
    mutationFn: (value: SeriesType) =>
      api<Schemas["TitleChoiceResult"]>("/titles/series-type", { method: "POST", body: { title_ids: [titleId], series_type: value } }),
    onSettled: done,
  });
}

export function useCeiling(titleId: number) {
  const done = useInvalidateTitle(titleId);
  return useMutation({
    mutationFn: ({ clear, ...body }: CeilingOverrideRequest & { clear?: boolean }) =>
      clear
        ? api<{ ok: boolean }>(`/titles/${titleId}/ceiling` + qs({ season: body.season }), { method: "DELETE" })
        : api<{ ok: boolean }>(`/titles/${titleId}/ceiling`, { method: "PUT", body }),
    onSettled: done,
  });
}
