// Settings › Plex & playback: the playlists Theatarr keeps in Plex (GET /anime/playlists), under the settings and
// folded by default with its count, so a long list (90 playlists on a big anime library) does not make the page
// several screens tall (verified D41).
import { usePlaylists } from "@/api/hooks";
import { Empty, ErrorState, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtTime, plural } from "@/lib/format";

const MODE: Record<string, string> = { applied: "Applied", failed: "Failed" };

export function PlexPlaylists() {
  const pl = usePlaylists();
  const items = pl.data?.items ?? [];
  const empty = items.filter((p) => !p.items).length;
  return (
    <details className="mb-4 rounded-[8px] border border-[var(--line)] bg-card" data-testid="plex-playlists">
      <summary className="cursor-pointer px-4 py-3 text-lg font-semibold">
        Playlists Theatarr keeps{pl.data ? <span className="ml-2 text-sm font-normal text-muted-foreground">{plural(items.length, "playlist")}{empty ? `, ${empty} empty` : ""}</span> : null}
      </summary>
      <div className="border-t border-[var(--line)]">
        {pl.isPending ? <Loading /> : pl.error ? <ErrorState error={pl.error} /> : items.length === 0 ? (
          <Card className="border-0"><Empty>No playlists yet.</Empty></Card>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Playlist</TableHead>
                <TableHead>Mode</TableHead>
                <TableHead className="text-right">Items</TableHead>
                <TableHead className="hidden md:table-cell">Synced</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((p, i) => (
                <TableRow key={i}>
                  <TableCell>{p.name}<div className="text-xs text-muted-foreground"><Mono>{p.franchise}</Mono> · {p.order}{p.route ? ` · ${p.route}` : ""}</div></TableCell>
                  <TableCell><Badge tone={p.mode === "applied" ? "positive" : p.mode === "failed" ? "negative" : "neutral"} title={p.mode}>{MODE[p.mode ?? ""] ?? p.mode}</Badge></TableCell>
                  <TableCell className="text-right tabular-nums">{p.items}</TableCell>
                  <TableCell className="hidden md:table-cell">{fmtTime(p.synced_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </details>
  );
}
