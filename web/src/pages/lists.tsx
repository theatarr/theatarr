// Settings › Import lists (/admin/settings/import-lists; /collections/lists redirects here): the import lists
// (docs/design/arrview.md, Lists), as Radarr's Settings › Import Lists: TMDb lists and discover rules, JSON lists, and
// Radarr or Sonarr instances whose titles are added, each for movies or series; then the list exclusions. Managers open it too: their Admin group links it, as they have no other Settings. An enabled list adds its new titles to its
// libraries at each sync and on Sync now; search on add starts the usual add-time search, which the download router
// gates. A list an arr client made is shown with its view and stays that client's. Managers and admins (GET/POST
// /lists, POST /lists/check, PUT/DELETE /lists/{id}, POST /lists/{id}/sync and /preview).
import { useState } from "react";
import { ListVideo, Loader2, Plus, X } from "lucide-react";
import { useAuth } from "@/api/auth";
import { useDebounced } from "@/api/add";
import { permsOf } from "@/api/library";
import { listIDFrom, normalListURL, useListCheck, useListExclusions, useListLookup, useListMutations, useLists, useRemoveExclusion, type ListAdd, type ListPreview, type MovieList } from "@/api/lists";
import { AVAILABILITY } from "@/components/collections/add-collection";
import { SettingsPicker } from "@/components/settings/settings-picker";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { fmtAgo, titleWithYear } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Forbidden } from "./library";

type ListType = MovieList["list_type"];
type Media = MovieList["kind"];
type Monitor = NonNullable<ListAdd["monitor"]>;
const MONITOR: Record<ListType, readonly (readonly [Monitor, string])[]> = {
  tmdb: [["movieOnly", "Movies"], ["movieAndCollection", "Movies and their collections"], ["none", "Nothing (added unmonitored)"]],
  tmdb_discover: [["movieOnly", "Movies"], ["movieAndCollection", "Movies and their collections"], ["none", "Nothing (added unmonitored)"]],
  json: [["movieOnly", "Movies"], ["movieAndCollection", "Movies and their collections"], ["none", "Nothing (added unmonitored)"]],
  radarr: [["movieOnly", "Movies"], ["none", "Nothing (added unmonitored)"]],
  sonarr: [["all", "Series"], ["none", "Nothing (added unmonitored)"]],
};
const SOURCES = [["tmdb", "TMDb list"], ["tmdb_discover", "TMDb discover"], ["json", "JSON list"], ["radarr", "Radarr"], ["sonarr", "Sonarr"]] as const;
const APP: Record<string, string> = { radarr: "Radarr", sonarr: "Sonarr" };
const MEDIA = [["movie", "Movies"], ["series", "Series"]] as const;
/** The monitor choices of a list: a series list's are a Sonarr list's. */
const monitorsOf = (t: ListType, media: Media) => MONITOR[media === "series" ? "sonarr" : t];
/** What a list adds: a Sonarr list series and a Radarr list movies, whatever media says. */
const mediaOf = (t: ListType, media: Media): Media => t === "sonarr" ? "series" : t === "radarr" ? "movie" : media;
type Mutations = ReturnType<typeof useListMutations>;
type Rule = NonNullable<MovieList["rule"]>;

/** TMDb's genres (stable ids), for a discover rule. */
const GENRES: Record<Media, readonly (readonly [number, string])[]> = {
  movie: [[28, "Action"], [12, "Adventure"], [16, "Animation"], [35, "Comedy"], [80, "Crime"], [99, "Documentary"], [18, "Drama"], [10751, "Family"],
    [14, "Fantasy"], [36, "History"], [27, "Horror"], [10402, "Music"], [9648, "Mystery"], [10749, "Romance"], [878, "Science Fiction"], [10770, "TV Movie"],
    [53, "Thriller"], [10752, "War"], [37, "Western"]],
  series: [[10759, "Action & Adventure"], [16, "Animation"], [35, "Comedy"], [80, "Crime"], [99, "Documentary"], [18, "Drama"], [10751, "Family"],
    [10762, "Kids"], [9648, "Mystery"], [10763, "News"], [10764, "Reality"], [10765, "Sci-Fi & Fantasy"], [10766, "Soap"], [10767, "Talk"],
    [10768, "War & Politics"], [37, "Western"]],
};

/** A discover rule in one line: "2010–2020 · rating 7+ · 200+ votes · Animation · ja · the 100 most popular". */
export function ruleWords(r: Rule, media: Media): string {
  const years = r.year_from && r.year_to ? `${r.year_from}–${r.year_to}` : r.year_from ? `from ${r.year_from}` : r.year_to ? `to ${r.year_to}` : "";
  const genres = (r.genres ?? []).map(g => GENRES[media].find(([id]) => id === g)?.[1] ?? `genre ${g}`).join(" or ");
  return [years, r.min_rating ? `rating ${r.min_rating}+` : "", r.min_votes ? `${r.min_votes}+ votes` : "", genres, r.language ?? "",
    `the ${r.limit} most popular`].filter(Boolean).join(" · ");
}

/** The discover rule's fields; numbers as typed ("" for none). */
type RuleForm = { from: string; to: string; rating: string; votes: string; genres: number[]; language: string; limit: string };
const ruleForm = (r?: Rule | null): RuleForm => ({ from: r?.year_from ? String(r.year_from) : "", to: r?.year_to ? String(r.year_to) : "",
  rating: r?.min_rating ? String(r.min_rating) : "", votes: String(r?.min_votes ?? 200), genres: r?.genres ?? [], language: r?.language ?? "", limit: String(r?.limit ?? 100) });
/** An empty "Minimum votes" is left out, so the server's default (200) applies; a typed 0 is no minimum. */
const ruleBody = (f: RuleForm) => ({ year_from: Number(f.from) || 0, year_to: Number(f.to) || 0, min_rating: Number(f.rating) || 0,
  ...(f.votes.trim() === "" ? {} : { min_votes: Number(f.votes) || 0 }), genres: f.genres, language: f.language.trim(), limit: Number(f.limit) || 100 });

function DiscoverFields({ f, set, media }: { f: RuleForm; set: (f: RuleForm) => void; media: Media }) {
  const num = (label: string, key: "from" | "to" | "rating" | "votes" | "limit", placeholder: string, step?: string) =>
    <label className="flex items-center gap-2">{label}
      <Input aria-label={label} className="w-24" type="number" inputMode="numeric" step={step} placeholder={placeholder} value={f[key]} onChange={e => set({ ...f, [key]: e.target.value })} />
    </label>;
  return <fieldset className="space-y-3" data-testid="discover-fields">
    <legend className="mb-1.5 font-medium text-[var(--ink)]">Rule</legend>
    <div className="flex flex-wrap items-center gap-4">
      {num("Year from", "from", "Any")}
      {num("Year to", "to", "Any")}
      {num("Minimum rating", "rating", "0", "0.1")}
      {num("Minimum votes", "votes", "200")}
    </div>
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="mr-1 text-[var(--ink)]">Genres, any of</span>
      {GENRES[media].map(([id, label]) => {
        const on = f.genres.includes(id);
        return <button key={id} type="button" aria-pressed={on} onClick={() => set({ ...f, genres: on ? f.genres.filter(g => g !== id) : [...f.genres, id] })}
          className={cn("rounded-[3px] border px-2 py-0.5 text-xs", on ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line-2)] text-[var(--ink-3)]")}>{label}</button>;
      })}
    </div>
    <div className="flex flex-wrap items-center gap-4">
      <label className="flex items-center gap-2">Original language
        <Input aria-label="Original language" className="w-20" maxLength={2} placeholder="Any" value={f.language} onChange={e => set({ ...f, language: e.target.value })} />
      </label>
      {num("Take the first", "limit", "100")}
    </div>
    <p className="text-xs text-muted-foreground">TMDb's most popular titles that match, read again at each sync: at most 500. The language is a two-letter code, such as en or ja.</p>
  </fieldset>;
}

/** Add a list (no list given) or edit one. */
function ListDialog({ list, libraries, seriesLibraries, canSync, m, onClose }: { list?: MovieList; libraries: Record<string, string>; seriesLibraries: Record<string, string>; canSync: boolean; m: Mutations; onClose: () => void }) {
  const [listType, setListType] = useState<ListType>(list?.list_type ?? "tmdb");
  const arr = listType === "radarr" || listType === "sonarr";
  const discover = listType === "tmdb_discover";
  const jsonList = listType === "json";
  const [rule, setRule] = useState<RuleForm>(ruleForm(list?.rule));
  const [pickedMedia, setPickedMedia] = useState<Media>(list?.kind ?? "movie");
  const media = mediaOf(listType, pickedMedia);
  const [source, setSource] = useState("");
  const [address, setAddress] = useState(list?.url ?? "");
  const [apiKey, setApiKey] = useState("");
  const check = useListCheck();
  const [name, setName] = useState(list?.name ?? "");
  const shown = media === "series" ? seriesLibraries : libraries;
  const keys = list ? list.libraries : Object.keys(shown);
  const [versions, setVersions] = useState<string[]>(list?.versions ?? (keys.length === 1 ? keys : []));
  const [monitor, setMonitor] = useState<Monitor>(list?.monitor ?? "movieOnly");
  const [searchOnAdd, setSearchOnAdd] = useState(list?.search_on_add ?? true);
  const [minimum, setMinimum] = useState<NonNullable<ListAdd["minimum_availability"]>>(list?.minimum_availability ?? "released");
  const [enabled, setEnabled] = useState(list?.enabled ?? true);
  const tmdbID = listIDFrom(useDebounced(source, 300));
  const lookup = useListLookup(list || !canSync ? 0 : tmdbID, media);
  const found = lookup.data;
  const title = name.trim() || (arr ? APP[listType] : discover ? "TMDb discover" : jsonList ? "JSON list" : found?.name) || "";
  const mutation = list ? m.set : m.add;
  const newAddress = list ? normalListURL(address) !== normalListURL(list.url ?? "") : true;
  const ready = title !== "" && versions.length > 0 && (arr ? address.trim() !== "" && (!newAddress || apiKey.trim() !== "")
    : jsonList ? address.trim() !== "" : list || discover ? true : listIDFrom(source) > 0 && !found?.added);
  const pick = (t: ListType, m: Media) => {
    setListType(t);
    setPickedMedia(m);
    const series = mediaOf(t, m) === "series";
    const k = Object.keys(series ? seriesLibraries : libraries);
    setVersions(k.length === 1 ? k : []);
    setMonitor(series ? "all" : "movieOnly");
    // A discover genre the media lacks would filter unseen: it goes.
    const genres = GENRES[mediaOf(t, m)];
    setRule(r => ({ ...r, genres: r.genres.filter(g => genres.some(([id]) => id === g)) }));
    check.reset();
  };
  const runCheck = () => check.mutate(list && !newAddress && apiKey.trim() === "" ? { id: list.id }
    : { list_type: listType as "radarr" | "sonarr", url: address, api_key: apiKey });
  const submit = () => {
    const body = { name: title, versions, monitor, search_on_add: searchOnAdd, ...(media === "series" ? {} : { minimum_availability: minimum }), enabled };
    const instance = arr ? { ...(newAddress ? { url: address } : {}), ...(apiKey.trim() ? { api_key: apiKey } : {}) }
      : jsonList && (!list || address.trim() !== list.url) ? { url: address.trim() } : {};
    if (list) m.set.mutate({ id: list.id, ...body, ...instance, ...(discover ? { rule: ruleBody(rule) } : {}) }, { onSuccess: onClose });
    else if (arr) m.add.mutate({ list_type: listType, ...body, ...instance }, { onSuccess: onClose });
    else if (jsonList) m.add.mutate({ list_type: listType, ...(media === "series" ? { media } : {}), ...body, ...instance }, { onSuccess: onClose });
    else if (discover) m.add.mutate({ list_type: listType, ...(media === "series" ? { media } : {}), rule: ruleBody(rule), ...body }, { onSuccess: onClose });
    else m.add.mutate({ tmdb_id: listIDFrom(source), ...(media === "series" ? { media } : {}), ...body }, { onSuccess: onClose });
  };
  const checked = check.data;
  const heading = list ? `Edit ${list.name}` : "Add list";
  return <Modal label={heading} onClose={onClose} testid="list-dialog"
    className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
    backdropClassName="absolute inset-0 bg-[var(--scrim)] animate-in fade-in-0"
    panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-xl sm:rounded-[8px]">
    <div className="relative border-b border-[var(--line)] p-4 pr-12">
      <h2 className="font-heading text-[22px] font-medium leading-tight text-[var(--ink)] [overflow-wrap:anywhere]">{heading}</h2>
      <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3"><X className="size-4" /></Button>
    </div>
    <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 text-sm">
      {list ? null : <label className="flex items-center gap-2"><span className="font-medium text-[var(--ink)]">Source</span>
        <Select className="w-auto" value={listType} onChange={e => pick(e.target.value as ListType, pickedMedia)}>
          {SOURCES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select>
      </label>}
      {list || arr ? null : <label className="flex items-center gap-2"><span className="font-medium text-[var(--ink)]">Media</span>
        <Select className="w-auto" aria-label="Media" value={media} onChange={e => pick(listType, e.target.value as Media)}>
          {MEDIA.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select>
      </label>}
      {arr ? <div className="space-y-4">
        <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">Address</span>
          <Input aria-label="Address" placeholder={listType === "sonarr" ? "http://192.168.1.10:8989" : "http://192.168.1.10:7878"} value={address} onChange={e => { setAddress(e.target.value); check.reset(); }} />
          <span className="block text-xs text-muted-foreground">The instance's address on your network.</span>
        </label>
        <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">API key</span>
          <Input aria-label="API key" type="password" autoComplete="off" placeholder={list?.api_key_set ? "Saved. Type a new key to replace it." : ""} value={apiKey} onChange={e => { setApiKey(e.target.value); check.reset(); }} />
          <span className="block text-xs text-muted-foreground">In the instance under Settings › General.</span>
        </label>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={runCheck} disabled={!canSync || address.trim() === "" || (newAddress && apiKey.trim() === "") || check.isPending}>{check.isPending ? <Loader2 className="animate-spin" aria-hidden /> : null}Check</Button>
          {!canSync ? <span className="text-xs text-muted-foreground" data-testid="list-check-off">TMDb is not set up, so the instance cannot be checked or synced yet.</span> : null}
          {checked ? <span className="text-xs text-[var(--ink)]" data-testid="list-checked">{checked.app} {checked.version}: {checked.titles.toLocaleString("en-US")} {listType === "sonarr" ? "series" : checked.titles === 1 ? "movie" : "movies"}.</span> : null}
        </div>
        {check.error ? <ErrorState error={check.error} /> : null}
        <p className="text-xs text-muted-foreground" data-testid="list-arr-note">Theatarr only reads this instance. It adds the titles it does not have yet to the libraries below, with their own monitoring and search settings. Files, monitoring changes and blocklists are not copied, and a title you remove in Theatarr is not added back. Theatarr does not report back to this instance: apps that ask it, such as Seerr, do not see what Theatarr has.</p>
      </div> : null}
      {list ? list.view ? <p className="text-xs text-muted-foreground" data-testid="list-view">Created through the {list.view} view: it stays that client's list, and only that view's libraries can be chosen. Automatic adding is {list.enable_auto ? "on" : "off"} there.</p> : null
        : listType !== "tmdb" ? null : <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">TMDb list</span>
          <Input aria-label="TMDb list id or URL" placeholder="List id, or its themoviedb.org address" value={source} onChange={e => setSource(e.target.value)} />
          {!canSync ? <span className="block text-xs text-muted-foreground">TMDb is not set up, so the list cannot be checked or synced yet.</span>
            : source.trim() && tmdbID === 0 && listIDFrom(source) === 0 ? <span className="block text-xs text-muted-foreground">Enter the number of a TMDb list, or paste its address (themoviedb.org/list/...).</span>
            : lookup.isFetching ? <span className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" aria-hidden />Looking the list up on TMDb</span>
            : lookup.error ? <ErrorState error={lookup.error} />
            : found ? <span className="block text-xs text-muted-foreground" data-testid="list-found">{found.added ? `${found.name} is already added for ${media === "series" ? "series" : "movies"}.` : `${found.name}: ${found.movies} ${found.movies === 1 ? "movie" : "movies"}${found.series ? `, ${found.series} series` : ""}.`}{found.description ? ` ${found.description}` : ""}</span> : null}
        </label>}
      {discover ? <DiscoverFields f={rule} set={setRule} media={media} /> : null}
      {jsonList ? <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">Address</span>
        <Input aria-label="JSON list address" placeholder="https://example.org/list.json" value={address} onChange={e => setAddress(e.target.value)} />
        <span className="block text-xs text-muted-foreground">An https address that answers a JSON array of entries with imdb_id or tmdb_id, such as the StevenLu lists; at most 1,000 entries. Enter the direct address: one that redirects is not followed.</span>
      </label> : null}
      <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">Name</span>
        <Input aria-label="List name" placeholder={arr ? APP[listType] : discover ? "TMDb discover" : found?.name ?? ""} value={name} onChange={e => setName(e.target.value)} />
      </label>
      <fieldset className="flex flex-wrap items-center gap-1.5">
        <legend className="mb-1.5 font-medium text-[var(--ink)]">Libraries</legend>
        {keys.map(key => {
          const on = versions.includes(key);
          return <button key={key} type="button" aria-pressed={on} title={key} onClick={() => setVersions(on ? versions.filter(k => k !== key) : [...versions, key])}
            className={cn("rounded-[3px] border px-2 py-0.5 text-xs", on ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line-2)] text-[var(--ink-3)]")}>{shown[key] ?? key}</button>;
        })}
        {keys.length === 0 ? <span className="text-xs text-muted-foreground">There is no {media === "series" ? "series" : "movie"} library to add to.</span>
          : arr ? <span className="block w-full text-xs text-muted-foreground">Each title goes to the chosen libraries that take it (anime to anime libraries).</span>
          : versions.length === 0 ? <span className="text-xs text-muted-foreground">Choose at least one. Each {media === "series" ? "series" : "movie"} goes to the chosen libraries that take it.</span> : null}
      </fieldset>
      <div className="flex flex-wrap items-center gap-4">
        <label className="flex items-center gap-2">Monitor
          <Select className="w-auto" value={monitor} onChange={e => setMonitor(e.target.value as typeof monitor)}>
            {monitorsOf(listType, media).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </Select>
        </label>
        {media === "series" ? null : <label className="flex items-center gap-2">Minimum availability
          <Select className="w-auto" value={minimum} onChange={e => setMinimum(e.target.value as typeof minimum)}>
            {AVAILABILITY.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            {minimum === "tba" ? <option value="tba">TBA</option> : null}
          </Select>
        </label>}
        <label className="flex items-center gap-2"><Checkbox checked={searchOnAdd} onChange={e => setSearchOnAdd(e.target.checked)} />Search on add</label>
        <label className="flex items-center gap-2"><Checkbox checked={enabled} onChange={e => setEnabled(e.target.checked)} />Enabled: sync at each list refresh</label>
      </div>
      {mutation.error ? <ErrorState error={mutation.error} /> : null}
    </div>
    <div className="flex justify-end gap-2 border-t border-[var(--line)] p-4">
      <Button variant="outline" onClick={onClose}>Cancel</Button>
      <Button onClick={submit} disabled={!ready || mutation.isPending}>{mutation.isPending ? <Loader2 className="animate-spin" aria-hidden /> : null}{list ? "Save" : "Add"}</Button>
    </div>
  </Modal>;
}

const titleName = (t: ListPreview["titles"][number]) => titleWithYear(t.name, t.year);

/** What Preview found: the counts in one sentence, then the first titles. */
function previewWords(p: ListPreview): string {
  const n = p.add_title + p.add_library;
  return `Sync now would add ${n} ${n === 1 ? "title" : "titles"} (${p.add_title} new, ${p.add_library} to libraries they are not in yet); ${p.present} ${p.present === 1 ? "is" : "are"} already there${p.excluded ? `, ${p.excluded} excluded` : ""}${p.skipped ? `, ${p.skipped} skipped (they fit none of the list's libraries)` : ""}.`;
}

function ListRow({ l, libraries, seriesLibraries, canSync, m, edit, remove, synced }: { l: MovieList; libraries: Record<string, string>; seriesLibraries: Record<string, string>; canSync: boolean; m: Mutations; edit: () => void; remove: () => void; synced: (words: string) => void }) {
  const syncing = m.sync.isPending && m.sync.variables === l.id;
  const previewing = m.preview.isPending && m.preview.variables === l.id;
  const preview = m.preview.variables === l.id ? m.preview.data : undefined;
  const adds = preview?.titles.filter(t => t.change !== "skipped") ?? [];
  const skipped = preview?.titles.filter(t => t.change === "skipped") ?? [];
  const busy = syncing || (m.set.isPending && m.set.variables?.id === l.id);
  const labels = l.kind === "series" ? seriesLibraries : libraries;
  const monitorLabel = monitorsOf(l.list_type, l.kind)?.find(([v]) => v === l.monitor)?.[1] ?? l.monitor;
  const sync = () => m.sync.mutate(l.id, { onSuccess: r => synced(`${r.list.name}: ${r.added} ${r.added === 1 ? "title" : "titles"} added, ${r.skipped} skipped (already in the library or excluded).${r.sync_error ? ` ${r.sync_error}` : ""}`) });
  return <li className="flex min-w-0 flex-wrap items-start gap-3 px-3 py-3" data-testid={`list-${l.id}`}>
    <Switch className="mt-0.5" checked={l.enabled} disabled={busy} aria-label={`Enable ${l.name}`} onCheckedChange={enabled => m.set.mutate({ id: l.id, enabled })} />
    <div className="min-w-0 flex-1 space-y-1.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className="min-w-0 font-semibold text-[var(--ink)] [overflow-wrap:anywhere]">{l.name}</h2>
        {l.list_type === "tmdb" ? <a className="text-xs text-primary hover:underline" href={`https://www.themoviedb.org/list/${l.tmdb_id}`} target="_blank" rel="noreferrer">TMDb list {l.tmdb_id}{l.kind === "series" ? " (series)" : ""}</a>
          : l.list_type === "json" ? <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]">JSON list, {l.kind === "series" ? "series" : "movies"}, at {l.url}</span>
          : l.list_type === "tmdb_discover" && l.rule ? <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid={`list-rule-${l.id}`}>TMDb discover, {l.kind === "series" ? "series" : "movies"}: {ruleWords(l.rule, l.kind)}</span>
          : <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]">{APP[l.list_type]} at {l.url}</span>}
        {l.view ? <Chip tone="ghost">Created through the {l.view} view</Chip> : null}
      </div>
      <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
        <span>Adds to</span>
        {l.versions.map(k => <Chip key={k} tone="ghost">{labels[k] ?? k}</Chip>)}
        <span>· Monitor: {monitorLabel}</span>
        <span>· Search on add: {l.search_on_add ? "on" : "off"}</span>
        <span>· Last sync: {l.last_sync_at ? fmtAgo(l.last_sync_at) : "never"}</span>
      </div>
      {l.last_error ? <p className="text-xs text-[var(--bad-ink)] [overflow-wrap:anywhere]" data-testid={`list-error-${l.id}`}>Last sync failed: {l.last_error}</p> : null}
      {preview ? <div className="space-y-1 text-xs" data-testid={`list-preview-${l.id}`}>
        <p className="text-[var(--ink)]">{previewWords(preview)}</p>
        {adds.length > 0 ? <ul className="list-inside list-disc text-muted-foreground">
          {adds.slice(0, 10).map(t => <li key={`${t.tmdb_id ?? 0}-${t.tvdb_id ?? 0}-${t.name}`}>{titleName(t)}{t.change === "add_library" ? ": to another library" : ""}</li>)}
        </ul> : null}
        {skipped.length > 0 ? <div data-testid={`list-skipped-${l.id}`}>
          <p className="text-[var(--ink)]">Skipped, because they fit none of the list's libraries:</p>
          <ul className="list-inside list-disc text-muted-foreground">
            {skipped.map(t => <li key={`${t.tmdb_id ?? 0}-${t.tvdb_id ?? 0}-${t.name}`}>{titleName(t)}</li>)}
          </ul>
        </div> : null}
        {preview.note && l.list_type !== "sonarr" ? <p className="text-muted-foreground">{preview.note}</p> : null}
      </div> : null}
      {m.preview.error && m.preview.variables === l.id ? <ErrorState error={m.preview.error} /> : null}
    </div>
    {/* On a phone the actions take their own row, so the name and address keep the width. */}
    <div className="flex basis-full flex-wrap items-center gap-2 sm:basis-auto" data-testid={`list-actions-${l.id}`}>
      <Button variant="outline" size="sm" disabled={!canSync || previewing} title={canSync ? undefined : "TMDb is not set up"} onClick={() => m.preview.mutate(l.id)}>{previewing ? <Loader2 className="animate-spin" aria-hidden /> : null}Preview</Button>
      <Button variant="outline" size="sm" disabled={!canSync || busy} title={canSync ? undefined : "TMDb is not set up"} onClick={sync}>{syncing ? <Loader2 className="animate-spin" aria-hidden /> : null}Sync now</Button>
      <Button variant="outline" size="sm" onClick={edit}>Edit</Button>
      <Button variant="outline" size="sm" onClick={remove}>Delete</Button>
    </div>
  </li>;
}

/** List exclusions: the movies and series no list adds (a title removed with "Don't add it again from lists", or
 *  excluded through an arr client), each with Remove. */
function Exclusions() {
  const q = useListExclusions();
  const remove = useRemoveExclusion();
  const list = q.data?.exclusions ?? [];
  return <section className="space-y-2" data-testid="list-exclusions">
    <h2 className="font-heading text-lg font-medium text-[var(--ink)]">List exclusions</h2>
    <p className="text-sm text-muted-foreground">Lists never add these titles. Removing an exclusion lets lists add the title again, but does not bring back a library it was removed from; adding it by hand does.</p>
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : list.length === 0
      ? <p className="text-sm text-muted-foreground">No exclusions. Remove a title with "Don't add it again from lists" to add one.</p>
      : <Card className="p-0"><ul className="divide-y divide-[var(--line)]">{list.map(x => {
        const busy = remove.isPending && remove.variables?.kind === x.kind && remove.variables.id === x.id;
        return <li key={`${x.kind}-${x.id}`} className="flex min-w-0 items-center gap-3 px-3 py-2 text-sm" data-testid={`exclusion-${x.kind}-${x.id}`}>
          <span className="min-w-0 flex-1 text-[var(--ink)] [overflow-wrap:anywhere]">{titleWithYear(x.name, x.year)}</span>
          <Chip tone="ghost">{x.kind === "series" ? "Series" : "Movie"}</Chip>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => remove.mutate(x)}>{busy ? <Loader2 className="animate-spin" aria-hidden /> : null}Remove</Button>
        </li>;
      })}</ul></Card>}
    {remove.error ? <ErrorState error={remove.error} /> : null}
  </section>;
}

export function ListsPage() {
  const me = useAuth();
  const perms = permsOf(me.data?.permissions);
  const q = useLists(perms.manage);
  const m = useListMutations();
  const [dialog, setDialog] = useState<MovieList | "add" | null>(null);
  const [removing, setRemoving] = useState<MovieList | null>(null);
  const [done, setDone] = useState("");
  if (me.isPending) return <Loading />;
  if (!perms.manage) return <Forbidden description="Import lists are for managers and admins." />;
  const d = q.data;
  const open = (v: MovieList | "add") => { m.add.reset(); m.set.reset(); setDialog(v); };
  return <div>
    <PageHeader crumb={perms.admin ? [{ label: "Admin", to: "/admin" }, { label: "Settings", to: "/admin/settings" }] : undefined} title="Import lists" description="Lists add titles by themselves: every title on an enabled list is added to the list's libraries at each sync, or with Sync now. Search on add then searches for them through the download settings, so nothing downloads while downloads are off."
      actions={d ? <Button onClick={() => open("add")}><Plus aria-hidden />Add list</Button> : null} />
    {perms.admin ? <SettingsPicker current="/admin/settings/import-lists" /> : null}
    {dialog && d ? <ListDialog key={dialog === "add" ? "add" : dialog.id} list={dialog === "add" ? undefined : dialog} libraries={d.libraries} seriesLibraries={d.series_libraries ?? {}} canSync={d.can_sync} m={m} onClose={() => setDialog(null)} /> : null}
    {removing ? <ConfirmDialog testid="list-delete" subject={removing.name} busy={m.remove.isPending} error={m.remove.error} onClose={() => setRemoving(null)}
      onConfirm={() => m.remove.mutate(removing.id, { onSuccess: () => setRemoving(null) })}
      scope={{ title: "Delete this list?", does: ["Stop adding titles from this list"], confirm: "Delete list",
        keeps: ["The titles it already added, and their files", ...(APP[removing.list_type] ? [`Everything in the ${APP[removing.list_type]} instance itself`] : []),
          ...(removing.view ? [`Nothing in the ${removing.view} client itself: delete it there too if it should not come back`] : [])] }} /> : null}
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground">A list is a TMDb list, a TMDb discover rule, a JSON list, or the titles of a Radarr or Sonarr instance.{d.can_sync ? "" : " TMDb is not set up, so lists do not sync."}</p>
      {done ? <p role="status" className="text-sm text-[var(--ink)]" data-testid="lists-done">{done}</p> : null}
      {m.sync.error ? <ErrorState error={m.sync.error} /> : null}
      {m.set.error && !dialog ? <ErrorState error={m.set.error} /> : null}
      {d.lists.length === 0 ? <Card><Empty icon={ListVideo}>No lists yet. Add a TMDb list by its id or address, or a Radarr or Sonarr instance by its address and API key.</Empty></Card> :
        <Card className="p-0"><ul className="divide-y divide-[var(--line)]">{d.lists.map(l => <ListRow key={l.id} l={l} libraries={d.libraries} seriesLibraries={d.series_libraries ?? {}} canSync={d.can_sync} m={m}
          edit={() => open(l)} remove={() => { m.remove.reset(); setRemoving(l); }} synced={setDone} />)}</ul></Card>}
      <Exclusions />
    </div> : null}
  </div>;
}
