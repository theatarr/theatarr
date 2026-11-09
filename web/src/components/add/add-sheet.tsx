import { usePerms } from "@/api/library";
// The add sheet: versions to turn on (every version that fits the title ticked), seasons
// for a series whose episodes the library knows, and a search-now switch shown only when a search would run (a ticked
// version in mode active with the router on). It posts to POST /titles, the same add path as the arr views.
// A side sheet on desktop and a bottom sheet on phones, in our Modal (components/ui/modal.tsx: focus trap, inert page,
// focus restore, shared scroll lock), since Radix Dialog's scroll lock injects a <style> element the CSP refuses. Layout follows Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/root/
// add-to-catalog-dialog.tsx @ 2ef7757c, GPL-3.0 (poster header, option rows, footer actions) and Seerr's RequestModal
// (MIT): the season list with a toggle per season.
import { AlertTriangle, ArrowUpRight, CheckCircle2, Loader2, Plus, Search, X } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { useAddOptions, useAddTitle, type AddResult, type AddTarget, type VersionOption } from "@/api/add";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { VersionTag } from "@/components/page";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { globalDownloadsOff, willGrab } from "@/lib/version-grab";
import { FacetPill, facetOf, Poster } from "@/components/search/search-results";
import { Modal } from "@/components/ui/modal";
import { addRequest, libraryTitleId } from "./add-request";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

function CheckRow({
  checked,
  disabled,
  onChange,
  children,
  aside,
  testid,
}: {
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  children: ReactNode;
  aside?: ReactNode;
  testid?: string;
}) {
  return (
    <label
      data-testid={testid}
      className={cn(
        "flex cursor-pointer items-center gap-3 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2.5 transition hover:border-[var(--line-2)]",
        checked && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]",
        disabled && "cursor-default opacity-70 hover:border-[var(--line)]",
      )}
    >
      <Checkbox checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="min-w-0 flex-1">{children}</span>
      {aside ? <span className="shrink-0">{aside}</span> : null}
    </label>
  );
}

/** A switch row (the search-now toggle): the shared Switch, with the whole row as its label. */
function SwitchRow({ on, onChange, title, detail, testid }: { on: boolean; onChange: (v: boolean) => void; title: string; detail: string; testid?: string }) {
  const id = useId();
  return (
    <label className="flex w-full cursor-pointer items-center gap-3 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2.5 transition-colors hover:bg-[var(--hover)]">
      <span className="min-w-0 flex-1">
        <span id={`${id}-t`} className="block text-sm font-medium text-[var(--ink)]">{title}</span>
        <span id={`${id}-d`} className="block text-xs text-[var(--ink-3)]">{detail}</span>
      </span>
      <Switch checked={on} onCheckedChange={onChange} aria-labelledby={`${id}-t`} aria-describedby={`${id}-d`} data-testid={testid} />
    </label>
  );
}

/** One library in the add drawer: the checkbox, then identity (name, key, kind) and the explanation on
 * their own wrapping rows, then the library state, so long names and narrow phones never overlap. */
function LibraryRow({ v, checked, disabled, onChange, note, admin }: {
  v: VersionOption;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  note?: string;
  admin: boolean;
}) {
  const id = useId();
  return (
    <div data-testid={`add-version-${v.key}`} data-checked={checked || undefined} className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 border-b border-[var(--line)] py-2.5 last:border-b-0">
      <Checkbox id={id} checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} aria-describedby={`${id}-state`} className="mt-0.5" />
      <div className="min-w-0 space-y-1">
        <label htmlFor={id} className={cn("flex flex-wrap items-baseline gap-x-2 gap-y-0.5", !disabled && "cursor-pointer")}>
          <span className="text-sm font-medium text-[var(--ink)] [overflow-wrap:anywhere]">{v.label || v.key}</span>
          <span className="font-mono text-[11.5px] text-[var(--ink-3)] [overflow-wrap:anywhere]">{v.key}</span>
          <span className="text-xs text-[var(--ink-3)]">{v.kind}{v.anime === true ? " · anime" : ""}</span>
        </label>
        {note ? <p className="text-xs text-[var(--ink-3)]">{note}</p> : null}
        <div id={`${id}-state`}><VersionStatus state={v.grab_state} showMode settingsKey={v.key} canManage={admin} /></div>
      </div>
    </div>
  );
}

function seasonLabel(n: number) {
  return n === 0 ? "Specials" : `Season ${n}`;
}

export function AddSheet({ target, onClose }: { target: AddTarget; onClose: () => void }) {
  const perms = usePerms();
  const [anime, setAnime] = useState(Boolean(target.anime));
  const opts = useAddOptions(target, anime);
  const inLibrary = libraryTitleId(target, opts.data) !== undefined;
  const add = useAddTitle();
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [seasons, setSeasons] = useState<Record<number, boolean>>({});
  const [search, setSearch] = useState(false);
  const [showOther, setShowOther] = useState(false);
  const [done, setDone] = useState<AddResult | null>(null);

  const o = opts.data;
  const versions = useMemo(() => o?.versions ?? [], [o]);
  // Ticked: what the user set, else the default (fits the title), else already on.
  const isOn = (v: VersionOption) => (v.on ? true : picked[v.key] ?? v.default);
  const chosen = versions.filter((v) => !v.on && isOn(v)).map((v) => v.key);
  const fitting = versions.filter((v) => v.fits || v.on);
  const other = versions.filter((v) => !v.fits && !v.on);
  const searchable = versions.filter((v) => isOn(v) && v.searchable).map((v) => v.key);
  // The footer counts the ticked libraries the server says will download (will_grab).
  const selected = versions.filter(isOn);
  const willDownload = selected.filter((v) => !!v.grab_state && willGrab(v.grab_state)).length;
  const globalOff = globalDownloadsOff(versions.map((v) => v.grab_state));
  const canSearch = searchable.length > 0;
  const seasonOn = (n: number, def: boolean) => seasons[n] ?? def;
  const seasonsChanged = (o?.seasons ?? []).some((s) => seasons[s.season] !== undefined && seasons[s.season] !== (s.monitored ?? true));
  const epKnown = Boolean(o?.episodes_known);

  const submit = () => {
    if (!o) return;
    const seasonList = o.kind === "series" && epKnown ? (o.seasons ?? []).map((s) => ({ season: s.season, monitored: seasonOn(s.season, s.monitored ?? true) })) : undefined;
    add.mutate(
      addRequest(target, o, {
        versions: [...chosen, ...versions.filter((v) => v.on).map((v) => v.key)],
        seasons: seasonList,
        search: canSearch && search,
        anime,
      }),
      { onSuccess: setDone },
    );
  };

  const facet = facetOf(target.kind, anime);
  return (
    <Modal
      label={`Add ${target.name}`}
      onClose={onClose}
      testid="add-sheet"
      className="z-[70]"
      backdropClassName="bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelSlot="sheet-content"
      panelClassName="absolute inset-x-0 bottom-0 flex max-h-[92dvh] flex-col rounded-t-[8px] border-t border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-8 sm:inset-y-0 sm:left-auto sm:right-0 sm:max-h-none sm:w-[min(460px,100vw)] sm:rounded-none sm:border-l sm:border-t-0 sm:slide-in-from-bottom-0 sm:slide-in-from-right-8"
    >
        <div className="relative flex gap-4 border-b border-[var(--line)] p-4 pr-12">
          <Poster src={target.poster} title={target.name} className="relative aspect-[2/3] w-[72px] shrink-0 rounded-[3px] ring-1 ring-[var(--line)]" />
          <div className="min-w-0">
            <div className="mb-1.5 flex flex-wrap items-center gap-1.5">
              <FacetPill facet={facet} />
              {inLibrary ? <Badge tone="positive">In library</Badge> : null}
            </div>
            <h2 className="text-lg font-semibold leading-tight text-[var(--ink)] [overflow-wrap:anywhere]">
              {target.name}
              {target.year ? <span className="font-normal text-[var(--ink-3)]"> ({target.year})</span> : null}
            </h2>
            {target.overview ? <p className="mt-1 line-clamp-3 text-xs text-[var(--ink-3)]">{target.overview}</p> : null}
          </div>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3">
            <X className="size-4" />
          </Button>
        </div>

        {done ? (
          <Done result={done} onClose={onClose} />
        ) : (
          <>
            <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-4">
              {opts.isPending ? (
                <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
                  <Loader2 className="size-4 animate-spin" /> Loading libraries…
                </div>
              ) : opts.error ? (
                <p className="text-sm text-[var(--bad-ink)]">{String((opts.error as Error).message)}</p>
              ) : o ? (
                <>
                  {!inLibrary ? (
                    <CheckRow checked={anime} onChange={(v) => { setAnime(v); setPicked({}); }} testid="add-anime">
                      <span className="block text-sm font-medium">Anime</span>
                      <span className="block text-xs text-[var(--ink-3)]">Anime titles get the anime libraries.{target.anime ? " TMDb says Japanese animation." : ""}</span>
                    </CheckRow>
                  ) : null}
                  <section>
                    <h3 className="mb-1 text-[13px] font-semibold text-[var(--ink-2)]">Add to these libraries</h3>
                    <DownloadsOffNotice off={globalOff} canManage={perms.admin} className="mb-2" />
                    <div className="border-t border-[var(--line)]" data-testid="add-versions">
                      {fitting.map((v) => (
                        <LibraryRow key={v.key} v={v} checked={isOn(v)} disabled={v.on} admin={perms.admin} onChange={(c) => setPicked((p) => ({ ...p, [v.key]: c }))} note={v.on ? "Already in this library" : undefined} />
                      ))}
                      {fitting.length === 0 ? <p className="text-sm text-muted-foreground">No library of the policy fits this title; pick one below.</p> : null}
                    </div>
                    {other.length > 0 ? (
                      <div className="mt-2">
                        <button type="button" className="text-xs font-medium text-[var(--accent-ink)] hover:underline" onClick={() => setShowOther((s) => !s)}>
                          {showOther ? "Hide" : "Show"} other libraries ({other.length})
                        </button>
                        {showOther ? (
                          <div className="mt-2 border-t border-[var(--line)]">
                            {other.map((v) => (
                              <LibraryRow key={v.key} v={v} checked={isOn(v)} admin={perms.admin} onChange={(c) => setPicked((p) => ({ ...p, [v.key]: c }))} note={v.anime ? "For anime titles" : "Not for anime titles"} />
                            ))}
                          </div>
                        ) : null}
                      </div>
                    ) : null}
                  </section>
                  {o.kind === "series" ? (
                    <section>
                      <h3 className="mb-2 text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Seasons</h3>
                      {epKnown ? (
                        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="add-seasons">
                          {(o.seasons ?? []).map((s) => (
                            <CheckRow key={s.season} checked={seasonOn(s.season, s.monitored ?? true)} onChange={(c) => setSeasons((p) => ({ ...p, [s.season]: c }))}>
                              <span className="text-sm font-medium">{seasonLabel(s.season)}</span>
                              <span className="ml-1.5 text-xs text-[var(--ink-3)]">{s.episodes} ep.</span>
                            </CheckRow>
                          ))}
                        </div>
                      ) : (
                        <p className="rounded-[5px] border border-dashed border-[var(--line)] px-3 py-2.5 text-xs leading-5 text-[var(--ink-3)]" data-testid="add-seasons-note">
                          {(o.seasons ?? []).length > 0
                            ? `${(o.seasons ?? []).filter((s) => s.season > 0).length} seasons on TMDb. `
                            : ""}
                          Every season is monitored. Season choices apply once the library knows the episodes; change them on the title page then.
                        </p>
                      )}
                    </section>
                  ) : null}
                  <section>
                    {canSearch ? (
                      <SwitchRow
                        on={search}
                        onChange={setSearch}
                        title="Search now"
                        detail={`Search the indexers for ${searchable.join(", ")} right after adding.`}
                        testid="add-search-now"
                      />
                    ) : globalOff ? null : (
                      <p className="flex items-start gap-2 text-xs leading-5 text-[var(--ink-3)]" data-testid="add-no-search">
                        <Search className="mt-0.5 size-3.5 shrink-0" />
                        No ticked library downloads now (see each library's state), so adding searches nothing; the wanted search picks them up once they download.
                      </p>
                    )}
                  </section>
                </>
              ) : null}
            </div>
            <div className="flex items-center justify-between gap-3 border-t border-[var(--line)] p-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
              {add.error ? (
                <span className="flex min-w-0 items-center gap-1.5 text-xs text-[var(--bad-ink)]">
                  <AlertTriangle className="size-3.5 shrink-0" />
                  <span className="truncate">{add.error instanceof ApiError ? add.error.message : "Could not add"}</span>
                </span>
              ) : (
                <span className="min-w-0 text-xs text-[var(--ink-3)]" data-testid="add-count">
                  {chosen.length > 0 || seasonsChanged ? <>{selected.length} {selected.length === 1 ? "library" : "libraries"} · <strong className="font-semibold text-[var(--ink)]">{willDownload} will download</strong></> : "Nothing new ticked"}
                </span>
              )}
              <div className="flex shrink-0 gap-2">
                <Button variant="ghost" onClick={onClose}>Cancel</Button>
                <Button onClick={submit} disabled={!o || (chosen.length === 0 && !seasonsChanged) || add.isPending} data-testid="add-submit">
                  {add.isPending ? <Loader2 className="animate-spin" /> : <Plus />}
                  {!inLibrary ? "Add" : chosen.length > 0 || o?.kind !== "series" ? "Add libraries" : "Save seasons"}
                </Button>
              </div>
            </div>
          </>
        )}
    </Modal>
  );
}

function Done({ result, onClose }: { result: AddResult; onClose: () => void }) {
  const t = result.title;
  // The submit button is gone: focus the result, so it is read out and Tab starts here.
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => box.current?.focus({ preventScroll: true }), []);
  return (
    <div ref={box} tabIndex={-1} className="flex-1 space-y-4 overflow-y-auto p-4 outline-none" data-testid="add-done" role="status">
      <div className="flex items-start gap-3 rounded-[5px] border border-[var(--th-success-border)] bg-[var(--ok-soft)] p-3 text-sm text-[var(--ok-ink)]">
        <CheckCircle2 className="mt-0.5 size-4 shrink-0" />
        <div>
          <div className="font-semibold">{result.created ? `${t.name} is in the library` : `${t.name} updated`}</div>
          <div className="mt-0.5 opacity-90">
            {result.new.length > 0 ? `Turned on: ${result.new.join(", ")}. New libraries start as wanted.` : "Saved."}
          </div>
        </div>
      </div>
      {result.search ? (
        <p className={cn("rounded-[5px] border px-3 py-2.5 text-xs", result.search.started ? "border-[var(--th-info-border)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line)] text-[var(--ink-3)]")} data-testid="add-search-result">
          {result.search.message}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        {t.versions.map((v) => <VersionTag key={v.key} k={v.key ?? ""} />)}
      </div>
      <div className="flex gap-2">
        <Button asChild>
          <Link to={`/title/${t.id}`} onClick={onClose}>
            Open title <ArrowUpRight />
          </Link>
        </Button>
        <Button variant="ghost" onClick={onClose}>Close</Button>
      </div>
    </div>
  );
}
