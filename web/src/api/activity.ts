// Query hooks for the activity, the calendar and Home's Continue row (GET /activity, /activity/continue, /calendar).
// The live stream's download and title events invalidate these keys (api/events.tsx), so screens update without a
// reload.
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { qk } from "./query-keys";

export type ActivityPage = Schemas["ActivityPage"];
export type ActivityDownload = Schemas["ActivityDownload"];
export type ActivityEvent = Schemas["ActivityEvent"];
export type ActivityBlocked = Schemas["ActivityBlocked"];
export type ActivityTitle = Schemas["ActivityTitle"];
export type ContinueRow = Schemas["ContinueRow"];
export type ContinueItem = Schemas["ContinueItem"];
export type CalendarPage = Schemas["CalendarPage"];
export type CalendarEntry = Schemas["CalendarEntry"];
export type DownloadEvent = Schemas["DownloadEvent"];
export type TitleEvent = Schemas["TitleEvent"];

/** Query keys the live events invalidate (query-keys.ts). */
export const activityKeys = { all: qk.activity.all, calendar: qk.calendar.all };

type View = { as?: "user" };

export const useActivityNow = (v: View = {}) =>
  useQuery({
    queryKey: [...activityKeys.all, "now", v],
    queryFn: () => api<ActivityPage>("/activity" + qs({ state: "now", as: v.as })),
  });

/** History filters as API parameters (lib/activity-history.ts historyParams). */
type HistoryParams = { title?: string; version?: string; kinds?: string; since?: string };

export const useActivityHistory = (v: View = {}, f: HistoryParams = {}) =>
  useInfiniteQuery({
    queryKey: [...activityKeys.all, "history", v, f],
    queryFn: ({ pageParam }) =>
      api<ActivityPage>("/activity" + qs({ state: "history", limit: 50, cursor: pageParam || undefined, as: v.as, ...f })),
    placeholderData: keepPreviousData,
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });

/** Blocklist filters and page: part of a title's name, a library key, and the offset (50 a page). */
export type BlockedParams = { title?: string; version?: string; offset?: number };

export const BLOCKED_PAGE = 50;

export const useActivityBlocked = (f: BlockedParams = {}) =>
  useQuery({
    queryKey: [...activityKeys.all, "blocked", f],
    queryFn: () =>
      api<ActivityPage>("/activity" + qs({ state: "blocked", limit: BLOCKED_PAGE, title: f.title || undefined, version: f.version || undefined, offset: f.offset || undefined })),
    placeholderData: keepPreviousData,
  });

export const useContinue = (limit = 20) =>
  useQuery({
    queryKey: [...activityKeys.all, "continue", limit],
    queryFn: () => api<ContinueRow>("/activity/continue" + qs({ limit })),
  });

export const useCalendar = (from: string, to: string) =>
  useQuery({
    queryKey: [...activityKeys.calendar, from, to],
    queryFn: () => api<CalendarPage>("/calendar" + qs({ from, to })),
    placeholderData: keepPreviousData,
  });


/** Mounted only by an open Inspect/Details panel; live Activity invalidation refreshes it too. */
export const useActivityAttempts = (id: number) =>
  useQuery({
    queryKey: [...activityKeys.all, "attempts", id],
    queryFn: () => api<Schemas["ActivityAttempts"]>(`/activity/downloads/${id}/attempts`),
  });
