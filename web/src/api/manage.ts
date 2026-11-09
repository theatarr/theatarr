// Season and episode management (docs/design/manage.md): the episode detail, episode-scoped releases, season and
// episode monitoring and search, and a version's journal entries.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import type { Releases, SearchOutcome } from "./add";
import type { JournalPage } from "./library";

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
    staleTime: live ? Infinity : 30_000,
    refetchOnWindowFocus: !live,
  });
}

/** The journal's entries for one version of a title, newest first (see files only). */
export const useVersionJournal = (titleId: number, version: string, enabled: boolean) =>
  useQuery({
    queryKey: ["journal", "title", String(titleId), version],
    queryFn: () => api<JournalPage>("/journal" + qs({ title: titleId, version, limit: 10 })),
    enabled,
  });

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
