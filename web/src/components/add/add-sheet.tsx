import { usePerms } from "@/api/library";
// The add sheet: versions to turn on (every version that fits the title ticked), seasons
// for a series whose episodes the library knows, a new series' preset or its chosen seasons (the series' own seasons,
// numbered as its episodes will be), and the footer's "Add" and "Add and search", the second offered only
// when a search would run (a ticked version while Pause is off). It posts to POST /titles, the same add path as the arr views.
// A side sheet on desktop and a bottom sheet on phones, in our Modal (components/ui/modal.tsx: focus trap, inert page,
// focus restore, shared scroll lock), since Radix Dialog's scroll lock injects a <style> element the CSP refuses. Layout follows Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/root/
// add-to-catalog-dialog.tsx @ 2ef7757c, GPL-3.0 (poster header, option rows, footer actions) and Seerr's RequestModal
// (MIT): the season list with a toggle per season. Each library says what it fetches (its profile's preferred quality)
// and whether the title is already there unmonitored (the add then monitors it again). After the add the sheet closes
// on the title page, and a toast says what was turned on and what is searched, by library name.
import { AlertTriangle, Loader2, Plus, Search, X } from "lucide-react";
import { useId, useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { toast } from "@/components/toaster";
import { ApiError } from "@/api/client";
import { useAddOptions, useAddTitle, type AddResult, type AddTarget, type VersionOption } from "@/api/add";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { globalDownloadsOff } from "@/lib/version-grab";
import { FacetPill, facetOf, Poster } from "@/components/search/search-results";
import { Modal } from "@/components/ui/modal";
import { Select } from "@/components/ui/select";
import { MINIMUM_AVAILABILITY, SERIES_TYPES } from "@/components/title/title-choices";
import { addRequest, libraryTitleId, type AddChoice } from "./add-request";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useLibraryName } from "@/lib/library-names";
import { nameWithoutYear } from "@/lib/format";
import { readableCode } from "@/lib/code-words";
import { syncedBy } from "@/lib/sync-groups";

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

/** A switch row: the shared Switch, with the whole row as its label. */
function SwitchRow({ on, onChange, title, detail, testid }: { on: boolean; onChange: (v: boolean) => void; title: string; detail?: string; testid?: string }) {
  const id = useId();
  return (
    <label className="flex w-full cursor-pointer items-center gap-3 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2.5 transition-colors hover:bg-[var(--hover)]">
      <span className="min-w-0 flex-1">
        <span id={`${id}-t`} className="block text-sm font-medium text-[var(--ink)]">{title}</span>
        {detail ? <span id={`${id}-d`} className="block text-xs text-[var(--ink-3)]">{detail}</span> : null}
      </span>
      <Switch checked={on} onCheckedChange={onChange} aria-labelledby={`${id}-t`} aria-describedby={detail ? `${id}-d` : undefined} data-testid={testid} />
    </label>
  );
}

/** One library in the add drawer: the checkbox, then identity (name, key, kind) and the explanation on
 * their own wrapping rows, then the library state, so long names and narrow phones never overlap. */
function LibraryRow({ v, checked, disabled, onChange, note, admin, syncedWith }: {
  v: VersionOption;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  note?: string;
  admin: boolean;
  /** The ticked library this one is added with because they are kept in sync: shown ticked and disabled. */
  syncedWith?: string;
}) {
  const id = useId();
  const nameOf = useLibraryName();
  return (
    <div data-testid={`add-version-${v.key}`} data-checked={checked || undefined} data-synced={syncedWith ? true : undefined} className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 border-b border-[var(--line)] py-2.5 last:border-b-0">
      <Checkbox id={id} checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} aria-describedby={`${id}-state`} className="mt-0.5" />
      <div className="min-w-0 space-y-1">
        <label htmlFor={id} className={cn("flex flex-wrap items-baseline gap-x-2 gap-y-0.5", !disabled && "cursor-pointer")}>
          <span className="text-sm font-medium text-[var(--ink)] [overflow-wrap:anywhere]" title={admin ? v.key : undefined}>{v.label || nameOf(v.key)}</span>
        </label>
        {fetchesWords(v) ? <p className="text-xs text-[var(--ink-2)]" title={admin && v.profile ? `Profile key ${v.profile}` : undefined} data-testid="add-fetches">{fetchesWords(v)}</p> : null}
        {syncedWith ? <p className="text-xs text-[var(--ink-3)]" data-testid="add-synced-with">in sync with {syncedWith}{v.removed ? "; it was removed and comes back" : ""}</p> : note ? <p className="text-xs text-[var(--ink-3)]">{note}</p> : null}
        <div id={`${id}-state`}><VersionStatus state={v.grab_state} settingsKey={v.key} canManage={admin} quiet /></div>
      </div>
    </div>
  );
}

/** What a library fetches, from its profile, by the profile's name: "Profile Remux 2160p: prefers Remux-2160p,
 *  upgrades on", or "…: prefers WEBDL-1080p, no upgrades" (the profile's switch or the library's own). */
export function fetchesWords(v: Pick<VersionOption, "cutoff" | "upgrades" | "profile">): string {
  if (!v.cutoff) return "";
  const name = v.profile ? `Profile ${readableCode(v.profile)}: prefers` : "Prefers";
  return `${name} ${v.cutoff}, ${v.upgrades ? "upgrades on" : "no upgrades"}`;
}

/** The note under a library already on the title: monitored ("Already in this library"), or held but not monitored. */
export function heldNote(v: Pick<VersionOption, "on" | "held">): string | undefined {
  if (v.on) return "Already in this library";
  if (v.held) return "Already here, not monitored: ticked, it is monitored again";
  return undefined;
}

function seasonCount(n: number) {
  return n === 1 ? "1 season" : `${n} seasons`;
}

function seasonLabel(n: number) {
  return n === 0 ? "Specials" : `Season ${n}`;
}

/** A labelled select in the drawer's row shape. */
function SelectRow({ label, detail, value, onChange, options, testid }: { label: string; detail?: string; value: string; onChange: (v: string) => void; options: readonly (readonly [string, string])[]; testid?: string }) {
  const id = useId();
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2.5">
      <span className="min-w-0 flex-1">
        <label htmlFor={id} className="block text-sm font-medium text-[var(--ink)]">{label}</label>
        {detail ? <span id={`${id}-d`} className="block text-xs text-[var(--ink-3)]">{detail}</span> : null}
      </span>
      <Select id={id} className="w-auto" value={value} onChange={(e) => onChange(e.target.value)} aria-describedby={detail ? `${id}-d` : undefined} data-testid={testid}>
        {options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
      </Select>
    </div>
  );
}

type MonitorPreset = NonNullable<AddChoice["monitor"]>;
/** The Monitor select's value for a new series' own seasons, ticked one by one: no preset is sent. */
const CHOOSE_SEASONS = "choose";
/** The presets offered for a new series (Sonarr's add options; existing and recent are left out to keep it short). */
const MONITOR_PRESETS: readonly (readonly [MonitorPreset, string])[] = [
  ["all", "All episodes"], ["future", "Future episodes"], ["missing", "Missing episodes"], ["firstSeason", "First season"],
  ["lastSeason", "Latest season"], ["pilot", "Pilot only"], ["none", "None"],
];

export function AddSheet({ target, onClose }: { target: AddTarget; onClose: () => void }) {
  const perms = usePerms();
  const [anime, setAnime] = useState(Boolean(target.anime));
  // Unset until picked: the select then shows what applies without a choice, and the request carries none.
  const [seriesType, setSeriesType] = useState<AddChoice["seriesType"]>();
  // The libraries offered ticked are the ones acquisition will search the title in: it reads a series as anime by
  // the Anime mark or the type picked here.
  const opts = useAddOptions(target, anime || seriesType === "anime", true);
  const inLibrary = libraryTitleId(target, opts.data) !== undefined;
  // A title every copy of which was removed is added by its title id (which restores it), but reads as a new add.
  const current = inLibrary && opts.data?.in_library !== false;
  const add = useAddTitle();
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [seasons, setSeasons] = useState<Record<number, boolean>>({});
  // "choose": a new series' own seasons, ticked one by one (CHOOSE_SEASONS).
  const [monitor, setMonitor] = useState<MonitorPreset | "" | typeof CHOOSE_SEASONS>("");
  const [monitorNew, setMonitorNew] = useState(true);
  const [minimum, setMinimum] = useState<AddChoice["minimumAvailability"]>();
  const [monitored, setMonitored] = useState(true);
  const [showOther, setShowOther] = useState(false);
  const navigate = useNavigate();
  const nameOf = useLibraryName();
  // Every ticked new library is one the title already holds unmonitored: the add only monitors them again.
  const monitorOnly = (lib: VersionOption[]) => lib.length > 0 && lib.every((v) => v.held);

  const o = opts.data;
  const versions = useMemo(() => o?.versions ?? [], [o]);
  // Ticked: what the user set, else the default (fits the title), else already on.
  const isOn = (v: VersionOption) => (v.on ? true : picked[v.key] ?? v.default);
  const chosen = versions.filter((v) => !v.on && isOn(v)).map((v) => v.key);
  // The libraries kept in sync with a ticked one that gain a copy with it (the server's answer): shown ticked and
  // disabled, and left out of the request (the server adds them).
  const synced = syncedBy(versions, (k) => chosen.includes(k));
  const isShown = (v: VersionOption) => isOn(v) || synced[v.key] !== undefined;
  const syncedWith = (v: VersionOption) => {
    const by = synced[v.key];
    return by === undefined ? undefined : versions.find((x) => x.key === by)?.label || nameOf(by);
  };
  const fitting = versions.filter((v) => v.fits || v.on);
  const other = versions.filter((v) => !v.fits && !v.on);
  const selected = versions.filter(isShown);
  // The summary counts what this click adds (the newly ticked libraries); a change of seasons alone, the libraries it
  // applies to.
  const counted = chosen.length > 0 ? selected.filter((v) => !v.on) : selected;
  const globalOff = globalDownloadsOff(versions.map((v) => v.grab_state));
  const canSearch = selected.some((v) => v.searchable);
  const seasonOn = (n: number, def: boolean) => seasons[n] ?? def;
  // A season's tick before any click: its state in the library, else on, specials aside.
  const seasonDefault = (s: { season: number; monitored?: boolean }) => s.monitored ?? s.season > 0;
  const seasonsChanged = (o?.seasons ?? []).some((s) => seasons[s.season] !== undefined && seasons[s.season] !== seasonDefault(s));
  const epKnown = Boolean(o?.episodes_known);
  // A new series whose own seasons the options carry (seasons without known episodes) can choose them; a preset
  // decides otherwise.
  const ownSeasons = !current && !epKnown && (o?.seasons ?? []).length > 0;
  const choosing = ownSeasons && monitor === CHOOSE_SEASONS;
  const preset = monitor && monitor !== CHOOSE_SEASONS ? monitor : undefined;
  // Two buttons, "Add" and "Add and search", when something new is ticked and a ticked library can search; a change of
  // seasons alone keeps its one "Save seasons".
  // A new movie added unmonitored is not searched: only "Add" is offered.
  const unmonitored = o?.kind === "movie" && !current && !monitored;
  const searchOffered = !!o && canSearch && chosen.length > 0 && !unmonitored;
  // The type a new series has without a choice: anime for an anime title, else standard.
  const typeDefault = anime ? "anime" : "standard";

  const submit = (search: boolean) => {
    if (!o) return;
    // Chosen seasons send every shown season's choice, specials included, and no preset.
    const seasonList = o.kind === "series" && (epKnown || choosing) ? (o.seasons ?? []).map((s) => ({ season: s.season, monitored: seasonOn(s.season, seasonDefault(s)) })) : undefined;
    add.mutate(
      addRequest(target, o, {
        versions: [...chosen, ...versions.filter((v) => v.on).map((v) => v.key)],
        seasons: seasonList,
        search: canSearch && search,
        anime,
        monitor: preset,
        monitorNew,
        seriesType: seriesType === typeDefault ? undefined : seriesType,
        minimumAvailability: chosen.length > 0 ? minimum : undefined,
        monitored: o.kind === "movie" && !current ? monitored : undefined,
      }),
      { onSuccess: (r) => done(r) },
    );
  };

  // The add's outcome: the sheet closes on the title page and a toast says what happened, by library name: the
  // libraries the person chose first (a removed copy brought back included), then the ones in sync with them.
  const done = (r: AddResult) => {
    const partners = r.synced ?? [];
    const named = [...new Set([...chosen, ...r.new])].filter((k) => !partners.includes(k));
    const turned = named.map((k) => nameOf(k)).join(", ");
    const inSync = partners.map((k) => nameOf(k)).join(", ");
    const said = turned ? `Turned on: ${turned}.${inSync ? ` ${inSync} too (in sync).` : ""}` : inSync ? `Turned on: ${inSync} (in sync).` : "Saved.";
    const body = [said, r.search?.message ? r.search.message.replace(/([^.!?])$/, "$1.") : ""].filter(Boolean).join(" ");
    toast({ tone: r.search && !r.search.started ? "warn" : "ok", title: r.created ? `${r.title.name} is in the library` : `${r.title.name} updated`, body, image: target.poster });
    onClose();
    navigate(`/title/${r.title.id}`);
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
              {current ? <Badge tone="positive">In library</Badge> : null}
            </div>
            <h2 className="text-lg font-semibold leading-tight text-[var(--ink)] [overflow-wrap:anywhere]">
              {nameWithoutYear(target.name, target.year)}
              {target.year ? <span className="font-normal text-[var(--ink-3)]"> ({target.year})</span> : null}
            </h2>
            {target.overview ? <p className="mt-1 line-clamp-3 text-xs text-[var(--ink-3)]">{target.overview}</p> : null}
          </div>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3">
            <X className="size-4" />
          </Button>
        </div>

        {(
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
                  {!current ? (
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
                        <LibraryRow key={v.key} v={v} checked={isShown(v)} disabled={v.on || syncedWith(v) !== undefined} admin={perms.admin} onChange={(c) => setPicked((p) => ({ ...p, [v.key]: c }))} note={heldNote(v)}
                          syncedWith={syncedWith(v)} />
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
                              <LibraryRow key={v.key} v={v} checked={isShown(v)} disabled={syncedWith(v) !== undefined} admin={perms.admin} onChange={(c) => setPicked((p) => ({ ...p, [v.key]: c }))} note={v.anime ? "For anime titles" : "Not for anime titles"}
                                syncedWith={syncedWith(v)} />
                            ))}
                          </div>
                        ) : null}
                      </div>
                    ) : null}
                  </section>
                  {o.kind === "series" ? (
                    <section>
                      <h3 className="mb-2 text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Seasons</h3>
                      {!current ? (
                        <div className="mb-2">
                          <SelectRow
                            label="Monitor"
                            detail={preset ? `This choice decides which episodes are monitored${epKnown ? "" : ", once the library knows the episodes"}.` : undefined}
                            value={monitor}
                            onChange={(v) => setMonitor(v as typeof monitor)}
                            options={[["", epKnown ? "Seasons I pick below" : "Every season"], ...(ownSeasons ? [[CHOOSE_SEASONS, "Choose seasons"] as const] : []), ...MONITOR_PRESETS]}
                            testid="add-monitor"
                          />
                        </div>
                      ) : null}
                      {choosing || (epKnown && !(preset && !current)) ? (
                        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="add-seasons">
                          {(o.seasons ?? []).map((s) => (
                            <CheckRow key={s.season} checked={seasonOn(s.season, seasonDefault(s))} onChange={(c) => setSeasons((p) => ({ ...p, [s.season]: c }))}>
                              <span className="text-sm font-medium">{seasonLabel(s.season)}</span>
                              {s.episodes ? <span className="ml-1.5 text-xs text-[var(--ink-3)]">{s.episodes} ep.</span> : null}
                            </CheckRow>
                          ))}
                        </div>
                      ) : preset && !current ? null : (
                        <p className="rounded-[5px] border border-dashed border-[var(--line)] px-3 py-2.5 text-xs leading-5 text-[var(--ink-3)]" data-testid="add-seasons-note">
                          {ownSeasons
                            ? `${seasonCount((o.seasons ?? []).filter((s) => s.season > 0).length)}. Every one is monitored, specials aside. To pick them, use Choose seasons above.`
                            : "Every season is monitored. Season choices apply once the library knows the episodes; change them on the title page then."}
                        </p>
                      )}
                      {!current ? (
                        <div className="mt-2 space-y-2">
                          <SwitchRow
                            on={monitorNew}
                            onChange={setMonitorNew}
                            title="Monitor seasons that appear later"
                            detail="Off: a season announced after the add stays unmonitored."
                            testid="add-monitor-new"
                          />
                          <SelectRow
                            label="Series type"
                            detail="Daily shows are searched by air date, anime by absolute episode number."
                            value={seriesType ?? typeDefault}
                            onChange={(v) => { setSeriesType(v as AddChoice["seriesType"]); setPicked({}); }}
                            options={SERIES_TYPES}
                            testid="add-series-type"
                          />
                        </div>
                      ) : null}
                    </section>
                  ) : null}
                  {o.kind === "movie" && chosen.length > 0 ? (
                    <div className="space-y-2">
                      {!current ? (
                        <SwitchRow
                          on={monitored}
                          onChange={setMonitored}
                          title="Monitored"
                          detail={monitored ? undefined : "Theatarr won't search for or download this movie automatically until you monitor it."}
                          testid="add-monitored"
                        />
                      ) : null}
                      <SelectRow
                        label="Minimum availability"
                        detail={`When the movie counts as out and is searched${current ? ", for the libraries added now" : ""}. Library default keeps each library's own setting.`}
                        value={minimum ?? ""}
                        onChange={(v) => setMinimum((v || undefined) as AddChoice["minimumAvailability"])}
                        options={[["", "Library default"], ...MINIMUM_AVAILABILITY]}
                        testid="add-minimum-availability"
                      />
                    </div>
                  ) : null}
                  {canSearch || globalOff || unmonitored ? null : (
                    <section>
                      <p className="flex items-start gap-2 text-xs leading-5 text-[var(--ink-3)]" data-testid="add-no-search">
                        <Search className="mt-0.5 size-3.5 shrink-0" />
                        No ticked library downloads now (see each library's state), so adding searches nothing; the wanted search picks them up once they download.
                      </p>
                    </section>
                  )}
                </>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--line)] p-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
              {add.error ? (
                <span className="flex min-w-0 items-center gap-1.5 text-xs text-[var(--bad-ink)]">
                  <AlertTriangle className="size-3.5 shrink-0" />
                  <span className="truncate">{add.error instanceof ApiError ? add.error.message : "Could not add"}</span>
                </span>
              ) : (
                <span className="min-w-0 text-xs text-[var(--ink-3)]" data-testid="add-count">
                  {chosen.length > 0 || seasonsChanged ? `${counted.length} ${counted.length === 1 ? "library" : "libraries"}` : "Nothing new ticked"}
                </span>
              )}
              <div className="ml-auto flex min-w-0 flex-wrap justify-end gap-2">
                <Button variant="ghost" onClick={onClose}>Cancel</Button>
                <Button variant={searchOffered ? "outline" : "default"} onClick={() => submit(false)} disabled={!o || (chosen.length === 0 && !seasonsChanged) || add.isPending} data-testid="add-submit">
                  {add.isPending && !add.variables?.search ? <Loader2 className="animate-spin" /> : <Plus />}
                  {!current ? "Add" : chosen.length > 0 && monitorOnly(versions.filter((v) => chosen.includes(v.key))) ? "Monitor" : chosen.length > 0 || o?.kind !== "series" ? "Add libraries" : "Save seasons"}
                </Button>
                {searchOffered ? (
                  <Button onClick={() => submit(true)} disabled={add.isPending} data-testid="add-submit-search">
                    {add.isPending && add.variables?.search ? <Loader2 className="animate-spin" /> : <Search />}
                    {!current ? "Add and search" : monitorOnly(versions.filter((v) => chosen.includes(v.key))) ? "Monitor and search" : "Add libraries and search"}
                  </Button>
                ) : null}
              </div>
            </div>
          </>
        )}
    </Modal>
  );
}
