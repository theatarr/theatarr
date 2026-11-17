// The selection bar's Remove, Remove from a library and Add to a library (docs/design/ui.md "Library views"). Remove
// is the title page's removal dialog with the counts summed over the selection (POST /titles/removal reads each
// title's scope); confirming runs the same per-title removal (POST /titles/{id}/remove) title after title, and ends in
// one notice. Add to a library turns the library on for each title (PUT /titles/{id}/versions/{key}, as the add
// sheet); nothing is searched.
import { Loader2 } from "lucide-react";
import { useRef, useState } from "react";
import { setVersion, useAddOptions, type VersionOption } from "@/api/add";
import type { TitleRow } from "@/api/library";
import { removalScopeOf, removeTitle, useBulkRemovalScope, useRefresh, type RemovalScope } from "@/api/ops";
import { toast } from "@/components/toaster";
import { Button } from "@/components/ui/button";
import { Choice, ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Modal } from "@/components/ui/modal";
import { SELECT } from "@/components/library/filters";
import type { CopyColumn } from "@/lib/copies";
import { plural } from "@/lib/format";
import { bulkRemovalScope, REMOVAL_DEBRID_NEEDS_FILES, someNames, type Scope } from "@/lib/ops";
import { cn } from "@/lib/utils";
import { groupOf, namesList } from "@/lib/sync-groups";

/** The titles a loop over the selection could not do, by the server's reason: "2 titles not removed (Alpha and
 *  Beta): …". */
function failedWords(failed: Map<string, string[]>, verb: string): string[] {
  return [...failed].map(([why, names]) => `${plural(names.length, "title")} not ${verb} (${someNames(names)}): ${why}.`);
}

function addFailure(failed: Map<string, string[]>, e: unknown, name: string) {
  const why = errText(e).replace(/\.$/, "");
  failed.set(why, [...(failed.get(why) ?? []), name]);
}

/** The libraries a title's preview showed: its copies and the removed libraries whose files are left. */
const previewKeys = (s: RemovalScope) => [...s.copies, ...s.leftover].map((c) => c.key);

/** What a title's removal names: the one library chosen (the server adds the libraries kept in sync with it again),
 *  else every library its preview showed. */
const removalKeys = (s: RemovalScope, versions: string[]) => (versions.length ? versions : previewKeys(s));

/** Whether a title still holds what its preview showed: the same libraries with the same files, and the same answer
 *  to whether it leaves the library. */
function sameScope(was: RemovalScope, now: RemovalScope): boolean {
  const sig = (s: RemovalScope) => JSON.stringify([s.title, s.copies.map((c) => [c.key, c.links, c.downloads]), s.leftover.map((c) => [c.key, c.links])]);
  return sig(was) === sig(now);
}

function LibrarySelect({ label, value, onChange, groups }: { label: string; value: string; onChange: (v: string) => void; groups: { label?: string; libs: CopyColumn[] }[] }) {
  return (
    <label className="block text-sm">
      <span className="mb-1 block text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">{label}</span>
      <select aria-label={label} className={cn(SELECT, "w-full")} value={value} onChange={(e) => onChange(e.target.value)}>
        {value === "" ? <option value="" disabled>Choose a library</option> : null}
        {groups.map((g) =>
          g.label ? (
            <optgroup key={g.label} label={g.label}>
              {g.libs.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
            </optgroup>
          ) : (
            g.libs.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)
          ),
        )}
      </select>
    </label>
  );
}

function Checking({ error, onClose }: { error: unknown; onClose: () => void }) {
  return (
    <Modal label="Remove" onClose={onClose} testid="bulk-remove" className="z-[85] flex items-center justify-center p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)]"
      panelClassName="relative rounded-[8px] border border-[var(--line)] bg-[var(--surface)] p-4 text-sm">
      {error ? <p className="text-[var(--bad-ink)]">{errText(error)}</p> : <p className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Checking what this removes…</p>}
      <div className="mt-3 flex justify-end"><Button size="sm" variant="ghost" onClick={onClose}>Close</Button></div>
    </Modal>
  );
}

/** Remove the selected titles (libraries absent), or remove them from one library chosen here (libraries: the ones
 *  the list shows). */
export function BulkRemoveDialog({ titles, libraries, onClose, onDone }: { titles: TitleRow[]; libraries?: CopyColumn[]; onClose: () => void; onDone: () => void }) {
  const [lib, setLib] = useState(libraries?.[0]?.key ?? "");
  const [files, setFiles] = useState(true);
  const [debrid, setDebrid] = useState(true);
  // How many are done while the removal runs; the scope is not read again once it started.
  const [progress, setProgress] = useState<number | null>(null);
  const stop = useRef(false);
  const refresh = useRefresh();
  const ids = titles.map((t) => t.id ?? 0).filter(Boolean);
  const versions = libraries ? [lib] : [];
  const q = useBulkRemovalScope(ids, versions, progress === null);
  const close = () => {
    stop.current = true;
    onClose();
  };
  if (!q.data) return <Checking error={q.error} onClose={close} />;
  const { scopes, skipped } = q.data;
  const label = libraries?.find((c) => c.key === lib)?.label;
  const scope = bulkRemovalScope(scopes, files, debrid, label);
  const skips = new Map<string, number>();
  for (const s of skipped) skips.set(s.reason, (skips.get(s.reason) ?? 0) + 1);
  const run = async () => {
    stop.current = false;
    const failed = new Map<string, string[]>();
    let removed = 0;
    let left = 0;
    let cleanup = false;
    let done = 0;
    for (const s of scopes) {
      if (stop.current) break;
      setProgress(done);
      const name = s.name;
      try {
        // What the person chose (the libraries in sync follow on the server, as the preview showed), and only while
        // the title still holds what it showed.
        const keys = removalKeys(s, versions);
        if (!sameScope(s, await removalScopeOf(s.title_id, keys))) {
          addFailure(failed, "changed since the preview", name);
          done++;
          continue;
        }
        const r = await removeTitle(s.title_id, { versions: keys, unlink: files, debrid: files && debrid });
        removed++;
        left += r.kept.length + r.failed.length;
        cleanup ||= r.cleanup;
      } catch (e) {
        addFailure(failed, e, name);
      }
      done++;
    }
    refresh();
    const body = [
      ...failedWords(failed, "removed"),
      ...(done < scopes.length ? [`Stopped after ${done} of ${plural(scopes.length, "title")}.`] : []),
      ...(left > 0 ? [`${plural(left, "library file")} left in place; each title's History says why.`] : []),
      ...(cleanup ? ["Their debrid torrents are being cleaned up; each title's History shows the outcome."] : []),
      ...(!files ? ["Their files and debrid torrents stay."] : []),
    ];
    toast({
      tone: failed.size || left ? "warn" : "ok",
      title: `Removed ${removed === scopes.length ? plural(removed, "title") : `${removed} of ${plural(scopes.length, "title")}`}${label ? ` from ${label}` : ""}`,
      body: body.join(" ") || undefined,
      duration: 10_000,
    });
    // A dialog closed while it ran leaves the bar (and any dialog opened since) alone.
    if (!stop.current) onDone();
  };
  return (
    <ConfirmDialog
      scope={scope}
      subject={someNames(scopes.map((s) => s.name))}
      busy={progress !== null || q.isFetching}
      onClose={close}
      onConfirm={() => void run()}
      testid="bulk-remove"
      top={libraries ? <LibrarySelect label="Library" value={lib} onChange={setLib} groups={[{ libs: libraries }]} /> : null}
    >
      {skips.size ? (
        <p className="text-xs text-[var(--ink-3)]" data-testid="bulk-remove-skipped">
          {[...skips].map(([why, n]) => `Skipped ${plural(n, "title")}: ${why}.`).join(" ")}
        </p>
      ) : null}
      <Choice checked={files} onChange={setFiles} testid="remove-files">Delete the files</Choice>
      <Choice checked={files && debrid} onChange={setDebrid} disabled={!files} testid="remove-debrid">Delete {ids.length === 1 ? "it" : "them"} from the debrid providers</Choice>
      {!files ? <p className="pl-6 text-xs text-[var(--ink-3)]" data-testid="remove-debrid-why">{REMOVAL_DEBRID_NEEDS_FILES}.</p> : null}
      {progress !== null ? <p role="status" className="text-[var(--ink-2)]" data-testid="bulk-progress">Removing {progress + 1} of {scopes.length}…</p> : null}
    </ConfirmDialog>
  );
}

/** Add the selected titles to one library, chosen here among the libraries of the kinds the list holds; a title of
 *  the other kind, or already monitored there, is skipped. */
export function BulkAddDialog({ titles, kinds, anime, onClose, onDone }: { titles: TitleRow[]; kinds: ("movie" | "series")[]; anime: boolean; onClose: () => void; onDone: () => void }) {
  const movies = useAddOptions(kinds.includes("movie") ? { kind: "movie", name: "" } : null, anime);
  const series = useAddOptions(kinds.includes("series") ? { kind: "series", name: "" } : null, anime);
  // The libraries that fit the list (anime or not) first, as the add sheet lists them.
  const fitting = (vs: VersionOption[] = []) => [...vs.filter((v) => v.fits), ...vs.filter((v) => !v.fits)];
  const groups = [
    { label: kinds.length > 1 ? "Movie libraries" : undefined, kind: "movie", libs: fitting(movies.data?.versions) },
    { label: kinds.length > 1 ? "Series libraries" : undefined, kind: "series", libs: fitting(series.data?.versions) },
  ].filter((g) => g.libs.length > 0);
  const libs = groups.flatMap((g) => g.libs);
  const [key, setKey] = useState("");
  const [progress, setProgress] = useState<number | null>(null);
  const stop = useRef(false);
  const refresh = useRefresh();
  const close = () => {
    stop.current = true;
    onClose();
  };
  const loading = movies.isPending && kinds.includes("movie") || series.isPending && kinds.includes("series");
  const inLib = (t: TitleRow, key: string) => (t.versions ?? []).some((v) => v.key === key && v.monitored);
  // Preselected: the first fitting library that would add at least one of the selected titles; none when no fitting
  // library would (a library that does not fit is only ever picked by hand).
  const adds = (v: VersionOption) => titles.some((t) => t.kind === v.kind && !inLib(t, v.key));
  const lib = libs.find((v) => v.key === key) ?? libs.find((v) => v.fits && adds(v));
  const select = <LibrarySelect label="Library" value={lib?.key ?? ""} onChange={setKey} groups={groups.map((g) => ({ label: g.label, libs: g.libs.map((v) => ({ key: v.key, label: v.label || v.key })) }))} />;
  if (libs.length === 0) {
    const error = movies.error ?? series.error;
    return (
      <Modal label="Add to a library" onClose={close} testid="bulk-add" className="z-[85] flex items-center justify-center p-6"
        backdropClassName="absolute inset-0 bg-[var(--scrim)]"
        panelClassName="relative rounded-[8px] border border-[var(--line)] bg-[var(--surface)] p-4 text-sm">
        {error ? <p className="text-[var(--bad-ink)]">{errText(error)}</p> : loading ? <p className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Reading the libraries…</p> : <p>There is no library to add these titles to.</p>}
        <div className="mt-3 flex justify-end"><Button size="sm" variant="ghost" onClick={close}>Close</Button></div>
      </Modal>
    );
  }
  if (!lib) {
    const all = plural(titles.length, "title");
    return <ConfirmDialog scope={{ title: `Add ${all} to a library`, does: [], keeps: [], confirm: `Add ${all}` }} danger={false} disabled onClose={close} onConfirm={() => {}} testid="bulk-add" top={select} />;
  }
  const name = lib.label || lib.key;
  const inIt = (t: TitleRow) => inLib(t, lib.key);
  const wrong = titles.filter((t) => t.kind !== lib.kind);
  const already = titles.filter((t) => t.kind === lib.kind && inIt(t));
  const todo = titles.filter((t) => t.kind === lib.kind && !inIt(t));
  const n = todo.length;
  const kindWord = lib.kind === "series" ? "series" : "movies";
  // The libraries kept in sync with this one: its saved group, whatever each title's own fit (the line is general).
  const label = (k: string) => libs.find((v) => v.key === k)?.label || k;
  const syncGroups = [...(movies.data?.sync_groups ?? []), ...(series.data?.sync_groups ?? [])];
  const partners = (groupOf(syncGroups, lib.key) ?? []).filter((k) => k !== lib.key).map(label);
  const scope: Scope = {
    title: `Add ${plural(n, "title")} to ${name}`,
    does: [`${plural(n, "title")} ${n === 1 ? "is" : "are"} added to ${name} and monitored there: ${n === 1 ? "it is" : "they are"} wanted in that library from now on`],
    keeps: [
      "Nothing is searched now",
      // A library kept in sync adds to its partners too (the line below the dialog says so): only the files stay.
      partners.length > 0 ? `${n === 1 ? "Its" : "Their"} files` : `${n === 1 ? "Its" : "Their"} other libraries and files`,
      ...(already.length ? [`${plural(already.length, "title")} already in ${name}`] : []),
      ...(wrong.length ? [`${plural(wrong.length, lib.kind === "series" ? "movie" : "series", lib.kind === "series" ? "movies" : "series")} skipped: ${name} is a library for ${kindWord}`] : []),
    ],
    confirm: `Add ${plural(n, "title")}`,
    blocked: n === 0 ? `Nothing to add: every selected title is already in ${name} or of the other kind` : undefined,
  };
  const run = async () => {
    stop.current = false;
    const failed = new Map<string, string[]>();
    let added = 0;
    let done = 0;
    const alsoAdded: Record<string, number> = {};
    for (const t of todo) {
      if (stop.current) break;
      setProgress(done);
      try {
        const r = await setVersion(t.id ?? 0, lib.key, true);
        for (const k of r.synced ?? []) alsoAdded[k] = (alsoAdded[k] ?? 0) + 1;
        added++;
      } catch (e) {
        addFailure(failed, e, t.name ?? `#${t.id}`);
      }
      done++;
    }
    refresh();
    const body = [...failedWords(failed, "added"), ...(done < n ? [`Stopped after ${done} of ${plural(n, "title")}.`] : [])];
    const also = Object.entries(alsoAdded).map(([k, c]) => `${c} also added to ${label(k)}`);
    toast({
      tone: failed.size ? "warn" : "ok",
      title: `Added ${added === n ? plural(added, "title") : `${added} of ${plural(n, "title")}`} to ${name}${also.length ? ` and ${namesList(also)}` : ""}`,
      body: body.join(" ") || undefined,
      duration: 10_000,
    });
    if (!stop.current) onDone();
  };
  return (
    <ConfirmDialog
      scope={scope}
      subject={someNames(todo.map((t) => t.name ?? ""))}
      danger={false}
      busy={progress !== null}
      onClose={close}
      onConfirm={() => void run()}
      testid="bulk-add"
      top={select}
    >
      {partners.length > 0 ? <p className="text-xs text-[var(--ink-3)]" data-testid="bulk-add-synced">Also added to {namesList(partners)} where a title has no copy there or was removed from it.</p> : null}
      {progress !== null ? <p role="status" className="text-[var(--ink-2)]" data-testid="bulk-progress">Adding {progress + 1} of {n}…</p> : null}
    </ConfirmDialog>
  );
}
