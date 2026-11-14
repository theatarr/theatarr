// Admin › System › Logs: recent log lines from the service's in-memory ring (GET /logs), newest first, polled every 5 s,
// and the rotated log files to download (GET /logs/files). The service's own log level is a separate card at the end
// with its own Apply (PUT /logs/level): it changes what the running service writes, unlike the viewing filter.
import { useState } from "react";
import { API_BASE } from "@/api/client";
import { useLogFiles, useLogLevel, useLogs, useSetLogLevel } from "@/api/hooks";
import { Empty, ErrorState, Loading, Mono, PageHeader } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtBytes, fmtTime } from "@/lib/format";
import { cn } from "@/lib/utils";

const LEVELS = ["debug", "info", "warn", "error"];

export function Logs() {
  const [level, setLevel] = useState("info");
  const [component, setComponent] = useState("");
  const q = useLogs(level, component);
  const files = useLogFiles();

  const items = [...(q.data?.items ?? [])].reverse();
  return (
    <div>
      <PageHeader crumb={[{ label: "System", to: "/admin" }]} title="Logs" description="The last 2,000 lines since the service started. Secret-looking fields are redacted." />
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <span className="text-sm text-muted-foreground">Download logs:</span>
        {(files.data?.items ?? []).map((f) => (
          <a key={f.name} className="text-sm text-primary underline" href={API_BASE + "/logs/files/" + encodeURIComponent(f.name)} download={f.name}>
            {f.name} ({fmtBytes(f.size)})
          </a>
        ))}
      </div>
      {(q.data?.restartTotal ?? 0) > 0 ? <p role="status" className="mb-4 text-sm text-muted-foreground">Theatarr restarted. The viewer is showing logs from the new process.</p> : null}
      {(q.data?.skippedTotal ?? 0) > 0 ? <p role="status" className="mb-4 text-sm text-muted-foreground">
        {q.data!.skippedTotal} log lines skipped: the in-memory log buffer filled before they could be read. Older lines are in the log files above.
      </p> : null}
      {files.error ? <ErrorState error={files.error} /> : null}
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <span className="text-sm text-muted-foreground">Show lines from:</span>
        <div className="flex gap-1">
          {LEVELS.map((l) => (
            <Button key={l} size="xs" variant={l === level ? "secondary" : "ghost"} onClick={() => setLevel(l)}>{l}</Button>
          ))}
        </div>
        <select
          aria-label="Component"
          value={component}
          onChange={(e) => setComponent(e.target.value)}
          className="h-8 rounded-md border border-input bg-field px-2 text-sm"
        >
          <option value="">All components</option>
          {(q.data?.components ?? []).map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
      </div>
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : items.length === 0 ? <Card><Empty>No lines at this level.</Empty></Card> : (
        <Card className="p-0">
          <ul className="divide-y divide-border font-mono text-[12.5px]">
            {items.map((e) => (
              <li key={e.seq} className="px-4 py-2">
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                  <span className="whitespace-nowrap text-muted-foreground">{fmtTime(e.time)}</span>
                  <Badge tone={e.level === "ERROR" ? "negative" : e.level === "WARN" ? "warning" : e.level === "DEBUG" ? "neutral" : "info"}>{e.level}</Badge>
                  {e.component ? <span className="text-primary">{e.component}</span> : null}
                  <span className="break-words font-sans text-[13px] text-foreground">{e.msg}</span>
                </div>
                {e.attrs && Object.keys(e.attrs).length > 0 ? (
                  <div className={cn("mt-1 break-words text-muted-foreground")}>
                    {/* A key=value pair breaks between pairs; only a long value (a URL) breaks inside itself. */}
                    {Object.entries(e.attrs).map(([k, v]) => <span key={k} className="mr-3 inline-block max-w-full [overflow-wrap:anywhere]"><Mono className="whitespace-nowrap text-[var(--ink-3)]">{k}=</Mono>{v}</span>)}
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        </Card>
      )}
      <ServiceLevel />
    </div>
  );
}

/** The level the running service writes at: a setting of the service, apart from the viewer's filter above, applied
 *  only with Apply. It lasts until a restart, which brings back the configured default. */
function ServiceLevel() {
  const runtime = useLogLevel();
  const changeLevel = useSetLogLevel();
  const [draft, setDraft] = useState<string>();
  const current = runtime.data?.level ?? "";
  const value = draft ?? current;
  return (
    <Card id="log-level" className="mt-6 scroll-mt-[calc(var(--top)+1rem)]" data-testid="log-level">
      <h2 className="text-[15px] font-semibold text-[var(--ink)]">Service log level</h2>
      <p className="mt-1 text-sm text-muted-foreground">What the running service writes to its logs, for every reader. It applies at once with Apply and goes back to the configured default on restart. The filter above only changes what this page shows.</p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <select aria-label="Service log level" value={value} disabled={!runtime.data || changeLevel.isPending} onChange={(e) => setDraft(e.target.value)}
          className="h-8 rounded-md border border-input bg-field px-2 text-sm">
          {!runtime.data ? <option value="">Loading…</option> : null}
          {LEVELS.map((l) => <option key={l} value={l.toUpperCase()}>{l}</option>)}
        </select>
        <Button size="sm" disabled={!runtime.data || changeLevel.isPending || value === current} onClick={() => changeLevel.mutate(value, { onSuccess: () => setDraft(undefined) })}>Apply</Button>
        {runtime.data ? <span className="text-sm text-muted-foreground" data-testid="log-level-now">Now: {current.toLowerCase()}</span> : null}
      </div>
      {runtime.error ? <ErrorState error={runtime.error} /> : null}
      {changeLevel.error ? <ErrorState error={changeLevel.error} /> : null}
    </Card>
  );
}
