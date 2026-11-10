import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, qs, type Schemas } from "@/api/client";
import { usePolicy } from "@/api/hooks";
import { PageHeader } from "@/components/page";
import { WantedTabs } from "@/components/wanted-tabs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtBytes } from "@/lib/format";

type Candidate = Schemas["ManualImportCandidate"];
type Choice = Schemas["ManualImportChoice"];
type Options = Schemas["ManualImportOptions"];
export type ImportRow = { candidate: Candidate; choice: Choice; selected: boolean };

export function importRow(candidate: Candidate): ImportRow {
  return { candidate, selected: true, choice: {
    id: candidate.id, path: candidate.path, movieId: candidate.movie?.id, seriesId: candidate.series?.id,
    episodeIds: candidate.episodes?.map((e) => e.id) ?? [], quality: candidate.quality,
    languages: candidate.languages, releaseGroup: candidate.releaseGroup ?? "",
    indexerFlags: candidate.indexerFlags, downloadId: candidate.downloadId,
  } };
}

export function ManualImportFiles({ rows, options, disabled, change }: {
  rows: ImportRow[]; options: Options; disabled: boolean; change: (index: number, row: ImportRow) => void;
}) {
  const series = options.flavour === "sonarr";
  return <Table><TableHeader><TableRow><TableHead>Import</TableHead><TableHead>File and decision</TableHead><TableHead>Choices</TableHead></TableRow></TableHeader>
    <TableBody>{rows.map((row, i) => {
      const { candidate: c, choice: f } = row;
      const title = options.titles.find((t) => t.id === (series ? f.seriesId : f.movieId));
      const update = (patch: Partial<Choice>) => change(i, { ...row, choice: { ...f, ...patch } });
      return <TableRow key={c.path}>
        <TableCell><input type="checkbox" aria-label={`Import ${c.path}`} checked={row.selected} disabled={disabled} onChange={(e) => change(i, { ...row, selected: e.target.checked })} /></TableCell>
        <TableCell className="max-w-md break-words align-top"><div>{c.path}</div><div>{fmtBytes(c.size)}</div>
          <Badge tone={c.rejections.length ? "warning" : "positive"}>{c.rejections.length ? "Needs review" : "Accepted"}</Badge>
          <ul>{c.rejections.map((r, n) => <li key={n}>{r.reason}</li>)}</ul><div>Format score: {c.customFormatScore}</div>
        </TableCell>
        <TableCell><fieldset disabled={disabled} className="grid min-w-64 gap-2"><legend className="sr-only">Choices for {c.path}</legend>
          <label>Title<select className="block w-full" value={series ? f.seriesId ?? "" : f.movieId ?? ""} onChange={(e) => update({ movieId: series ? undefined : Number(e.target.value), seriesId: series ? Number(e.target.value) : undefined, episodeIds: [] })}>
            <option value="">Choose a title</option>{options.titles.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select></label>
          {series && <label>Episodes<select multiple className="block w-full" value={(f.episodeIds ?? []).map(String)} onChange={(e) => update({ episodeIds: Array.from(e.target.selectedOptions, (o) => Number(o.value)) })}>
            {(title?.episodes ?? []).map((ep) => <option key={ep.id} value={ep.id}>S{ep.season}E{ep.number} {ep.name}</option>)}
          </select></label>}
          <label>Quality<select className="block w-full" value={f.quality?.quality.id ?? 0} onChange={(e) => { const q = options.qualities.find((q) => q.id === Number(e.target.value)); if (q) update({ quality: { quality: q, revision: f.quality?.revision ?? { version: 1 } } }); }}>
            {options.qualities.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
          </select></label>
          <label>Revision<Input type="number" min={1} value={f.quality?.revision.version ?? 1} onChange={(e) => { if (f.quality) update({ quality: { ...f.quality, revision: { ...f.quality.revision, version: Math.max(1, Number(e.target.value)) } } }); }} /></label>
          <label>Languages<select multiple className="block w-full" value={(f.languages ?? []).map((l) => String(l.id))} onChange={(e) => { const ids = Array.from(e.target.selectedOptions, (o) => Number(o.value)); update({ languages: options.languages.filter((l) => ids.includes(l.id)) }); }}>
            {options.languages.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
          </select></label>
          <label>Release group<Input value={f.releaseGroup ?? ""} onChange={(e) => update({ releaseGroup: e.target.value })} /></label>
        </fieldset></TableCell>
      </TableRow>;
    })}</TableBody></Table>;
}

/** Where a magnet or hash Theatarr has not seen goes: this page only reads downloads Theatarr started. */
const NEW_HASH_HELP = "To add a new magnet or hash, open the title and use Add by magnet or hash.";

/** What listing a source found; a hash with no download of ours says where a new hash goes instead of "0". */
export function listedWords(files: number, kind: string): string {
  return files === 0 && kind === "downloadId" ? `No download of Theatarr's has this hash. ${NEW_HASH_HELP}` : `${files} candidate file(s)`;
}

export function ManualImportPage() {
  const policy = usePolicy();
  const [version, setVersion] = useState("");
  const [source, setSource] = useState("");
  const [kind, setKind] = useState("folder");
  const [rows, setRows] = useState<ImportRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState("");
  const errorRef = useRef<HTMLDivElement>(null);
  const options = useQuery({ queryKey: ["manualimport", "options", version], queryFn: () => api<Options>("/manualimport/options" + qs({ version })), enabled: !!version });
  const problem = error || options.error?.message || policy.error?.message || "";
  useEffect(() => { if (problem) errorRef.current?.focus(); }, [problem]);
  const run = async (action: () => Promise<void>) => {
    setBusy(true); setError(""); setResult("");
    try { await action(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
  };
  const list = (e: FormEvent) => {
    e.preventDefault();
    void run(async () => {
      const files = await api<Candidate[]>("/manualimport" + qs({ version, [kind]: source.trim() }));
      setRows(files.map(importRow)); setDirty(false);
      setResult(listedWords(files.length, kind));
    });
  };
  const review = () => void run(async () => {
    const files = await api<Candidate[]>("/manualimport" + qs({ version }), { method: "POST", body: rows.map((r) => r.choice) });
    setRows(files.map((c, i) => ({ ...importRow(c), selected: rows[i]?.selected ?? true }))); setDirty(false); setResult("Decisions updated. Review the rejections before importing.");
  });
  const execute = () => void run(async () => {
    const files = rows.filter((r) => r.selected).map((r) => r.choice);
    if (files.some((f) => !(f.movieId || f.seriesId) || (options.data?.flavour === "sonarr" && !f.episodeIds?.length))) throw new Error("Choose a title and its episodes for every selected file.");
    const command = await api<Schemas["ManualImportResult"]>("/manualimport/execute" + qs({ version }), { method: "POST", body: { name: "ManualImport", importMode: "auto", files } });
    if (command.status === "failed") throw new Error(command.message);
    setResult(command.message);
  });
  return <div>
    <PageHeader title="Wanted" description="Manual import: review files from a download or mount folder, then link the chosen files into a library." />
    <WantedTabs active="manual-import" />
    <div ref={errorRef} tabIndex={-1} role={problem ? "alert" : undefined}>{problem}</div>
    <div role="status" aria-live="polite">{busy ? "Working…" : result}</div>
    <form onSubmit={list} className="my-4 flex flex-wrap items-end gap-3">
      <label>Library<select required disabled={busy} className="block" value={version} onChange={(e) => { setVersion(e.target.value); setRows([]); setResult(""); }}><option value="">Choose a library</option>{(policy.data?.versions ?? []).map((v) => <option key={v.key} value={v.key}>{v.key}</option>)}</select></label>
      <label>Source<select disabled={busy} className="block" value={kind} onChange={(e) => setKind(e.target.value)}><option value="folder">Mount folder</option><option value="downloadId">Download hash</option></select></label>
      <label className="min-w-64 flex-1">{kind === "folder" ? "Folder under the mount" : "Download hash"}<Input required disabled={busy} value={source} onChange={(e) => setSource(e.target.value)} /></label>
      <Button disabled={busy || !version || !source.trim() || !options.data} type="submit">List files</Button>
    </form>
    {kind === "downloadId" && <p className="mb-4 text-xs text-[var(--ink-3)]" data-testid="manual-import-hash-help">A download Theatarr already started. {NEW_HASH_HELP}</p>}
    {options.data && rows.length > 0 && <>
      <ManualImportFiles rows={rows} options={options.data} disabled={busy} change={(i, row) => { setRows((old) => old.map((r, n) => n === i ? row : r)); setDirty(true); }} />
      <div className="mt-4 flex gap-3"><Button variant="secondary" disabled={busy} onClick={review}>Review choices</Button><Button disabled={busy || dirty || !rows.some((r) => r.selected)} onClick={execute}>Import selected files</Button></div>
      {dirty && <p>Review choices to update decisions before importing.</p>}
    </>}
  </div>;
}
