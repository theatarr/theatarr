// The TRaSH defaults on Settings › Profiles: every TRaSH-Guides quality profile of each flavour, built in. They come
// from the TRaSH-Guides checkout in THEATARR_TRASH when one loads, otherwise from the snapshot bundled with this
// version, so they are there with no setup. Using one opens the profile editor on a copy; the copy is an ordinary
// profile, and the default stays listed.
import { useState } from "react";
import { Link } from "react-router";
import { buildPreset, presetCopies, usePresets, type Built, type Preset, type QualityOverview } from "@/api/settings-profiles";
import { ErrorState, Loading, Mono, Section } from "@/components/page";
import { ExplainBox } from "@/components/settings/parts";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { explainError } from "@/lib/settings";

const FLAVOURS = [["radarr", "Movies (Radarr)"], ["sonarr", "Series (Sonarr)"]] as const;

/** Where the defaults come from, in words. */
export function TrashSourceLine({ source, commit, date }: { source?: string; commit?: string; date?: string }) {
  const at = commit ? <> (commit <Mono>{commit.slice(0, 7)}</Mono>{date ? `, ${date}` : ""})</> : null;
  if (source === "bundled") return <p className="text-[13px] text-muted-foreground" data-testid="trash-source">TRaSH defaults bundled with this version{at}. A TRaSH-Guides checkout (<Mono>THEATARR_TRASH</Mono>) or a tracked source under <Link className="underline" to="/admin/settings/quality-sync">Quality sync</Link> keeps them current.</p>;
  if (source === "checkout") return <p className="text-[13px] text-muted-foreground" data-testid="trash-source">TRaSH defaults from the TRaSH-Guides checkout in <Mono>THEATARR_TRASH</Mono>{at}.</p>;
  return null;
}

function FlavourDefaults({ flavour, label, overview, readOnly, onUse }: { flavour: string; label: string; overview: QualityOverview; readOnly: boolean; onUse: (preset: Preset, built: Built) => void }) {
  const presets = usePresets(flavour);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<unknown>();
  const use = async (p: Preset) => {
    setBusy(p.trash_id); setError(undefined);
    try { onUse(p, await buildPreset(flavour, p.trash_id, [])); } catch (e) { setError(e); } finally { setBusy(""); }
  };
  const list = presets.data?.presets ?? [];
  return <details open className="rounded-lg border border-[var(--line)]" data-testid={`trash-defaults-${flavour}`}>
    <summary className="cursor-pointer select-none px-3 py-2 text-sm font-medium">{label}{presets.data ? <span className="font-normal text-muted-foreground"> · {list.length} {list.length === 1 ? "profile" : "profiles"}</span> : null}</summary>
    <div className="border-t border-[var(--line)]">
      {presets.isPending ? <Loading /> : presets.error ? <ErrorState error={presets.error} /> : presets.data.unavailable ? <p className="px-3 py-2 text-sm text-muted-foreground">{presets.data.unavailable}.</p> : <Table>
        <TableHeader><TableRow>
          <TableHead>TRaSH profile</TableHead>
          <TableHead className="hidden md:table-cell">Qualities</TableHead>
          <TableHead className="hidden lg:table-cell">Formats</TableHead>
          <TableHead className="hidden sm:table-cell">Your copies</TableHead>
          <TableHead><span className="sr-only">Actions</span></TableHead>
        </TableRow></TableHeader>
        <TableBody>
          {list.map((p) => {
            const copies = presetCopies(overview.profiles, flavour, p.trash_id);
            const qualities = `${p.qualities.join(" › ")} · cutoff ${p.cutoff}`;
            const copyList = copies.length ? <ul className="flex flex-wrap gap-1">{copies.map((k) => <li key={k}><Badge tone="outline">{k}</Badge></li>)}</ul> : <span className="text-[13px] text-muted-foreground">none yet</span>;
            // "Use" makes the first profile from the default; with a copy already there it is one more copy.
            const action = copies.length ? "Create a copy" : "Use";
            return <TableRow key={p.trash_id} data-preset={p.trash_id}>
              <TableCell className="align-top">
                <div className="text-sm font-medium [overflow-wrap:anywhere]">{p.name}</div>
                {p.description ? <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]">{p.description}</div> : null}
                <div className="mt-1 text-xs text-muted-foreground [overflow-wrap:anywhere] md:hidden">{qualities}</div>
                <div className="mt-1 sm:hidden">{copyList}</div>
              </TableCell>
              <TableCell className="hidden max-w-[22rem] align-top text-[13px] [overflow-wrap:anywhere] md:table-cell">{qualities}</TableCell>
              <TableCell className="hidden align-top tabular-nums lg:table-cell">{p.formats}</TableCell>
              <TableCell className="hidden align-top sm:table-cell">{copyList}</TableCell>
              <TableCell className="align-top text-right"><Button size="sm" variant="outline" disabled={readOnly || !!busy} aria-label={`${action}: ${p.name} (${label})`} onClick={() => void use(p)}>{busy === p.trash_id ? "Building…" : action}</Button></TableCell>
            </TableRow>;
          })}
        </TableBody>
      </Table>}
      {error ? <div className="p-3"><ExplainBox error={explainError(error)} /></div> : null}
    </div>
  </details>;
}

export function TrashDefaults({ overview, readOnly, onUse }: { overview: QualityOverview; readOnly: boolean; onUse: (flavour: string, preset: Preset, built: Built) => void }) {
  const flavours = FLAVOURS.filter(([f]) => overview.trash.includes(f));
  if (!flavours.length) return null;
  return <Section title="TRaSH defaults">
    <div className="space-y-3" data-testid="trash-defaults">
      <p className="text-[13px] text-muted-foreground">Every TRaSH-Guides quality profile is built in. Use one to get a profile of your own from it, which you can edit and pick for a library; the library editor also offers these directly. A default stays listed after it is used, and your copy never changes it.</p>
      <TrashSourceLine source={overview.trash_source} commit={overview.trash_commit} date={overview.trash_commit_date} />
      <Card className="space-y-2 p-2">
        {flavours.map(([f, label]) => <FlavourDefaults key={f} flavour={f} label={label} overview={overview} readOnly={readOnly} onUse={(p, b) => onUse(f, p, b)} />)}
      </Card>
    </div>
  </Section>;
}
