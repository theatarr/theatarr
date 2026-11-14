import { settingHref } from "@/lib/settings-map";
import { RcloneConnection } from "@/components/settings/rclone-connection";
// Now playing (docs/design/now-playing.md): what Plex plays now, who watches on which player, how it streams and whether
// it plays smoothly, with the likeliest cause when it does not. Managers and admins also see where the file comes from
// (versions, release, providers, single copy, the zurg account serving it), the mount's fetch speed and the
// next-episode pre-warm. Users see their own playbacks only.
import { MonitorPlay } from "lucide-react";
import { Link } from "react-router";
import { useNowPlaying, type NowPlayingSession } from "@/api/nowplaying";
import { usePerms } from "@/api/library";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Strip, VersionTag, type StripItem } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtBytes } from "@/lib/format";
import { fmtClock, fmtMbps, healthBadge, itemLabel, placeLabel, prewarmText, progress, streamLabel } from "@/lib/now-playing";
import { ProviderTag } from "@/components/provider-pills";
import { TorrentLink } from "@/components/title/info-hash";

export function NowPlayingPage() {
  const q = useNowPlaying();
  const perms = usePerms();
  const data = q.data;
  const strip: StripItem[] = [];
  if (data?.enabled) {
    const struggling = data.sessions.filter((s) => s.health.verdict === "buffering" || s.health.verdict === "slow").length;
    strip.push({ label: data.sessions.length === 1 ? "playback" : "playbacks", value: data.sessions.length });
    strip.push({ label: "struggling", value: struggling, tone: struggling ? "bad" : "ok" });
    if (data.zurg_streams !== undefined) strip.push({ label: "zurg streams", value: data.zurg_streams });
    if (data.cache) {
      const c = data.cache;
      strip.push({
        label: "rclone cache",
        value: c.outOfSpace ? "full" : c.erroredFiles ? `${c.erroredFiles} errored` : fmtBytes(c.bytesUsed),
        tone: c.outOfSpace || c.erroredFiles ? "bad" : undefined,
      });
    }
  }
  return (
    <div>
      <PageHeader
        title="Now playing"
        description={
          perms.seeFiles
            ? "What Plex plays now, where each file comes from and why a playback struggles."
            : "What you are playing on Plex now."
        }
        actions={data?.updated_at ? <span className="text-sm text-muted-foreground">Updated {fmtAgo(data.updated_at)}</span> : null}
      />
      {perms.admin ? <><RcloneConnection /><Link to={settingHref("library", "mount")!}>rclone remote control: Libraries › File management</Link></> : null}
      {q.isLoading ? <Loading /> : null}
      {q.error ? <ErrorState error={q.error} /> : null}
      {data && !data.enabled ? (
        <Empty icon={MonitorPlay}>
          {perms.admin ? (
            <>Now playing is off. Set up Plex in <Link to={settingHref("library", "plex")!}>Plex &amp; playback</Link> (Plex server: address and token, Now playing on).</>
          ) : (
            "Now playing is off. Ask an administrator to set up Plex."
          )}
        </Empty>
      ) : null}
      {data?.enabled ? (
        <>
          {strip.length ? <Strip items={strip} className="mb-5" /> : null}
          {data.error ? (
            <Card className="mb-5 px-4 py-3 text-sm text-[var(--bad-ink)]">Plex: {data.error}</Card>
          ) : null}
          {data.sessions.length === 0 ? (
            <Empty icon={MonitorPlay}>Nothing is playing.</Empty>
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {data.sessions.map((s) => (
                <SessionCard key={s.key} s={s} seeFiles={perms.seeFiles} />
              ))}
            </div>
          )}
        </>
      ) : null}
    </div>
  );
}

function SessionCard({ s, seeFiles }: { s: NowPlayingSession; seeFiles: boolean }) {
  const health = healthBadge(s.health);
  const src = s.source;
  const pw = prewarmText(s.prewarm);
  const label = itemLabel(s);
  const details: [string, React.ReactNode][] = [];
  if (seeFiles && s.health.detail) details.push(["Why", s.health.detail]);
  if (seeFiles && src) {
    if (src.versions?.length) {
      details.push(["Libraries", <span className="flex flex-wrap gap-1.5">{src.versions.map((v) => <VersionTag key={v} k={v} />)}</span>]);
    }
    if (src.release) details.push(["Release", <Mono>{src.release}</Mono>]);
    if (src.providers?.length || src.served_by) {
      details.push([
        "Providers",
        <span className="flex flex-wrap items-center gap-1.5">
          {(src.providers ?? []).map((p) => <Badge key={p} tone="ok"><ProviderTag account={p} /></Badge>)}
          {src.hash ? <TorrentLink hash={src.hash} /> : null}
          {src.single_copy ? <Badge tone="warning">single copy</Badge> : null}
          {src.served_by ? <span className="text-muted-foreground">served by {src.served_by}</span> : null}
        </span>,
      ]);
    }
    if (!src.matched) details.push(["Source", <span className="text-muted-foreground">not a Theatarr link or a known release</span>]);
    details.push(["File", <Mono className="break-all">{src.target || src.file}</Mono>]);
  }
  if (seeFiles && s.fetch) {
    details.push([
      "Mount fetch",
      `${fmtMbps(s.fetch.speed)}${s.fetch.ratio ? ` · ${s.fetch.ratio.toFixed(1)}x the bitrate` : ""}`,
    ]);
  }
  if (seeFiles && pw) details.push(["Next episode", <Badge tone={pw.tone}>{pw.text}</Badge>]);
  return (
    <Card className="p-4" data-testid="now-playing-session">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="font-medium text-[var(--ink)]">
            {src?.title_id ? (
              <Link to={`/title/${src.title_id}`} className="hover:underline">{label}</Link>
            ) : (
              label
            )}
          </div>
          <div className="mt-0.5 text-sm text-muted-foreground">
            {s.user} · {s.player}
            {s.platform && s.platform !== s.player ? ` (${s.platform})` : ""}
          </div>
        </div>
        <Badge tone={health.tone}>{health.label}</Badge>
      </div>
      <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden>
        <div className="h-full bg-primary" style={{ width: `${(progress(s) * 100).toFixed(1)}%` }} />
      </div>
      <div className="mt-1 flex justify-between text-xs tabular-nums text-muted-foreground">
        <span>{fmtClock(s.view_offset_ms)}</span>
        <span>{fmtClock(s.duration_ms)}</span>
      </div>
      <div className="mt-3 flex flex-wrap gap-1.5">
        <Badge tone="neutral">{streamLabel(s)}</Badge>
        <Badge tone="neutral">{placeLabel(s)}</Badge>
        {s.resolution ? <Badge tone="neutral">{s.resolution}</Badge> : null}
        {s.bitrate_kbps ? <Badge tone="neutral">{(s.bitrate_kbps / 1000).toFixed(1)} Mbit/s</Badge> : null}
        {s.buffering_events ? (
          <Badge tone="warning">
            buffered {s.buffering_events}× in 10 min
          </Badge>
        ) : null}
      </div>
      {details.length ? (
        <div className="mt-4 border-t border-border pt-3">
          <KV items={details} />
        </div>
      ) : null}
    </Card>
  );
}
