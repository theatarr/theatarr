import { ShieldCheck, ShieldX } from "lucide-react";
import { useLive } from "@/api/events";
import { useGateway } from "@/api/hooks";
import { ErrorState, KV, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtTime } from "@/lib/format";

function Network({ vpn }: { vpn: boolean }) {
  const gw = useGateway();
  const live = useLive();
  if (gw.isPending) return <Loading />;
  if (gw.error) return <ErrorState error={gw.error} />;
  const g = gw.data;
  const eg = g?.egress;
  return (
    <div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {vpn ? <Card>
          <div className="flex items-center justify-between gap-2">
            <h3 className="font-semibold">VPN egress</h3>
            <Badge tone={eg?.mode === "off" || eg?.mode === "monitor-only" ? "warning" : eg?.vpn_ok ? "positive" : "negative"} data-testid="vpn-badge">
              {eg?.vpn_ok ? <ShieldCheck /> : <ShieldX />}
              {eg?.mode === "off" ? "VPN guard off" : eg?.mode === "monitor-only" ? "Monitor-only" : eg?.vpn_ok ? "VPN up" : "not confirmed"}
            </Badge>
          </div>
          <div className="mt-3 space-y-3">
            {(eg?.paths ?? []).map((p) => (
              <KV
                key={p.path}
                items={[
                  ["Path", <Mono key="p">{p.path}</Mono>],
                  ["Exit IP", <Mono key="ip">{p.ip || "—"}</Mono>],
                  ["Checked", `${fmtTime(p.checked_at)} (${p.age_s ?? "?"} s ago)${p.stale ? " · stale" : ""}`],
                  ...(p.error ? ([["Error", <span key="e" className="text-[var(--bad-ink)]">{p.error}</span>]] as [string, React.ReactNode][]) : []),
                ]}
              />
            ))}
            <KV items={[["Required for", (eg?.require_vpn_for ?? []).join(", ") || "—"]]} />
          </div>
          <p className="mt-3 text-xs text-muted-foreground">{live.connected ? "Live: updates as the gateway changes." : "Not live: refresh to update."}</p>
        </Card> : null}
        {!vpn ? <Card>
          <h3 className="font-semibold">Back-offs</h3>
          {(g?.backoffs ?? []).length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">None.</p>
          ) : (
            <Table density="dense" wrapperClassName="mt-3">
              {/* Below sm each back-off stacks as labelled lines, the reason included. */}
              <TableHeader className="max-sm:hidden">
                <TableRow>
                  <TableHead>Subject</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Failures</TableHead>
                  <TableHead className="hidden sm:table-cell">Reason</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(g?.backoffs ?? []).map((b) => (
                  <TableRow key={b.subject} className="max-sm:grid max-sm:grid-cols-[minmax(0,1fr)] max-sm:py-2 max-sm:[&>td]:h-auto max-sm:[&>td]:py-0.5">
                    <TableCell><Mono className="[overflow-wrap:anywhere]">{b.subject}</Mono></TableCell>
                    <TableCell>{b.active ? <Badge tone="warning">until {fmtTime(b.until)}</Badge> : <span className="text-muted-foreground">over</span>}</TableCell>
                    <TableCell><span className="text-muted-foreground sm:hidden">Failures </span>{b.failures}</TableCell>
                    <TableCell className="max-w-[22rem] [overflow-wrap:anywhere] max-sm:max-w-none"><span className="text-muted-foreground sm:hidden">Reason </span>{b.reason || "—"}<div className="text-xs text-muted-foreground">{fmtAgo(b.updated_at)}</div></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card> : null}
      </div>
    </div>
  );
}



export function VpnStatus() { return <Network vpn />; }
export function RequestLimitsStatus() { return <Network vpn={false} />; }
