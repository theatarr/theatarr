// The broken links behind the Status check (GET /health/dangling; admins): the latest dangling-link report's links,
// at most 500, oldest first. Read-only; loaded only while the list is open.
import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { qk } from "./query-keys";

export type DanglingList = Schemas["DanglingList"];
export type DanglingItem = Schemas["DanglingItem"];

/** The list, fetched when enabled and kept for a minute, as the health checks are. */
export const useDanglingLinks = (enabled: boolean) =>
  useQuery({ queryKey: [...qk.health, "dangling"], queryFn: () => api<DanglingList>("/health/dangling"), enabled, staleTime: 60_000 });
