// Now playing (docs/design/now-playing.md): GET /nowplaying, read every 10 s while the page is open (which also keeps the
// server polling Plex every 10 s).
import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { qk } from "./query-keys";

export type NowPlaying = Schemas["NowPlaying"];
export type NowPlayingSession = Schemas["NowPlayingSession"];

export const useNowPlaying = () =>
  useQuery({ queryKey: qk.nowPlaying, queryFn: () => api<NowPlaying>("/nowplaying"), refetchInterval: 10_000 });
