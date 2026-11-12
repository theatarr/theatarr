// System › Status (docs/design/ui.md): what needs attention first (VPN egress, failing health checks, failed jobs,
// back-offs, downloads stuck or waiting too long), each with its effect and the next step; then one summary strip
// each for downloads and jobs; then diagnostics (readiness, schema, version and every health check). The numbers are
// the API's (/readyz, /status, /health, /jobs, /gateway, /activity); job counts and the gateway update live.
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2 } from "lucide-react";
import { type ReactNode, useState } from "react";
import { Link } from "react-router";
import { useReviewCounts } from "@/api/reviews";
import { useSettings, useSettingsSections } from "@/api/settings";
import { useSetup, setupUnfinished } from "@/api/setup";
import { useAppBackups } from "@/api/backups";
import { SETTINGS_PAGES } from "@/lib/settings-map";
import { useActivityNow } from "@/api/activity";
import { useDanglingLinks } from "@/api/dangling";
import { type Gateway, useGateway, useHealth, useStatus } from "@/api/hooks";
import { useJobCounts, useJobsFiltered } from "@/api/jobs";
import { qk } from "@/api/query-keys";
import { PRODUCT_NAME } from "@/brand";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { Empty, ErrorState, Loading, Mono, PageHeader, Section, Stat, StatGrid, Strip } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { needsAttention, showCounts, stageLabel, timing } from "@/lib/activity";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";
import { danglingNext, healthAttention, healthBadgeTone, healthEffect, healthLink, healthStatTone } from "@/lib/health";
import { attentionCount, jobEffect, jobName, type Job } from "@/lib/jobs";
import { JobActions } from "./jobs";

type Ready = {
  status?: string;
  version?: string;
  error?: string;
  schema?: { current?: number; target?: number };
  egress?: Gateway["egress"];
};

function useReady() {
  return useQuery({
    queryKey: qk.readyz,
    queryFn: async () => {
      const r = await fetch("/readyz", { credentials: "same-origin" });
      return (await r.json()) as Ready;
    },
    refetchInterval: 30_000,
  });
}

type Attention = {
  key: string;
  tone: "bad" | "warn";
  title: ReactNode;
  effect: ReactNode;
  next: string;
  to: string;
  open?: string;
  job?: Job;
  grouped?: boolean;
};

/** How many stuck downloads Needs attention names one by one before it sums up the rest. */
const DOWNLOADS_LISTED = 3;

export function Overview() {
  const reviews = useReviewCounts();
  const setup = useSetup();
  const ready = useReady();
  const st = useStatus();
  const failed = useJobsFiltered({ attention: true, limit: 50 });
  const gw = useGateway();
  const hc = useHealth();
  const now = useActivityNow();
  const counts = useJobCounts(failed.data);
  const header = <PageHeader crumb={[{ label: "System" }]} title="Status" actions={setupUnfinished(setup.data) ? <Link to="/admin/setup">Setup checklist</Link> : undefined} description="What needs attention first, then how downloads and background work are doing." />;
  if (ready.isPending || st.isPending) return <div>{header}<Loading /></div>;
  if (st.error) return <div>{header}<ErrorState error={st.error} /></div>;
  const r = ready.data ?? {};
  const egress = gw.data?.egress ?? r.egress;
  const vpn = egress?.vpn_ok;
  const vpnOptional = egress?.mode === "off" || egress?.mode === "monitor-only";
  const nFailed = attentionCount(counts);
  const t = Date.now();
  const downloads = now.data?.now ?? [];
  const stuck = downloads.filter((d) => needsAttention(d, t));
  const dl = showCounts(downloads, t);

  const items: Attention[] = [];
  for (const name of reviews.failures) items.push({ key: `reviews-${name}`, tone: "warn", title: `Reviews: couldn't load ${name}`, effect: "Waiting decisions are unknown.", next: "Open Reviews", to: "/admin/reviews" });
  if (reviews.total > 0 || reviews.decisionsMore) items.push({ key: "reviews", tone: "warn", title: `${reviews.decisionsMore ? reviews.total ? `${reviews.total}+` : "?" : reviews.total} decisions waiting in Reviews`, effect: "Updates and matches that need your decision.", next: "Review the waiting items", to: "/admin/reviews" });
  if (vpn === false || vpnOptional) {
    items.push({ key: "vpn", tone: vpnOptional ? "warn" : "bad", title: egress?.mode === "off" ? "VPN guard off" : egress?.mode === "monitor-only" ? "VPN monitor-only" : "VPN egress not confirmed", effect: vpnOptional ? "Requests do not require a VPN check." : `Requests requiring VPN (${(egress?.require_vpn_for ?? []).join(", ")}) wait until the VPN check passes.`, next: "Check the egress paths", to: "/admin/settings/indexers/vpn" });
  }
  for (const c of healthAttention(hc.data?.checks)) {
    const e = healthEffect(c.source);
    items.push({
      key: `check-${c.source}`,
      tone: c.type === "error" ? "bad" : "warn",
      title: c.name ?? c.source ?? "Health check",
      effect: e.effect ? <>{e.effect} <span className="text-[var(--ink-3)]">{c.message}</span></> : c.message,
      next: e.next,
      to: healthLink(c.source),
    });
  }
  const failures = new Map<string, Job[]>();
  for (const j of failed.data?.items ?? []) failures.set(j.kind ?? "", [...(failures.get(j.kind ?? "") ?? []), j]);
  for (const group of failures.values()) {
    // The attention endpoint orders failures by finish time, newest first.
    const j = group[0];
    const grouped = group.length > 1;
    items.push({
      key: `job-${j.id}`,
      tone: "bad",
      title: <>{jobName(j.kind)} failed{grouped ? ` ${group.length} times` : ""} <Mono className="ml-1 text-[11.5px] text-muted-foreground">{j.kind} #{j.id}</Mono></>,
      effect: <>{jobEffect(j.kind) ? `${jobEffect(j.kind)} ` : ""}<span className="text-[var(--ink-3)]">{j.last_error || "no error recorded"}</span></>,
      next: grouped ? "Retry the newest task, or see all failures" : "Retry it, or dismiss it once handled",
      to: grouped ? `/admin/system/tasks?kind=${encodeURIComponent(j.kind ?? "")}` : "/admin/system/tasks",
      open: grouped ? "See all" : undefined,
      grouped,
      job: j,
    });
  }
  for (const b of (gw.data?.backoffs ?? []).filter((x) => x.active)) {
    const subjectKind = b.subject?.split(":")[0];
    items.push({
      key: `backoff-${b.subject}`,
      tone: "warn",
      title: `${b.subject} backed off`,
      effect: `${b.reason || "Repeated failures"}; requests to it pause until ${fmtTime(b.until)}.`,
      next: "It resumes on its own; check it if it keeps happening",
      to: subjectKind === "provider" ? "/admin/settings/downloads" : subjectKind === "indexer" ? "/admin/settings/indexers" : "/admin/advanced/limits",
    });
  }
  for (const d of stuck.slice(0, DOWNLOADS_LISTED)) {
    const w = timing(d, t);
    items.push({
      key: `download-${d.id}`,
      tone: d.state === "failed" || d.steps?.some((s) => s.state === "failed") ? "bad" : "warn",
      title: <>{d.title.name} <span className="font-normal text-muted-foreground">· {stageLabel(d)}</span></>,
      effect: `Not in the library yet${w.waited ? `; waiting ${w.waited}` : w.updated ? `; last update ${w.updated}` : ""}${w.retry ? `; next try ${w.retry}` : ""}.`,
      next: "Inspect the download",
      to: "/activity?show=attention",
      open: "Inspect",
    });
  }
  if (stuck.length > DOWNLOADS_LISTED) {
    const more = stuck.length - DOWNLOADS_LISTED;
    items.push({ key: "downloads-more", tone: "warn", title: `${more} more download${more === 1 ? "" : "s"} need a look`, effect: "Stuck or waiting longer than usual.", next: "See them in Activity", to: "/activity?show=attention" });
  }
  // A source that failed to load is said so: its alerts are unknown, never an all-clear.
  const sources = [
    { key: "jobs", name: "failed jobs", q: failed, to: "/admin/system/tasks" },
    { key: "health", name: "health checks", q: hc, to: "/admin" },
    { key: "gateway", name: "the gateway (VPN egress and back-offs)", q: gw, to: "/admin/advanced/limits" },
    { key: "downloads", name: "downloads", q: now, to: "/activity" },
  ];
  for (const { key, name, q, to } of sources.filter((x) => x.q.error)) {
    items.push({ key: `unavailable-${key}`, tone: "warn", title: `Could not load ${name}`, effect: <>Alerts from it are unknown. <span className="text-[var(--ink-3)]">{q.error instanceof Error ? q.error.message : String(q.error)}</span></>, next: "Reload the page; check the server if it keeps failing", to });
  }
  const checking = reviews.isPending || sources.some((x) => x.q.isPending);
  items.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === "bad" ? -1 : 1));
  const checks = hc.data?.checks ?? [];
  const schemaOK = st.data?.schema?.current === st.data?.schema?.target;

  return (
    <div>
      {header}
      <Readiness />
      <Section title={`Needs attention${items.length > 0 ? ` (${items.length})` : ""}`} className="mt-0">
        <Card className="p-0">
          {items.length === 0 && checking ? (
            <Loading />
          ) : items.length === 0 ? (
            <Empty icon={CheckCircle2}>Nothing needs attention.</Empty>
          ) : (
            <ul className="divide-y divide-border" data-testid="attention">
              {items.map((a) => (
                <li key={a.key} className="flex flex-wrap items-start gap-3 px-4 py-3 sm:flex-nowrap" data-testid={a.job ? `attention-job-${a.job.id}` : `attention-${a.key}`}>
                  <Badge tone={a.tone === "bad" ? "negative" : "warning"}>!</Badge>
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">{a.title}</div>
                    <div className="break-words text-[13px] text-muted-foreground">{a.effect}</div>
                    <div className="mt-0.5 text-[12.5px] text-[var(--accent-ink)]">Next: {a.next}</div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    {a.job ? <JobActions job={a.job} newest={a.grouped} /> : null}
                    <Link to={a.to} className="text-sm text-primary hover:underline">{a.open ?? "Open"}</Link>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </Section>

      <Section title="Downloads" aside={<Link to="/activity" className="text-primary hover:underline">Open queue</Link>}>
        {now.error ? <ErrorState error={now.error} /> : (
          <Strip
            items={[
              { label: "getting ready", value: fmtNumber(dl.all), testid: "dl-all" },
              { label: "downloading", value: fmtNumber(dl.downloading), testid: "dl-downloading" },
              { label: "waiting", value: fmtNumber(dl.waiting), testid: "dl-waiting" },
              { label: "need attention", value: fmtNumber(dl.attention), tone: dl.attention > 0 ? "warn" : undefined, testid: "dl-attention" },
            ]}
          />
        )}
      </Section>

      <Section title="Tasks" aside={<Link to="/admin/system/tasks" className="text-primary hover:underline">Open tasks</Link>}>
        <Strip
          items={[
            { label: "running", value: fmtNumber(counts.running ?? 0), testid: "ov-jobs-running" },
            { label: "queued", value: fmtNumber(counts.queued ?? 0), testid: "ov-jobs-queued" },
            { label: "need attention", value: fmtNumber(nFailed), tone: nFailed > 0 ? "bad" : undefined, testid: "health-failed" },
          ]}
          action={<span className="text-xs text-muted-foreground">Finished and resolved tasks are folded on Tasks</span>}
        />
      </Section>

      <Section title="Libraries">
        <DownloadsOffNotice off={globalDownloadsOff((st.data?.versions ?? []).map((v) => v.grab_state))} canManage className="mb-2" />
        <Card><div className="space-y-2">{(st.data?.versions ?? []).map((v) => <div key={v.key} className="flex flex-wrap items-start gap-2"><Link className="text-sm font-medium hover:underline" to={`/admin/settings/libraries?edit=${encodeURIComponent(v.key)}`}>{v.key}</Link><VersionStatus state={v.grab_state} showMode settingsKey={v.key} canManage /></div>)}</div></Card>
      </Section>

      {!setupUnfinished(setup.data) ? <p className="mt-6"><Link className="text-primary underline" to="/admin/setup">Setup checklist</Link></p> : null}
      <Section title="Diagnostics">
        <StatGrid>
          <Stat testid="health-status" value={<span className="flex items-center gap-2">{r.status === "ready" ? <CheckCircle2 className="size-5 text-[var(--ok)]" /> : <AlertTriangle className="size-5 text-[var(--bad)]" />}{r.status ?? "unknown"}</span>} label="readiness (/readyz)" />
          <Stat value={egress?.mode === "off" ? "Off" : egress?.mode === "monitor-only" ? "Monitor-only" : vpn === undefined ? "—" : vpn ? "Up" : "Down"} tone={vpnOptional ? "warn" : vpn === false ? "bad" : vpn ? "ok" : undefined} label="VPN egress" />
          <Stat testid="health-checks" value={hc.data?.status ?? "—"} tone={healthStatTone(hc.data?.status)} label="health checks" />
          <Stat value={`${st.data?.schema?.current ?? "—"} / ${st.data?.schema?.target ?? "—"}`} tone={schemaOK ? undefined : "warn"} label="schema version" />
          <Stat value={<Mono className="block truncate text-lg">{(st.data?.version ?? "—").split(" ")[0]}</Mono>} label={`${PRODUCT_NAME} version`} />
        </StatGrid>
        {r.error ? <div className="mt-4"><ErrorState error={r.error} /></div> : null}
      </Section>

      <Section title="Checks" aside={checks.length > 0 ? `checked ${fmtAgo(checks[0].checked_at)}` : undefined}>
        <Card className="p-0">
          {hc.error ? (
            <div className="p-4"><ErrorState error={hc.error} /></div>
          ) : checks.length === 0 ? (
            <Empty>{hc.isPending ? "Checking…" : "No health checks."}</Empty>
          ) : (
            <Table density="dense">
              <TableBody>
                {checks.map((c) => (
                  <TableRow key={c.source} data-testid={`check-${c.source}`}>
                    <TableCell className="w-0 whitespace-nowrap"><Badge tone={healthBadgeTone(c.type)}>{c.type}</Badge></TableCell>
                    <TableCell className="font-medium sm:whitespace-nowrap">{c.name}</TableCell>
                    <TableCell className="text-[13px] text-muted-foreground [overflow-wrap:anywhere]">
                      {c.message}
                      {c.source === "DanglingLinksCheck" ? <BrokenLinks count={Number((c.detail as { dangling?: number } | undefined)?.dangling ?? 0)} /> : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      </Section>
    </div>
  );
}

/**
 * The broken links behind the dangling-links check: a list that loads when opened (GET /health/dangling) and only
 * shows; nothing is scanned or repaired from here.
 */
function BrokenLinks({ count }: { count: number }) {
  const [open, setOpen] = useState(false);
  const q = useDanglingLinks(open);
  if (count <= 0) return null;
  return (
    <div className="mt-1.5" data-testid="broken-links">
      <button type="button" className="text-primary hover:underline" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? "Hide the broken links" : `Show the ${fmtNumber(count)} broken link${count === 1 ? "" : "s"}`}
      </button>
      {!open ? null : q.error ? (
        <div className="mt-2"><ErrorState error={q.error} /></div>
      ) : !q.data ? (
        <Loading />
      ) : q.data.items.length === 0 ? (
        <p className="mt-2">The latest report lists no broken link.</p>
      ) : (
        <>
          <ul className="mt-2 divide-y divide-[var(--line)] rounded-[8px] border border-[var(--line)]">
            {q.data.items.map((d) => (
              <li key={d.path} className="px-3 py-2" data-testid="broken-link">
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-[var(--ink)]">
                  {d.title_id ? <Link className="font-medium hover:underline" to={`/title/${d.title_id}`}>{d.title}</Link> : <span className="font-medium">{d.folder || "Unknown"}</span>}
                  {d.label ? <Badge>{d.label}</Badge> : null}
                  <span className="text-muted-foreground">since {fmtAgo(d.first_seen)}</span>
                  {d.cause === "no_video" ? <span className="text-muted-foreground">torrent has no video upstream</span> : null}
                </div>
                <div>{danglingNext(d.link_state)}</div>
                <Mono className="block text-[11.5px] [overflow-wrap:anywhere]">{d.path}</Mono>
              </li>
            ))}
          </ul>
          {q.data.truncated ? <p className="mt-2">Showing the {fmtNumber(q.data.items.length)} oldest; <Mono>theatarr library dangling</Mono> lists all.</p> : null}
        </>
      )}
    </div>
  );
}

export function Readiness() {
  const settings = useSettings();
  const backups = useAppBackups();
  const switches = settings.data?.switches;
  // Send notifications is the notify section's enabled field (default off).
  const sections = useSettingsSections().data;
  const notify = sections ? sections.sections.find(s => s.key === "notify")?.config?.enabled === true : undefined;
  const latest = [...(backups.data?.sets ?? [])].filter(b => b.complete).sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  return <Section title="Readiness" className="mt-0"><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
    {([
      ["Pause", "libraries", switches?.paused],
      ["Notifications", "notifications", notify],
    ] as const).map(([label, page, on]) => <Link key={page} to={SETTINGS_PAGES.find(p => p.id === page)!.route}><Card><h2>{label}</h2><strong>{on === undefined ? "Unknown" : on ? "ON" : "OFF"}</strong></Card></Link>)}
    <Link to="/admin/system/backup"><Card><h2>Backup</h2><span>{backups.error ? "Could not load backups" : backups.isPending ? "Loading…" : latest ? fmtTime(latest.created_at) : "No backup yet"}</span></Card></Link>
  </div></Section>;
}
