import { ShieldCheck, ShieldX } from "lucide-react";
import { useLive } from "@/api/events";
import { useGateway } from "@/api/hooks";
import { ErrorState, KV, Loading, Mono, Section } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtNumber, fmtRate, fmtTime } from "@/lib/format";
import { cn } from "@/lib/utils";

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
      {!vpn ? <Section title="Budgets" aside="this hour · today">
        <Card className="p-0">
          <Table>
            {/* Below sm each budget stacks as labelled lines, nothing dropped. */}
            <TableHeader className="max-sm:hidden">
              <TableRow>
                <TableHead>Budget</TableHead>
                <TableHead>Rate</TableHead>
                <TableHead>Used by</TableHead>
                <TableHead className="text-right">Hour</TableHead>
                <TableHead className="text-right">Today</TableHead>
                <TableHead className="hidden sm:table-cell">Cap</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(g?.budgets ?? []).map((b) => {
                // Both caps when both are set: each has its own bar, the hour's first.
                const caps = [b.hourly ? { used: b.used_hour ?? 0, of: b.hourly, per: "hour" } : null, b.daily ? { used: b.used_day ?? 0, of: b.daily, per: "day" } : null].filter((c) => c !== null);
                return (
                  <TableRow key={b.key} data-budget={b.key} className="max-sm:grid max-sm:grid-cols-[minmax(0,1fr)] max-sm:py-2 max-sm:[&>td]:h-auto max-sm:[&>td]:py-0.5">
                    <TableCell><Mono className="[overflow-wrap:anywhere]">{b.key}</Mono>{b.backoff?.active ? <Badge tone="warning" className="ml-2 max-sm:ml-0 max-sm:mt-1 max-sm:flex max-sm:w-fit">backed off</Badge> : null}</TableCell>
                    <TableCell className="sm:whitespace-nowrap"><span className="text-muted-foreground sm:hidden">Rate </span>{fmtRate(b.requests, b.per)}</TableCell>
                    <TableCell className="max-w-[18rem] text-[13px] text-muted-foreground max-sm:max-w-none"><span className="text-muted-foreground sm:hidden">Used by </span>{(b.used_by ?? []).join(", ") || "—"}</TableCell>
                    <TableCell className="tabular-nums sm:text-right" data-col="hour"><span className="text-muted-foreground sm:hidden">This hour </span>{fmtNumber(b.used_hour)}</TableCell>
                    <TableCell className="tabular-nums sm:text-right" data-col="day"><span className="text-muted-foreground sm:hidden">Today </span>{fmtNumber(b.used_day)}</TableCell>
                    <TableCell className="max-sm:flex max-sm:items-center max-sm:gap-1 sm:min-w-[140px]"><span className="text-muted-foreground sm:hidden">Caps </span>
                      {caps.length ? (
                        <div className="grid gap-1 max-sm:flex-1">{caps.map((cap) => {
                          const pct = Math.min(100, Math.round((cap.used / cap.of) * 100));
                          return <div key={cap.per} className="flex items-center gap-2" data-cap={cap.per}>
                            <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--bg)]">
                              <div className={cn("h-full rounded-full", pct >= 100 ? "bg-[var(--bad)]" : pct > 75 ? "bg-[var(--warn)]" : "bg-primary", BAR_W[Math.round(pct / 5)])} />
                            </div>
                            <span className="whitespace-nowrap text-xs text-muted-foreground">{fmtNumber(cap.of)}/{cap.per}</span>
                          </div>;
                        })}</div>
                      ) : (
                        <span className="text-xs text-muted-foreground">rate only</span>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      </Section> : null}
    </div>
  );
}

// Bar widths in 5% steps as classes: the CSP forbids style attributes in markup, and Tailwind needs literal names.
const BAR_W = ["w-[2%]", "w-[5%]", "w-[10%]", "w-[15%]", "w-[20%]", "w-[25%]", "w-[30%]", "w-[35%]", "w-[40%]", "w-[45%]", "w-[50%]", "w-[55%]", "w-[60%]", "w-[65%]", "w-[70%]", "w-[75%]", "w-[80%]", "w-[85%]", "w-[90%]", "w-[95%]", "w-full"];


export function VpnStatus() { return <Network vpn />; }
export function RequestLimitsStatus() { return <Network vpn={false} />; }
