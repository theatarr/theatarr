// System › Status (docs/design/ui.md): what needs attention first, by consequence (VPN egress and failing health
// checks, failed jobs and back-offs, held indexer definitions, then downloads, collapsed into one line when more than one: the queue's
// own rule, internal/activity NativeQueueFailureSQL), each with its effect and the next step; then one summary strip
// each for downloads and jobs; then diagnostics (readiness, schema, version and every health check). The numbers are
// the API's (/readyz, /status, /health, /jobs, /gateway, /activity); job counts and the gateway update live.
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2 } from "lucide-react";
import { type ReactNode, useState } from "react";
import { Link } from "react-router";
import { useCardigannHolds } from "@/api/cardigann";
import { useRequestSummary } from "@/api/requests";
import { useSettings, useSettingsSections } from "@/api/settings";
import { useSetup, setupUnfinished } from "@/api/setup";
import { useAppBackups } from "@/api/backups";
import { SETTINGS_PAGES } from "@/lib/settings-map";
import { type ActivityDownload, useActivityNow } from "@/api/activity";
import { useDanglingLinks } from "@/api/dangling";
import { type Gateway, useGateway, useHealth, useStatus } from "@/api/hooks";
import { useJobCounts, useJobsFiltered, useTasks } from "@/api/jobs";
import { qk } from "@/api/query-keys";
import { PRODUCT_NAME } from "@/brand";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { Empty, ErrorState, Loading, Mono, PageHeader, Section, Stat, StatGrid, Strip, VersionTag } from "@/components/page";
import { episodesWords } from "@/components/activity/row-bits";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { downloadHref, needsAttention, showCounts, stageLabel, timing } from "@/lib/activity";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";
import { accountNotice, danglingNext, healthAttention, healthBadgeTone, healthEffect, healthLink, healthStatTone } from "@/lib/health";
import { attentionCount, jobEffect, jobName, type Job } from "@/lib/jobs";
import { useProviderCode } from "@/lib/library-names";
import { JobActions } from "./jobs";
import { backoffWords } from "@/lib/router-words";
import { cn } from "@/lib/utils";

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

/** Needs attention's order by consequence: VPN and health checks (accounts, mount, backups) first, then tasks,
 * back-offs and sources that did not load, then held definitions, and single downloads last. */
function rank(key: string): number {
  if (key === "vpn" || key.startsWith("check-")) return 0;
  if (key.startsWith("job-") || key.startsWith("backoff-") || key.startsWith("unavailable-")) return 1;
  if (key === "holds") return 2;
  return 3;
}

/** What the downloads needing a look are, in words: "2 failed, 1 waiting longer than usual". */
export function downloadsWords(ds: ActivityDownload[]): string {
  const failed = ds.filter((d) => d.state === "failed" || d.steps?.some((s) => s.state === "failed") || d.import_state === "rejected" || d.import_state === "failed").length;
  const partly = ds.filter((d) => d.import_state === "partial").length;
  const waiting = ds.length - failed - partly;
  return [failed ? `${fmtNumber(failed)} failed` : "", partly ? `${fmtNumber(partly)} partly linked` : "", waiting ? `${fmtNumber(waiting)} stuck or waiting longer than usual` : ""].filter(Boolean).join(", ");
}

export function Overview() {
  // Readiness reads its own answers (settings sections, Theatarr backups) only once Details is opened.
  const [details, setDetails] = useState(false);
  const holdsQ = useCardigannHolds();
  const holds = holdsQ.data?.length ?? 0;
  const summary = useRequestSummary();
  const problems = summary.data?.problems ?? 0;
  const setup = useSetup();
  const ready = useReady();
  const st = useStatus();
  const failed = useJobsFiltered({ attention: true, limit: 50 });
  const tasks = useTasks();
  const gw = useGateway();
  const hc = useHealth();
  const now = useActivityNow();
  const counts = useJobCounts(failed.data);
  const providerCode = useProviderCode();
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
  // In hand: neither finished (linked, failed) nor never started; one held for review (the library refused it) or
  // whose link failed counts under need attention only.
  const inHand = downloads.filter((d) => !d.not_started && d.state !== "failed" && d.state !== "imported" &&
    d.import_state !== "rejected" && d.import_state !== "failed").length;

  const items: Attention[] = [];
  if (holds > 0) items.push({ key: "holds", tone: "warn", title: `${fmtNumber(holds)} indexer ${holds === 1 ? "definition update" : "definition updates"} waiting`, effect: "An updated definition is used once you approve it.", next: "Review them in Settings › Indexers", to: "/admin/settings/indexers" });
  if (problems > 0) items.push({ key: "problems", tone: "warn", title: `${fmtNumber(problems)} problem ${problems === 1 ? "report" : "reports"}`, effect: "Someone says a movie or an episode does not play right.", next: "Open Requests › Problems", to: "/requests/problems" });
  if (vpn === false || vpnOptional) {
    items.push({ key: "vpn", tone: vpnOptional ? "warn" : "bad", title: egress?.mode === "off" ? "VPN guard off" : egress?.mode === "monitor-only" ? "VPN monitor-only" : "VPN egress not confirmed", effect: vpnOptional ? "Requests do not require a VPN check." : `Requests requiring VPN (${(egress?.require_vpn_for ?? []).join(", ")}) wait until the VPN check passes.`, next: "Check the egress paths", to: "/admin/settings/indexers/vpn" });
  }
  for (const c of healthAttention(hc.data?.checks)) {
    const e = healthEffect(c.source);
    // The debrid accounts by provider name, with what to do (renew, or check the key).
    const acct = c.source === "ProviderAccountCheck" ? accountNotice(c, providerCode) : null;
    items.push({
      key: `check-${c.source}`,
      tone: c.type === "error" ? "bad" : "warn",
      title: c.name ?? c.source ?? "Health check",
      effect: acct ? acct.effect : e.effect ? <>{e.effect} <span className="text-[var(--ink-3)]">{c.message}</span></> : c.message,
      next: acct ? acct.next : e.next,
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
  // A scheduled task whose newest run failed, until a run works.
  for (const t of (tasks.data?.items ?? []).filter((x) => x.last_error)) {
    items.push({
      key: `task-${t.key}`,
      tone: "bad",
      // The task by its name; its key only as a tooltip.
      title: <span title={t.key}>{t.name} failed</span>,
      effect: <span className="text-[var(--ink-3)]">{t.last_error}</span>,
      next: "Run it now, or wait for its next run",
      to: "/admin/system/tasks",
    });
  }
  for (const b of (gw.data?.backoffs ?? []).filter((x) => x.active)) {
    const subjectKind = b.subject?.split(":")[0];
    items.push({
      key: `backoff-${b.subject}`,
      tone: "warn",
      title: backoffWords(b.subject),
      effect: `${b.reason || "Repeated failures"}; requests to it pause until ${fmtTime(b.until)}.`,
      next: "It resumes on its own; check it if it keeps happening",
      to: subjectKind === "provider" ? "/admin/settings/downloads" : subjectKind === "indexer" ? "/admin/settings/indexers" : "/admin/advanced/limits",
    });
  }
  // One download is named; more collapse into one line that opens them in Activity.
  for (const d of stuck.length === 1 ? stuck : []) {
    const w = timing(d, t);
    const failedDl = d.state === "failed" || d.steps?.some((s) => s.state === "failed");
    const eps = episodesWords(d.episodes);
    // A failure says its error; otherwise whether some files are in (partly linked) or none yet (whether the title
    // already has one, an upgrade, is not said here).
    const what = failedDl ? d.reason || d.error || "No error recorded" : d.import_state === "partial" ? "Some files linked" : "No file from this download yet";
    items.push({
      key: `download-${d.id}`,
      tone: failedDl ? "bad" : "warn",
      title: <>{d.title.id ? <Link className="hover:underline" to={`/title/${d.title.id}`}>{d.title.name}</Link> : d.title.name}{eps ? ` ${eps}` : ""} {d.version ? <VersionTag k={d.version} className="ml-1 align-middle" /> : null} <span className="font-normal text-muted-foreground">· {stageLabel(d)}</span></>,
      effect: <>{what}{w.waited ? `; waiting ${w.waited}` : w.updated ? `; last update ${w.updated}` : ""}{w.retry ? `; next try ${w.retry}` : ""}.{d.release ? <span className="block text-[var(--ink-3)] [overflow-wrap:anywhere]" data-testid="attention-release">Release: {d.release}</span> : null}</>,
      next: "Inspect the download",
      to: downloadHref(d.id),
      open: "Inspect",
    });
  }
  if (stuck.length > 1) {
    const words = downloadsWords(stuck);
    items.push({ key: "downloads", tone: words.includes("failed") ? "bad" : "warn", title: `${fmtNumber(stuck.length)} downloads need a look`, effect: `${words.charAt(0).toUpperCase()}${words.slice(1)}.`, next: "See them in Activity", to: "/activity?show=attention", open: "Open queue" });
  }
  // A source that failed to load is said so: its alerts are unknown, never an all-clear.
  const sources = [
    { key: "jobs", name: "failed jobs", q: failed, to: "/admin/system/tasks" },
    { key: "health", name: "health checks", q: hc, to: "/admin" },
    { key: "gateway", name: "the gateway (VPN egress and back-offs)", q: gw, to: "/admin/advanced/limits" },
    { key: "downloads", name: "downloads", q: now, to: "/activity" },
    { key: "holds", name: "indexer definition updates", q: holdsQ, to: "/admin/settings/indexers" },
    { key: "problems", name: "problem reports", q: summary, to: "/requests/problems" },
  ];
  for (const { key, name, q, to } of sources.filter((x) => x.q.error)) {
    items.push({ key: `unavailable-${key}`, tone: "warn", title: `Could not load ${name}`, effect: <>Alerts from it are unknown. <span className="text-[var(--ink-3)]">{q.error instanceof Error ? q.error.message : String(q.error)}</span></>, next: "Reload the page; check the server if it keeps failing", to });
  }
  const checking = sources.some((x) => x.q.isPending);
  items.sort((a, b) => rank(a.key) - rank(b.key) || (a.tone === b.tone ? 0 : a.tone === "bad" ? -1 : 1));
  const checks = hc.data?.checks ?? [];
  const schemaOK = st.data?.schema?.current === st.data?.schema?.target;

  return (
    <div>
      {header}
      <Section className="mt-0" title={`Needs attention${items.length > 0 ? ` (${items.length})` : ""}`}>
        <Card className="p-0">
          {items.length === 0 && checking ? (
            <Loading />
          ) : items.length === 0 ? (
            <Empty icon={CheckCircle2}>Nothing needs attention.</Empty>
          ) : (
            <ul className="divide-y divide-border" data-testid="attention">
              {items.map((a) => (
                // A failed job's Retry, Dismiss and Open take a row of their own under its text on a phone, so the
                // error is not squeezed beside three buttons; from sm up they sit beside it.
                <li key={a.key} className={cn("items-start gap-3 px-4 py-3", a.job ? "grid grid-cols-[auto_minmax(0,1fr)] sm:flex" : "flex flex-wrap sm:flex-nowrap")} data-testid={a.job ? `attention-job-${a.job.id}` : `attention-${a.key}`}>
                  <Badge tone={a.tone === "bad" ? "negative" : "warning"}>!</Badge>
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">{a.title}</div>
                    <div className="break-words text-[13px] text-muted-foreground">{a.effect}</div>
                    <div className="mt-0.5 whitespace-pre-line break-words text-[12.5px] text-[var(--accent-ink)]">Next: {a.next}</div>
                  </div>
                  <div className={cn("flex items-center gap-2", a.job ? "col-start-2 flex-wrap sm:shrink-0 sm:flex-nowrap" : "shrink-0")} data-testid="attention-actions">
                    {a.job ? <JobActions job={a.job} newest={a.grouped} /> : null}
                    <Link to={a.to} className="text-sm text-primary hover:underline">{a.open ?? "Open"}</Link>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
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
                      {c.source === "AnimeReviewCheck" && Number((c.detail as { open?: number } | undefined)?.open ?? 0) > 0 ? (
                        <Link to="/admin/settings/anime-mappings" className="ml-2 text-primary hover:underline">Review them</Link>
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      </Section>
      {/* The rest repeats other pages or is for diagnosis: folded, so the verdict comes first. */}
      <details className="group mt-8" data-testid="status-details" onToggle={(e) => setDetails(e.currentTarget.open)}>
        <summary className="cursor-pointer select-none text-[15px] font-semibold text-[var(--ink)]">Details: readiness, downloads, tasks, libraries, diagnostics</summary>
      {details ? <Readiness /> : null}
      <Section title="Downloads" aside={<Link to="/activity" className="text-primary hover:underline">Open queue</Link>}>
        {now.error ? <ErrorState error={now.error} /> : now.isPending ? <Loading /> : (
          <Strip
            items={[
              { label: "in progress", value: fmtNumber(inHand), testid: "dl-all" },
              { label: "downloading", value: fmtNumber(dl.downloading), testid: "dl-downloading" },
              { label: "waiting", value: fmtNumber(dl.waiting), testid: "dl-waiting" },
              { label: "need attention", value: fmtNumber(dl.attention), tone: dl.attention > 0 ? "warn" : undefined, testid: "dl-attention" },
            ]}
          />
        )}
      </Section>

      <Section title="Tasks" aside={<Link to="/admin/system/tasks" className="text-primary hover:underline">Open tasks</Link>}>
        {/* The counts come from the live stream or the failed-jobs answer; until either has spoken they are unknown, not 0. */}
        {Object.keys(counts).length === 0 && failed.error ? <ErrorState error={failed.error} /> : Object.keys(counts).length === 0 && failed.isPending ? <Loading /> : <Strip
          items={[
            { label: "running", value: fmtNumber(counts.running ?? 0), testid: "ov-jobs-running" },
            { label: "queued", value: fmtNumber(counts.queued ?? 0), testid: "ov-jobs-queued" },
            { label: "need attention", value: fmtNumber(nFailed), tone: nFailed > 0 ? "bad" : undefined, testid: "health-failed" },
          ]}
          action={<span className="text-xs text-muted-foreground">Finished and resolved tasks are folded on Tasks</span>}
        />}
      </Section>

      <Section title="Libraries">
        <DownloadsOffNotice off={globalDownloadsOff((st.data?.versions ?? []).map((v) => v.grab_state))} canManage className="mb-2" />
        <Card><div className="space-y-2">{(st.data?.versions ?? []).map((v) => <div key={v.key} className="flex flex-wrap items-start gap-2"><Link className="text-sm font-medium hover:underline" to={`/admin/settings/libraries?edit=${encodeURIComponent(v.key)}`} title={v.key}>{v.label || v.key}</Link><VersionStatus state={v.grab_state} settingsKey={v.key} canManage quiet /></div>)}</div></Card>
      </Section>

      <Section title="Diagnostics" aside={!setupUnfinished(setup.data) ? <Link className="text-primary hover:underline" to="/admin/setup">Setup checklist</Link> : undefined}>
        <StatGrid>
          <Stat testid="health-status" value={<span className="flex items-center gap-2">{r.status === "ready" ? <CheckCircle2 className="size-5 text-[var(--ok)]" /> : <AlertTriangle className="size-5 text-[var(--bad)]" />}{r.status ?? "unknown"}</span>} label="readiness (/readyz)" />
          <Stat value={egress?.mode === "off" ? "Off" : egress?.mode === "monitor-only" ? "Monitor-only" : vpn === undefined ? "—" : vpn ? "Up" : "Down"} tone={vpnOptional ? "warn" : vpn === false ? "bad" : vpn ? "ok" : undefined} label="VPN egress" />
          <Stat testid="health-checks" value={hc.data?.status ?? "—"} tone={healthStatTone(hc.data?.status)} label="health checks" />
          <Stat value={`${st.data?.schema?.current ?? "—"} / ${st.data?.schema?.target ?? "—"}`} tone={schemaOK ? undefined : "warn"} label="schema version" />
          <Stat value={<Mono className="block truncate text-lg">{(st.data?.version ?? "—").split(" ")[0]}</Mono>} label={`${PRODUCT_NAME} version`} />
        </StatGrid>
        {r.error ? <div className="mt-4"><ErrorState error={r.error} /></div> : null}
      </Section>

      </details>
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
                  <span className="text-muted-foreground">{d.cause === "no_video" ? "torrent has no video upstream" : "target missing"}</span>
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
  const sectionsQ = useSettingsSections();
  const sections = sectionsQ.data;
  const notify = sections ? sections.sections.find(s => s.key === "notify")?.config?.enabled === true : undefined;
  // Loading or an error reads as such, never as "Unknown" or OFF.
  const word = (on: boolean | undefined, pending: boolean, error: unknown) => error ? "Could not load" : pending ? "Loading…" : on === undefined ? "Unknown" : on ? "ON" : "OFF";
  const latest = [...(backups.data?.sets ?? [])].filter(b => b.complete).sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  return <Section title="Readiness" className="mt-4"><div className="grid gap-3 sm:grid-cols-3">
    {([
      ["Pause", "libraries", word(switches?.paused, settings.isPending, settings.error)],
      ["Notifications", "notifications", word(notify, sectionsQ.isPending, sectionsQ.error)],
    ] as const).map(([label, page, value]) => <Link key={page} to={SETTINGS_PAGES.find(p => p.id === page)!.route}><Card><h2>{label}</h2><strong>{value}</strong></Card></Link>)}
    <Link to="/admin/system/backup"><Card><h2>Backup</h2><span>{backups.error ? "Could not load backups" : backups.isPending ? "Loading…" : latest ? fmtTime(latest.created_at) : "No backup yet"}</span></Card></Link>
  </div></Section>;
}
