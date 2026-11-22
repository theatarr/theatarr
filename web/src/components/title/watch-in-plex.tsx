// Watch in Plex (docs/design/now-playing.md "Watch in Plex"): on the title page, one button per library of the title
// with a file. Nothing is asked until the tap: then GET /titles/{id}/plex?version=<library> looks the title up, and a
// found item opens in the Plex app on iOS (app_url) and in Plex Web everywhere else (web_url, a new tab); otherwise the
// server's sentence ("Not found in Plex yet", or what to fix) shows in place.
import { useMutation } from "@tanstack/react-query";
import { ExternalLink, Loader2 } from "lucide-react";
import { api, qs, type Schemas } from "@/api/client";
import type { Version } from "@/api/library";
import { Button } from "@/components/ui/button";

type PlexLink = Schemas["PlexLink"];

/** iPhone and iPad (an iPad says Mac in its user agent, but has touch): the Plex app opens app_url there. */
function isIOS(): boolean {
  const ua = navigator.userAgent;
  return /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
}

function PlexButton({ titleId, v, named }: { titleId: number; v: Version; named: boolean }) {
  const look = useMutation({
    mutationFn: () => api<PlexLink>(`/titles/${titleId}/plex` + qs({ version: v.key })),
    onSuccess: (r) => {
      if (!r.found) return;
      if (isIOS() && r.app_url) window.location.assign(r.app_url);
      else if (r.web_url) window.open(r.web_url, "_blank", "noopener");
    },
  });
  const message = look.data && !look.data.found ? look.data.message || "Not found in Plex yet" : look.error ? (look.error as Error).message : "";
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Button size="sm" variant="outline" className="h-auto min-h-8 whitespace-normal text-left" disabled={look.isPending} onClick={() => look.mutate()}>
        {look.isPending ? <Loader2 className="animate-spin" /> : <ExternalLink />}
        {named ? `Watch in Plex · ${v.label || v.key}` : "Watch in Plex"}
      </Button>
      {message ? <p className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]" role="status" data-testid="watch-in-plex-message">{message}</p> : null}
    </div>
  );
}

/** One Watch in Plex per library with a file; none without one. */
export function WatchInPlex({ titleId, versions }: { titleId: number; versions: Version[] }) {
  const held = versions.filter((v) => v.has_file && v.key);
  if (held.length === 0) return null;
  return (
    <div className="flex flex-wrap items-start gap-2" data-testid="watch-in-plex">
      {held.map((v) => <PlexButton key={v.key} titleId={titleId} v={v} named={held.length > 1} />)}
    </div>
  );
}
