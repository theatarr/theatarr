// Admin › Import from Sonarr/Radarr (docs/migration.md "Import from Sonarr/Radarr"): a one-time import of an
// instance's titles, monitoring and debrid-mount links into one library. Check reads the instance; Preview writes
// nothing; Import is open only after a Preview of the same choices. The API key stays in this page's memory only.
import { useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { arrImportKey, startArrImport, useArrImportCheck, useArrImportRun, type ArrImportCheck, type ArrImportReport, type ArrImportRequest, type ArrImportRun } from "@/api/arr-import";
import { useVersions, type VersionRecord } from "@/api/settings-versions";
import { ErrorState, Fold, PageHeader } from "@/components/page";
import { Field, SELECT_CLASS } from "@/components/settings/parts";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtNumber } from "@/lib/format";
import { libraryLabel } from "@/lib/library-label";

type Flavour = "radarr" | "sonarr";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const n = fmtNumber;
const plural = (count: number, one: string, many: string) => `${n(count)} ${count === 1 ? one : many}`;
const appOf = (f: string) => f === "sonarr" ? "Sonarr" : "Radarr";

/** The choices a Preview is for: Import needs a finished Preview of the same ones. */
function choiceKey(c: { flavour: string; url: string; library: string; profiles: number[]; paths?: Record<string, string> | null }) {
  const paths = Object.entries(c.paths ?? {}).filter(([, to]) => to.trim()).sort(([a], [b]) => a.localeCompare(b));
  return JSON.stringify([c.flavour, c.url.trim(), c.library, [...c.profiles].sort((a, b) => a - b), paths]);
}

function libraryName(versions: VersionRecord[] | undefined, key: string) {
  const v = versions?.find((r) => r.key === key);
  return v ? libraryLabel(v.key, v.config.profile, v.config.name) : key;
}

export function ArrImportPage() {
  const qc = useQueryClient();
  const versions = useVersions();
  const runQuery = useArrImportRun();
  const check = useArrImportCheck();
  const [flavour, setFlavour] = useState<Flavour>("radarr");
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [checked, setChecked] = useState<ArrImportCheck>();
  const [library, setLibrary] = useState("");
  const [profiles, setProfiles] = useState<number[]>([]);
  const [paths, setPaths] = useState<Record<string, string>>({});
  const [startError, setStartError] = useState<unknown>();
  const [starting, setStarting] = useState(false);

  const kind = flavour === "sonarr" ? "series" : "movie";
  const app = appOf(flavour);
  const options = (versions.data?.versions ?? []).filter((v) => v.config.kind === kind);
  const chosen = options.some((v) => v.key === library) ? library : options[0]?.key ?? "";
  const run = runQuery.data?.run ?? undefined;
  const running = run?.state === "running";

  function reset() { setChecked(undefined); setProfiles([]); setPaths({}); check.reset(); }
  function runCheck() {
    setStartError(undefined);
    check.mutate({ flavour, url: url.trim(), api_key: key }, {
      onSuccess: (res) => { setChecked(res); setProfiles(res.profiles.map((p) => p.id)); setPaths({}); },
      onError: () => setChecked(undefined),
    });
  }
  const request = (dryRun: boolean): ArrImportRequest => ({
    flavour, url: url.trim(), api_key: key, library: chosen, profiles, dry_run: dryRun,
    paths: Object.fromEntries(Object.entries(paths).filter(([, to]) => to.trim()).map(([from, to]) => [from, to.trim()])),
  });
  async function start(dryRun: boolean) {
    setStarting(true); setStartError(undefined);
    try {
      const state = await startArrImport(request(dryRun));
      qc.setQueryData(arrImportKey, state);
      void qc.invalidateQueries({ queryKey: arrImportKey });
    } catch (error) { setStartError(error); }
    finally { setStarting(false); }
  }

  const ready = !!checked && !!chosen && profiles.length > 0 && !!key && !running && !starting;
  const previewed = !!run && run.dry_run && run.state === "done" && choiceKey(run) === choiceKey(request(true));

  return <div className="min-w-0 space-y-4 [overflow-wrap:anywhere]">
    <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Import from Sonarr/Radarr"
      description="Bring the titles of a Sonarr or Radarr instance into one of your libraries, once: what is monitored, and the files you already have on the debrid mount. Theatarr only reads the instance and changes nothing in it." />

    <Card className="space-y-3 text-sm">
      <h2 className="text-base font-semibold">Instance</h2>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <Field label="Type"><select className={SELECT_CLASS} value={flavour} disabled={running} onChange={(e) => { setFlavour(e.target.value as Flavour); reset(); }}>
          <option value="radarr">Radarr</option><option value="sonarr">Sonarr</option>
        </select></Field>
        <Field label="Address" help="The instance's address on your network."><Input value={url} disabled={running} placeholder={flavour === "sonarr" ? "http://192.168.1.10:8989" : "http://192.168.1.10:7878"} onChange={(e) => { setUrl(e.target.value); reset(); }} /></Field>
        <Field label="API key" help="In the instance under Settings › General."><Input type="password" autoComplete="off" value={key} disabled={running} onChange={(e) => setKey(e.target.value)} /></Field>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" disabled={!url.trim() || !key || check.isPending || running} onClick={runCheck}>{check.isPending ? "Checking…" : "Check"}</Button>
        {checked ? <span role="status" data-testid="arr-check">{app} {shortVersion(checked.version)}: {plural(checked.titles, kind === "series" ? "series" : "movie", kind === "series" ? "series" : "movies")}, {n(checked.with_files)} with a file.</span> : null}
      </div>
      {check.error ? <p role="alert" className="text-[var(--bad-ink)]">{message(check.error)}</p> : null}
    </Card>

    <Card className="space-y-2 text-sm">
      <h2 className="text-base font-semibold">Library</h2>
      {versions.error ? <ErrorState error={versions.error} /> : versions.isPending ? <p>Loading libraries…</p>
        : options.length === 0 ? <p>There is no {kind} library yet. Create one under <Link className="underline" to="/admin/settings/libraries">Settings › Libraries</Link> first.</p>
          : <Field label="Library" help="The library keeps its own quality profile and naming. Its folder must be separate from the instance's folders.">
            <select className={SELECT_CLASS} value={chosen} disabled={running} onChange={(e) => setLibrary(e.target.value)}>
              {options.map((v) => { const label = libraryLabel(v.key, v.config.profile, v.config.name); return <option key={v.key} value={v.key}>{label === v.key ? v.key : `${label} (${v.key})`}</option>; })}
            </select>
          </Field>}
    </Card>

    {checked ? <>
      <Card className="space-y-2 text-sm">
        <h2 className="text-base font-semibold">Quality profiles</h2>
        <fieldset className="space-y-1" disabled={running}>
          <legend className="mb-1">Import titles that use these quality profiles in {app}:</legend>
          {checked.profiles.map((p) => <label key={p.id} className="flex items-center gap-2">
            <Checkbox checked={profiles.includes(p.id)} onChange={(e) => setProfiles((cur) => e.target.checked ? [...cur, p.id] : cur.filter((id) => id !== p.id))} />
            <span>{p.name} ({n(p.titles)})</span>
          </label>)}
        </fieldset>
        <p className="text-[12.5px] text-muted-foreground">To send titles to different libraries, import once per library with its profiles ticked.</p>
        {profiles.length === 0 ? <p role="status">Tick at least one quality profile.</p> : null}
      </Card>

      <Card className="space-y-2 text-sm">
        <h2 className="text-base font-semibold">Folders</h2>
        <ul className="space-y-2">{checked.root_folders.map((r) => r.readable
          ? <li key={r.path}><span className="font-mono text-[13px]">{r.path}</span> ({plural(r.titles, "title", "titles")}): Theatarr can read it.</li>
          : <li key={r.path}><Field label={<>Theatarr cannot read <span className="font-mono text-[13px]">{r.path}</span>. Where does Theatarr see this folder?</>} name={`Where Theatarr sees ${r.path}`}>
            <Input value={paths[r.path] ?? ""} disabled={running} placeholder={r.path} onChange={(e) => setPaths((cur) => ({ ...cur, [r.path]: e.target.value }))} />
          </Field></li>)}</ul>
        <p className="text-[12.5px] text-muted-foreground">Theatarr reads the instance's files to see where their links point. Mount the folder into Theatarr's container if needed; read-only is enough.</p>
      </Card>
    </> : null}

    <div className="flex flex-wrap items-center gap-3">
      <Button variant="outline" disabled={!ready} onClick={() => void start(true)}>Preview</Button>
      <Button disabled={!ready || !previewed} title={ready && !previewed ? "Preview first" : undefined} onClick={() => void start(false)}>Import</Button>
      {ready && !previewed ? <span className="text-[12.5px] text-muted-foreground">Preview first</span> : null}
    </div>
    {startError ? <p role="alert" className="text-sm text-[var(--bad-ink)]">{message(startError)}</p> : null}

    {runQuery.error ? <ErrorState error={runQuery.error} /> : run ? <RunView run={run} libraryName={libraryName(versions.data?.versions, run.library)} /> : null}
  </div>;
}

/** "5.14.0.9383" reads as "5.14". */
function shortVersion(v: string) { return v.split(".").slice(0, 2).join("."); }

function RunView({ run, libraryName }: { run: ArrImportRun; libraryName: string }) {
  const app = appOf(run.flavour);
  if (run.state === "running") {
    const p = run.progress;
    const noun = run.flavour === "sonarr" ? "series" : "movies";
    const text = p.phase === "importing" ? `${run.dry_run ? "Previewing" : "Importing"}: ${n(p.done)} of ${n(p.total)} titles…`
      : p.phase === "reading" && p.total > 0 ? `Reading ${app}: ${n(p.done)} of ${n(p.total)} ${noun}…` : `Reading ${app}…`;
    return <Card className="text-sm" aria-busy><p role="status" data-testid="arr-progress">{text}</p></Card>;
  }
  return <Card className="space-y-2 text-sm" data-testid="arr-report">
    <h2 className="text-base font-semibold">{run.dry_run ? "Preview" : "Import"}</h2>
    {run.state === "failed" ? <p role="alert" className="text-[var(--bad-ink)]">{run.error}</p> : null}
    {run.report ? run.dry_run ? <PreviewReport r={run.report} library={libraryName} /> : <ImportReport r={run.report} library={libraryName} app={app} /> : null}
  </Card>;
}

/** The four reasons a file cannot be linked that the Preview can see, then the rest adopt may report. */
function notLinkedItems(r: ArrImportReport): string[] {
  const c = (reason: string) => r.not_linked[reason] ?? 0;
  const items: [number, string, string][] = [
    [c("not_a_symlink"), "ordinary file", "ordinary files"],
    [c("target_off_mount"), "link that points", "links that point"],
    [c("dangling"), "link whose file is", "links whose file is"],
    [c("arr_file_missing"), "file Theatarr cannot find", "files Theatarr cannot find"],
    [c("path_taken"), "file whose place in the library is taken by another file", "files whose place in the library is taken by another file"],
    [c("naming"), "file the library's naming could not name", "files the library's naming could not name"],
  ];
  const tail: Record<string, string> = {
    "ordinary file": " (for example from Usenet or a local disk)", "link that points": " outside the debrid mount",
    "link whose file is": " gone from the debrid mount", "file Theatarr cannot find": " (check the folder above)",
  };
  return items.filter(([count]) => count > 0).map(([count, one, many]) => `${plural(count, one, many)}${tail[one] ?? ""}`);
}

/** Files not linked for any reason (has_file here is a pack whose other episodes are still missing). */
function anyNotLinked(r: ArrImportReport) {
  return Object.values(r.not_linked).some((count) => count > 0);
}

function Skipped({ r }: { r: ArrImportReport }) {
  const s = r.skipped;
  const items: [number, string][] = [
    [s.profile, "use a quality profile you did not tick"],
    [s.does_not_fit, "do not fit this library (anime or not)"],
    [s.excluded, "are excluded in Theatarr"],
    [r.removed, "were removed from this library in Theatarr"],
    [s.no_id, "have no TMDb/TVDB id"],
  ];
  const shown = items.filter(([count]) => count > 0);
  return shown.length ? <p>Skipped: {shown.map(([count, words]) => `${n(count)} ${words}`).join(" · ")}.</p> : null;
}

const ADDED_AS_MISSING = "Files that are not linked can leave their titles or episodes missing: if they are monitored, Theatarr looks for them like any other missing title.";

function PreviewReport({ r, library }: { r: ArrImportReport; library: string }) {
  const items = notLinkedItems(r);
  return <>
    {r.tvdb_missing ? <p data-testid="arr-tvdb-missing">Theatarr has no TVDB key, so episodes cannot be matched to Sonarr's numbering: series will be added, but their monitoring and files will not be brought over. Add a TVDB key in <Link className="underline" to="/admin/settings/metadata">Settings › Metadata</Link> and run the import again.</p> : null}
    {!r.writable ? <p role="alert">Import needs the library to write links: {r.write_block}. Set it to Live and turn Write library links on; Allow new downloads can stay off.</p> : null}
    <p>Import would add {plural(r.added_title + r.added_library, "title", "titles")} to {library}: {n(r.added_title)} new to Theatarr, {n(r.added_library)} already in Theatarr but not in this library. {n(r.present)} are already in this library and stay as they are.</p>
    {!r.tvdb_missing ? <p>Up to {plural(r.linkable, "file", "files")} can be linked.</p> : null}
    {items.length ? <p>Not linkable: {items.join(" · ")}.</p> : null}
    {anyNotLinked(r) ? <p>{ADDED_AS_MISSING}</p> : null}
    <Skipped r={r} />
    {r.failed ? <p>{plural(r.failed, "title", "titles")} failed (see the list).</p> : null}
    {r.error ? <p role="alert">The preview stopped early: {r.error}</p> : null}
    <Problems r={r} />
  </>;
}

function ImportReport({ r, library, app }: { r: ArrImportReport; library: string; app: string }) {
  const items = notLinkedItems(r);
  const c = (reason: string) => r.not_linked[reason] ?? 0;
  const more: ReactNode[] = [];
  if (c("numbering")) more.push(<>{plural(c("numbering"), "series file", "series files")}: the episode list comes from TMDb, whose numbering can differ from Sonarr's. Add a TVDB key under <Link className="underline" to="/admin/settings/metadata">Settings › Metadata &amp; artwork</Link>, then run the import again.</>);
  if (c("no_episodes")) more.push(<>{plural(c("no_episodes"), "file", "files")} whose episodes Theatarr could not match.</>);
  if (c("no_episode_list")) more.push(<>{plural(c("no_episode_list"), "file", "files")} of series with no episode list yet: set up TVDB, then run the import again.</>);
  if (r.failed) more.push(<>{plural(r.failed, "title", "titles")} failed (see the list).</>);
  return <>
    <p>Added {plural(r.added_title + r.added_library, "title", "titles")} to {library} ({n(r.added_title)} new to Theatarr). Linked {plural(r.linked, "file", "files")}; {n(r.existing)} were linked already, and {plural(r.files_has_file, "file was", "files were")} left alone because the library has its own file for them.</p>
    {items.length || more.length ? <div><p>Not linked:{items.length ? ` ${items.join(" · ")}.` : null}</p>{more.length ? <ul className="list-disc pl-5">{more.map((m, i) => <li key={i}>{m}</li>)}</ul> : null}</div> : null}
    {anyNotLinked(r) ? <p>{ADDED_AS_MISSING}</p> : null}
    <Skipped r={r} />
    {r.missing_monitored ? <p>{plural(r.missing_monitored, "monitored title still has", "monitored titles still have")} something missing. <Link className="underline" to="/wanted">Wanted</Link> lists what can be searched now.</p> : null}
    {r.error ? <p role="alert">The import stopped early: {r.error}</p> : null}
    <p className="text-muted-foreground">Nothing was changed in {app}. Running the import again adds only new titles and links files that are still missing; it never changes monitoring you set in Theatarr. To keep adding titles that are added to {app} later, add it as a list under <Link className="underline" to="/collections/lists">Collections › Lists</Link>.</p>
    <Problems r={r} />
  </>;
}

const WHY: Record<string, string> = {
  not_a_symlink: "Ordinary file (Usenet or local disk)",
  target_off_mount: "Link points outside the debrid mount",
  dangling: "File gone from the debrid mount",
  arr_file_missing: "Theatarr cannot find the file",
  path_taken: "Its place in the library is taken",
  naming: "The library's naming could not name it",
  no_episodes: "Episodes not matched",
  numbering: "Episode list from TMDb",
  no_episode_list: "No episode list yet",
  has_file: "Part of a pack: the library has its own file for some episodes; the others are still missing",
  failed: "Failed",
};

function Problems({ r }: { r: ArrImportReport }) {
  if (!r.problems.length) return null;
  return <Fold title="Not linked (first 200)" testid="arr-problems">
    <Table>
      <TableHeader><TableRow><TableHead>Title</TableHead><TableHead>File</TableHead><TableHead>Why</TableHead></TableRow></TableHeader>
      <TableBody>{r.problems.map((p, i) => <TableRow key={i}>
        <TableCell className="min-w-[8rem] [overflow-wrap:normal]">{p.title}</TableCell>
        <TableCell className="min-w-[14rem] font-mono text-[12.5px] [overflow-wrap:anywhere]">{p.path ?? ""}</TableCell>
        <TableCell className="min-w-[11rem] [overflow-wrap:break-word]">{WHY[p.reason] ?? p.reason}{p.detail && (p.reason === "failed" || p.reason === "no_episode_list" || p.reason === "has_file") ? `: ${p.detail}` : ""}</TableCell>
      </TableRow>)}</TableBody>
    </Table>
  </Fold>;
}
