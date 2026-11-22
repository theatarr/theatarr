// Watch in Plex (docs/design/now-playing.md "Watch in Plex"): on the title page, one button per library of the title
// with a file. Nothing is asked until the tap: then GET /titles/{id}/plex?version=<library> looks the title up, and a
// found item opens in the Plex app on iOS (app_url, with an "Open in Plex web" link kept beneath the button for a phone
// without the app) and in Plex Web everywhere else (web_url, in a blank tab opened at the tap, before the lookup, so a
// slow answer does not lose the tap's permission to open it; an "Open in Plex" link when the browser refused the tab);
// otherwise the tab is closed and the server's sentence ("Not found in Plex yet", or what to fix) shows in place.
import { useMutation } from "@tanstack/react-query";
import { useRef, useState } from "react";
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

/** The hand-over to the Plex app (a test replaces it: jsdom cannot navigate). */
export const plexHandover = { toApp: (url: string) => window.location.assign(url) };

function PlexButton({ titleId, v, named }: { titleId: number; v: Version; named: boolean }) {
  // A link shown beneath the button: Plex Web when the browser refused the new tab, or on iOS after the hand-over to
  // the app (a phone without the app still has a way).
  const [link, setLink] = useState<{ href: string; label: string }>();
  // The tab opened at the tap; null when the browser refused it, or on iOS (the app opens there).
  const tab = useRef<Window | null>(null);
  const take = () => {
    const t = tab.current;
    tab.current = null;
    return t;
  };
  const look = useMutation({
    mutationFn: () => api<PlexLink>(`/titles/${titleId}/plex` + qs({ version: v.key })),
    onMutate: () => setLink(undefined),
    onSuccess: (r) => {
      const t = take();
      if (r.found && isIOS() && r.app_url) {
        t?.close();
        plexHandover.toApp(r.app_url);
        if (r.web_url) setLink({ href: r.web_url, label: "Open in Plex web" });
      } else if (r.found && r.web_url) {
        if (t) t.location.href = r.web_url;
        else setLink({ href: r.web_url, label: "Open in Plex" });
      } else t?.close();
    },
    onError: () => take()?.close(),
  });
  const tap = () => {
    if (!isIOS()) {
      // A blank tab cut from this page, sent to Plex Web when the answer comes: with noopener window.open gives
      // nothing back even when the tab opens, so the fallback link could not tell a refused tab from an opened one.
      tab.current?.close();
      tab.current = window.open("", "_blank");
      if (tab.current) tab.current.opener = null;
    }
    look.mutate();
  };
  const message = look.data && !look.data.found ? look.data.message || "Not found in Plex yet" : look.error ? (look.error as Error).message : "";
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Button size="sm" variant="outline" className="h-auto min-h-8 max-w-full whitespace-normal text-left" disabled={look.isPending} onClick={tap}>
        {look.isPending ? <Loader2 className="animate-spin" /> : <ExternalLink />}
        {/* A long library name wraps inside the button rather than running past it. */}
        <span className="min-w-0 [overflow-wrap:anywhere]">{named ? `Watch in Plex · ${v.label || v.key}` : "Watch in Plex"}</span>
      </Button>
      {message ? <p className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]" role="status" data-testid="watch-in-plex-message">{message}</p> : null}
      {link ? <a href={link.href} target="_blank" rel="noopener noreferrer" className="text-sm font-medium text-primary hover:underline">{link.label}</a> : null}
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
