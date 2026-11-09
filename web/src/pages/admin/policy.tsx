// Advanced › YAML settings › Policy files, in phase tabs (docs/design/settings.md): Policy files is the running policy,
// read-only (GET /policy), with a fresh validation of the policy directory (GET /policy/validate, which never
// reloads); Indexers, Providers, Budgets, Import & export and Audit are the settings database (pages/admin/settings.tsx).
import { CheckCircle2, XCircle } from "lucide-react";
import { useState } from "react";
import { usePolicy, useValidate } from "@/api/hooks";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { ErrorState, Loading, Mono, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function PolicyFiles() {
  const pol = usePolicy();
  const [check, setCheck] = useState(false);
  const val = useValidate(check);
  if (pol.isPending) return <Loading />;
  if (pol.error) return <ErrorState error={pol.error} />;
  const p = pol.data;
  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-3xl text-sm text-muted-foreground">
          Policy directory: <Mono>{p.dir}</Mono>. Indexers, providers, budgets, quality profiles (with their format
          sets), custom formats, naming and libraries are managed on their own tabs. Global switches, arr views and the other sections
          (library and Plex, notifications, metadata, egress and the rest) are managed in Settings. The files here are imported only when you run an import (Import & export, or <Mono>theatarr settings import</Mono>).
        </p>
        <Button variant="outline" onClick={() => (check ? val.refetch() : setCheck(true))} disabled={val.isFetching}>Validate now</Button>
      </div>
      {check && val.data ? (
        <Card className="mb-4" data-testid="validate-result">
          <div className="flex items-center gap-2 font-medium">
            {val.data.ok ? <CheckCircle2 className="size-5 text-[var(--ok)]" /> : <XCircle className="size-5 text-[var(--bad)]" />}
            {val.data.ok ? "Valid" : "Problems found"} <span className="text-sm font-normal text-muted-foreground">in {val.data.dir}</span>
          </div>
          {(val.data.problems ?? []).length > 0 ? <ul className="mt-2 list-disc space-y-1 pl-6 text-sm">{val.data.problems?.map((x, i) => <li key={i}>{x}</li>)}</ul> : null}
        </Card>
      ) : null}
      {val.error ? <ErrorState error={val.error} /> : null}

      <Section title="Libraries" className="mt-0">
        <DownloadsOffNotice off={globalDownloadsOff(p.versions.map((v) => v.grab_state))} canManage className="mb-2" />
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Library</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="hidden sm:table-cell">Profile</TableHead>
                <TableHead className="hidden lg:table-cell">Library</TableHead>
                <TableHead className="hidden md:table-cell">Indexers</TableHead>
                <TableHead className="hidden md:table-cell">Providers</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {p.versions.map((v) => (
                <TableRow key={v.key}>
                  <TableCell><VersionTag k={v.key} /><div className="mt-1 text-xs text-muted-foreground">{v.kind} · {v.storage}</div></TableCell>
                  <TableCell><VersionStatus state={v.grab_state} showMode settingsKey={v.key} canManage /></TableCell>
                  <TableCell className="hidden sm:table-cell"><Mono>{v.profile}</Mono></TableCell>
                  <TableCell className="hidden break-all lg:table-cell"><Mono>{v.library}</Mono></TableCell>
                  <TableCell className="hidden max-w-[16rem] text-[13px] md:table-cell">{v.indexers.join(", ")}</TableCell>
                  <TableCell className="hidden text-[13px] md:table-cell">{v.providers.join(" › ")}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </Section>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Section title="Indexers">
          <Card className="p-0">
            <Table>
              <TableBody>
                {p.indexers.map((ix) => (
                  <TableRow key={ix.key}>
                    <TableCell><Mono>{ix.key}</Mono></TableCell>
                    <TableCell>{ix.kind}</TableCell>
                    <TableCell><Badge tone={ix.privacy === "public" ? "positive" : "warning"}>{ix.privacy}</Badge></TableCell>
                    <TableCell className="hidden sm:table-cell"><Mono className="text-muted-foreground">{ix.budget ?? "—"}</Mono>{ix.proxy ? <Badge className="ml-2">proxy</Badge> : null}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </Section>
        <Section title="Outbound hosts" aside={`${p.outbound_hosts.length} hosts · budgets: ${p.budgets.length}`}>
          <Card className="p-0">
            <Table density="dense">
              <TableBody>
                {p.outbound_hosts.map((h) => (
                  <TableRow key={h.host}>
                    <TableCell className="break-all"><Mono>{h.host}</Mono></TableCell>
                    <TableCell className="text-[12.5px] text-muted-foreground">{(h.used_by ?? []).join(", ")}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </Section>
      </div>
      <Section title="Files">
        <p className="text-sm text-muted-foreground">{p.files.map((f) => <Mono key={f} className="mr-3 inline-block">{f}</Mono>)}</p>
      </Section>
    </div>
  );
}
