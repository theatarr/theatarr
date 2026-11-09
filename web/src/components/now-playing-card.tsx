// Now playing card on Home, for admins (docs/design/now-playing.md): one line per playback (who, what, health), linked to
// the Now playing page. It renders nothing while the feature is off, Plex cannot be read or nothing plays.
import { ChevronRight, MonitorPlay } from "lucide-react";
import { Link } from "react-router";
import { useNowPlaying } from "@/api/nowplaying";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { healthBadge, itemLabel, progress } from "@/lib/now-playing";

export function NowPlayingCard() {
  const q = useNowPlaying();
  const sessions = q.data?.enabled ? q.data.sessions : [];
  if (!sessions.length) return null;
  return (
    <section className="mb-7" aria-labelledby="home-now-playing" data-testid="home-now-playing">
      <Link to="/now-playing" className="group mb-2 flex items-center gap-2">
        <MonitorPlay className="size-4 text-primary" />
        <h2 id="home-now-playing" className="text-lg font-semibold">Now playing</h2>
        <span className="text-sm text-muted-foreground">{sessions.length}</span>
        <ChevronRight className="size-4 text-muted-foreground transition group-hover:translate-x-0.5" />
      </Link>
      <Card className="divide-y divide-border">
        {sessions.map((s) => {
          const h = healthBadge(s.health);
          return (
            <Link key={s.key} to="/now-playing" className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 hover:bg-muted/40">
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{itemLabel(s)}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {s.user} · {s.player} · {Math.round(progress(s) * 100)}%
                </span>
              </span>
              <Badge tone={h.tone}>{h.label}</Badge>
            </Link>
          );
        })}
      </Card>
    </section>
  );
}
