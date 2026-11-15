import { ADVANCED_SETTINGS_PAGES } from "@/lib/settings-map";
// Advanced tools and the existing database diagnostics composed on System › Backup.
import { CheckCircle2, ChevronRight, XCircle } from "lucide-react";
import { useEffect, useRef } from "react";
import { Link, useLocation } from "react-router";
import { useHealth, useStatus } from "@/api/hooks";
import { useTasks } from "@/api/jobs";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Section, Stat, StatGrid } from "@/components/page";
import { ADVANCED } from "@/components/shell/nav";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { fmtNumber, fmtTime } from "@/lib/format";
import { backupCheck, healthBadgeTone } from "@/lib/health";

const ABOUT: Record<string, string> = {
  ...Object.fromEntries(ADVANCED_SETTINGS_PAGES.map(p => [p.route, p.description])),
};

export function AdvancedPage() {
  return (
    <div>
      <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Advanced" description="YAML settings, request limits and settings history." />
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

const COUNT_LABELS: Record<string, string> = {
  title: "titles",
  version: "title-library entries",
  content: "torrents in the registry",
  job: "jobs",
  link: "library links",
};

export function Database() {
  const st = useStatus();
  const backups = useTasks();
  if (st.isPending) return <Loading />;
  if (st.error) return <ErrorState error={st.error} />;
  const counts = st.data?.counts ?? {};
  const nightly = backups.data?.items.find((t) => t.key === "db.nightly");
  return (
    <div>
      <StatGrid>
        {/* version_file is an empty legacy table: not shown. */}
        {Object.keys(counts).filter((k) => k !== "version_file").sort().map((k) => (
          <Stat key={k} testid={`count-${k}`} value={fmtNumber(counts[k])} label={COUNT_LABELS[k] ?? k} />
        ))}
      </StatGrid>
      <Section title="Backups">
        <Card>
          <KV
            items={[
              ["Schema", `${st.data?.schema?.current} of ${st.data?.schema?.target}`],
              ["Last nightly backup", backups.error ? "Could not load" : backups.isPending ? "Loading…" : nightly?.last_finished ? `${fmtTime(nightly.last_finished)}${nightly.last_error ? " (failed)" : ""}` : "none recorded yet"],
              ["How", <span key="h">the <Mono>db.nightly</Mono> task writes a copy to the backup directory</span>],
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
