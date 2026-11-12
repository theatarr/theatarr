// Link Prowlarr (docs/design/settings.md, "Adding an indexer"): Prowlarr's indexers as Torznab feeds. The trackers
// and their logins stay in Prowlarr; Theatarr reads its indexer list on the LAN and adds the ticked ones as feeds.
// The address and key are cardigann.prowlarr; no catalog is needed. The key is never answered. Linking is a settings
// write (POST /settings/prowlarr/feeds through useSettingsWrite): {generation, ids, versions}.
import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

/** linkable: can be added (key is the one it gets); linked: an indexer already has its URL (linked is that key);
 * disabled: off in Prowlarr; usenet: Theatarr searches torrents only. */
export type ProwlarrFeed = {
  id: number;
  name: string;
  privacy: string;
  protocol: string;
  enabled: boolean;
  url: string;
  key?: string;
  linked?: string;
  status: "linkable" | "linked" | "disabled" | "usenet";
};

export type ProwlarrFeeds = { configured: boolean; url?: string; feeds?: ProwlarrFeed[] };

export const prowlarrFeedsKey = ["settings", "prowlarr", "feeds"] as const;

/** Reads Prowlarr's indexer list through Theatarr; configured is false until an address and key are stored. A failed
 * read (Prowlarr unreachable) is not retried on its own: only Try again or a new save reads it again. */
export const useProwlarrFeeds = (enabled = true) =>
  useQuery({
    queryKey: prowlarrFeedsKey,
    queryFn: () => api<ProwlarrFeeds>("/settings/prowlarr/feeds"),
    enabled,
    retry: false,
    retryOnMount: false,
    refetchOnWindowFocus: false,
  });
