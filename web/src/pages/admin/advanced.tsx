import { ArrTabs } from "./arr-tabs";
import { ADVANCED_SETTINGS_PAGES } from "@/lib/settings-map";
// Advanced tools and the existing database diagnostics composed on System › Backup.
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, ChevronRight, XCircle } from "lucide-react";
import { useEffect, useRef } from "react";
import { Link, useLocation } from "react-router";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";
import { PRODUCT_NAME } from "@/brand";
import { useHealth, useImports, useStatus, type JobList } from "@/api/hooks";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Section, Stat, StatGrid, VersionTag } from "@/components/page";
import { ADVANCED } from "@/components/shell/nav";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtDuration, fmtNumber, fmtTime } from "@/lib/format";
import { backupCheck, healthBadgeTone } from "@/lib/health";

const CRUMB = [{ label: "Admin", to: "/admin" }, { label: "Advanced", to: "/admin/advanced" }];

const ABOUT: Record<string, string> = {
  ...Object.fromEntries(ADVANCED_SETTINGS_PAGES.map(p => [p.route, p.description])),
  "/admin/advanced/manual-import": "Review mount files and link selected titles and episodes into a library.",
  "/admin/advanced/journal": "Every grab decision and what followed it; judge flagged entries.",
};

export function AdvancedPage() {
  return (
    <div>
      <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Advanced" description="Diagnostics and records for when something needs a closer look." />
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {ADVANCED.map((it) => (
          <Link key={it.to} to={it.to!} className="group" data-testid={`advanced-${it.to!.split("/").pop()}`}>
            <Card className="flex h-full items-start gap-3 transition group-hover:border-[var(--line-2)] group-hover:bg-[var(--hover)]">
              <it.icon className="mt-0.5 size-5 shrink-0 text-[var(--accent-ink)]" />
              <div className="min-w-0 flex-1">
                <div className="font-medium text-[var(--ink)]">{it.label}</div>
                <p className="mt-0.5 text-[13px] text-muted-foreground">{ABOUT[it.to!]}</p>
              </div>
              <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
            </Card>
          </Link>
        ))}
      </div>
    </div>
  );
}

export function ImportsPage() {
  return (
    <div>
      <PageHeader crumb={[...CRUMB, { label: "Sonarr/Radarr migration", to: "/admin/advanced/arr" }]} title="Import runs" description={`The 15-minute read-only import from the arrs ${PRODUCT_NAME} shadows. It ends at cutover.`} />
      <ArrTabs />
      <Imports />
    </div>
  );
}

type Summary = { version?: string; titles?: { total?: number }; changes?: Record<string, unknown> };

function Imports() {
  const im = useImports(20);
  if (im.isPending) return <Loading />;
  if (im.error) return <ErrorState error={im.error} />;
  const items = im.data?.items ?? [];
  return (
    <div>
      {items.length === 0 ? <Card><Empty>No import runs yet.</Empty></Card> : (
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Run</TableHead>
                <TableHead>Instance</TableHead>
                <TableHead className="hidden sm:table-cell">Library</TableHead>
                <TableHead className="text-right">Titles</TableHead>
                <TableHead className="hidden text-right md:table-cell">Changes</TableHead>
                <TableHead className="hidden md:table-cell">Started</TableHead>
                <TableHead className="text-right">Took</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((r) => {
                const s = (r.summary ?? {}) as Summary;
                return (
                  <TableRow key={r.id}>
                    <TableCell className="tabular-nums text-muted-foreground">{r.id}</TableCell>
                    <TableCell><Mono>{r.instance}</Mono></TableCell>
                    <TableCell className="hidden sm:table-cell">{s.version ? <VersionTag k={s.version} /> : "—"}</TableCell>
                    <TableCell className="text-right tabular-nums">{fmtNumber(s.titles?.total)}</TableCell>
                    <TableCell className="hidden text-right tabular-nums md:table-cell">{fmtNumber(Object.keys(s.changes ?? {}).length)}</TableCell>
                    <TableCell className="hidden whitespace-nowrap md:table-cell">{fmtTime(r.started_at)}</TableCell>
                    <TableCell className="whitespace-nowrap text-right">{fmtDuration(r.started_at, r.finished_at)}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

const COUNT_LABELS: Record<string, string> = {
  title: "titles",
  version: "versions",
  version_file: "library files",
  content: "contents",
  import_run: "import runs",
  job: "jobs",
  link: "library links",
};

export function Database() {
  const st = useStatus();
  const backups = useQuery({
    queryKey: qk.jobs.kind("db.nightly"),
    queryFn: () => api<JobList>("/jobs?kind=db.nightly&state=done&limit=1"),
  });
  if (st.isPending) return <Loading />;
  if (st.error) return <ErrorState error={st.error} />;
  const counts = st.data?.counts ?? {};
  const last = backups.data?.items?.[0];
  return (
    <div>
      <StatGrid>
        {Object.keys(counts).sort().map((k) => (
          <Stat key={k} testid={`count-${k}`} value={fmtNumber(counts[k])} label={COUNT_LABELS[k] ?? k} />
        ))}
      </StatGrid>
      <Section title="Backups">
        <Card>
          <KV
            items={[
              ["Schema", `${st.data?.schema?.current} of ${st.data?.schema?.target}`],
              ["Last nightly backup", last ? `${fmtTime(last.finished_at)} (job #${last.id})` : "none recorded yet"],
              ["How", <span key="h">the <Mono>db.nightly</Mono> job writes a copy to the backup directory</span>],
            ]}
          />
        </Card>
      </Section>
      <BackupCheck />
    </div>
  );
}

/** The health backup check: its state, the newest set's files, and the fix when it fails (lib/health backupCheck). */
function BackupCheck() {
  const hc = useHealth();
  const { hash } = useLocation();
  const ref = useRef<HTMLDivElement>(null);
  const check = hc.data?.checks?.find((c) => c.source === "BackupCheck");
  useEffect(() => {
    if (hash === "#backup-check" && check) ref.current?.scrollIntoView({ block: "start" });
  }, [hash, check]);
  if (hc.isPending) return null;
  if (!check) {
    return (
      <div id="backup-check" ref={ref}>
        <Section title="Backup check">
          <Card><Empty>The backup check has not run on this server.</Empty></Card>
        </Section>
      </div>
    );
  }
  const b = backupCheck(check);
  return (
    <div id="backup-check" ref={ref} data-testid="backup-check" data-type={check.type}>
      <Section title="Backup check" aside={<Badge tone={healthBadgeTone(check.type)}>{check.type ?? "ok"}</Badge>}>
        <Card>
          <p className="text-sm font-medium text-[var(--ink)]">{check.message}</p>
          <div className="mt-3">
            <KV
              items={[
                ["Newest set", b.newest ? <Mono key="n">{b.newest}</Mono> : "none"],
                [
                  "Set holds",
                  b.parts.length ? (
                    <ul key="p" className="flex flex-wrap gap-x-4 gap-y-1" data-testid="backup-parts">
                      {b.parts.map((p) => (
                        <li key={p.label} data-present={p.present} className={p.present ? "inline-flex items-center gap-1.5" : "inline-flex items-center gap-1.5 text-[var(--bad-ink)]"}>
                          {p.present ? <CheckCircle2 className="size-3.5 text-[var(--ok-ink)]" /> : <XCircle className="size-3.5" />}
                          {p.label}
                          {p.present ? "" : " missing"}
                        </li>
                      ))}
                    </ul>
                  ) : (
                    "—"
                  ),
                ],
                ["Sets kept", b.count != null ? fmtNumber(b.count) : "—"],
                ["Directory", b.dir ? <Mono key="d">{b.dir}</Mono> : "off"],
                ["Checked", fmtTime(check.checked_at)],
              ]}
            />
          </div>
          {b.fix ? (
            <p className="mt-3 rounded-lg border border-[var(--line)] bg-[var(--sunken)] p-3 text-sm" data-testid="backup-fix">
              <span className="font-semibold">Fix: </span>
              {b.fix}
            </p>
          ) : null}
        </Card>
      </Section>
    </div>
  );
}
