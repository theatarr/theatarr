// The title page's action bar (docs/design/ui.md, Title page), also the header of the Last search page: Search now
// (every monitored library that can search), Interactive search (a movie, managers, when a library of it is Live; a
// series has it per season and per episode only, as Sonarr does), then on the title page Rename files (admins: a
// preview first), History, Refresh metadata and Remove title; the Last search page has Add to another library
// instead. Below 768 px the title page's other actions sit under More. The notices under the bar: Pause once for the
// page, why a movie has no Interactive search, the Search now outcome or failure with Retry, and why nothing is
// searched. Search now is offered only when it would run: a monitored library in mode active while router.enabled is
// true (the add options say which). Nothing here changes what is searched: the same routes as before.
import { AlertTriangle, History as HistoryIcon, Loader2, MoreHorizontal, PencilLine, Plus, RefreshCw, RotateCcw, Search, Trash2, UserSearch } from "lucide-react";
import { useState } from "react";
import { useAddOptions, useSearchNow, type AddTarget } from "@/api/add";
import { ApiError } from "@/api/client";
import { usePerms, useRefreshTitle, type Version } from "@/api/library";
import { useRename, useRenamePreview } from "@/api/rename";
import { useMinWidth } from "@/components/library/copy-bits";
import { Mono } from "@/components/page";
import { useSearchUI } from "@/components/search/search-context";
import { RemoveDialog } from "@/components/title/copy-actions";
import { InteractiveSearch } from "@/components/title/interactive-search";
import { DownloadsOffNotice } from "@/components/version-status";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Modal } from "@/components/ui/modal";
import { renameSides } from "@/lib/library-rows";
import { renameError } from "@/lib/rename";
import { cn } from "@/lib/utils";
import { globalDownloadsOff, verdict } from "@/lib/version-grab";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** The rename preview: each file's library, old and new name; Rename only on confirm. */
function RenameDialog({ titleId, labels, onClose }: { titleId: number; labels: Record<string, string>; onClose: () => void }) {
  const preview = useRenamePreview(titleId, true);
  const rename = useRename(titleId);
  const error = renameError(preview, rename);
  const p = preview.data;
  const files = p?.files ?? [];
  const done = rename.data;
  return (
    <Modal label="Rename files" onClose={onClose} testid="rename-dialog" className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)]"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] sm:max-w-3xl sm:rounded-[8px]">
      <div className="border-b border-[var(--line)] p-4">
        <h2 className="text-lg font-semibold text-[var(--ink)]">Rename files</h2>
      </div>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4 text-sm">
        {done ? (
          <p role="status" data-testid="rename-done">Renamed {plural(done.files.length, "file")}.</p>
        ) : preview.isPending ? (
          <p className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Checking names…</p>
        ) : p ? (
          <>
            {files.length ? (
              <ul className="divide-y divide-[var(--line)] border-y border-[var(--line)]" data-testid="rename-files">
                {files.map((f) => {
                  const [from, to] = renameSides(f.existing_path, f.new_path);
                  return (
                  <li key={f.link_id} className="py-2" data-testid="rename-file">
                    <div className="text-xs text-[var(--ink-3)]">{labels[f.version] ?? f.version}</div>
                    <div className="flex flex-col gap-x-2 sm:flex-row sm:items-baseline">
                      <Mono className="min-w-0 text-muted-foreground [overflow-wrap:anywhere]"><span title={f.existing_path}>{from}</span></Mono>
                      <span aria-label="becomes" className="text-[var(--ink-3)]">→</span>
                      <Mono className="min-w-0 [overflow-wrap:anywhere]"><span title={f.new_path}>{to}</span></Mono>
                    </div>
                  </li>
                  );
                })}
              </ul>
            ) : null}
            {p.skipped ? <p className="text-[var(--ink-3)]" data-testid="rename-skipped">{plural(p.skipped, "file")} could not be checked.</p> : null}
            {!files.length && !p.skipped ? <p data-testid="rename-none">Every file already has the name the naming settings give it.</p> : null}
          </>
        ) : null}
        {error ? <p role="alert" className="text-[var(--bad-ink)]" data-testid="rename-error">{error}</p> : null}
      </div>
      <div className="flex justify-end gap-2 border-t border-[var(--line)] p-3">
        <Button size="sm" variant="ghost" onClick={onClose}>{done ? "Close" : "Cancel"}</Button>
        {!done && files.length ? (
          <Button size="sm" onClick={() => rename.mutate()} disabled={rename.isPending} data-testid="rename-confirm">
            {rename.isPending ? <Loader2 className="animate-spin" /> : <PencilLine />} Rename {plural(files.length, "file")}
          </Button>
        ) : null}
      </div>
    </Modal>
  );
}

export function TitleToolbar({ titleId, name, year, kind, poster, anime, versions, removed = [], onHistory, page = "title" }: {
  titleId: number;
  name: string;
  year?: number;
  kind: "movie" | "series";
  poster?: string;
  anime?: boolean;
  /** The libraries the title is in now (not removed). */
  versions: Version[];
  /** Libraries it was removed from: their leftover links can still be renamed, under their names. */
  removed?: Version[];
  /** Opens the History tab (the title page). */
  onHistory?: () => void;
  /** "releases": the Last search page's header (Add to another library; no rename, history, refresh or remove). */
  page?: "title" | "releases";
}) {
  const perms = usePerms();
  const target: AddTarget = { kind, name, year, poster, title_id: titleId, anime };
  const opts = useAddOptions(target, Boolean(anime));
  const searchNow = useSearchNow(titleId);
  const refresh = useRefreshTitle(String(titleId));
  const { openAdd } = useSearchUI();
  const wide = useMinWidth(768);
  const [dialog, setDialog] = useState<"interactive" | "rename" | "remove" | null>(null);
  const titlePage = page === "title";
  // Interactive search of the whole movie for managers when a library of it searches live (not Watch only).
  const movie = kind === "movie" && perms.manage;
  const live = movie && versions.some((v) => v.control?.live_releases);
  const watchOnly = movie && !live && versions.some((v) => v.control?.mode === "shadow");
  // The mutation holds the last try: its answer, its error, or nothing while a new one runs (mutate resets both).
  const outcome = searchNow.data ?? null;
  const runSearch = () => searchNow.mutate(undefined);
  const o = opts.data;
  const searchable = perms.manage ? (o?.versions ?? []).filter((v) => v.on && v.searchable).map((v) => v.key) : [];
  // Why nothing is searched, in the shared verdict words: the global state once, else each monitored library's
  // verdict. On the title page each library row states its own, so only "No library is monitored" is said here.
  const on = (o?.versions ?? []).filter((v) => v.on);
  const globalOff = globalDownloadsOff(on.map((v) => v.grab_state));
  const why = !o || !perms.manage || (titlePage && on.length)
    ? ""
    : !on.length
      ? "No library is monitored."
      : globalOff
        ? <DownloadsOffNotice off={globalOff} canManage={perms.admin} />
        : <>Nothing is searched now: {on.map((v) => `${v.label || v.key}: ${v.grab_state ? verdict(v.grab_state).word : "Downloads off"}`).join(" · ")}</>;
  // The title page says Pause once, above its rows (no row repeats it).
  const pageOff = titlePage ? globalDownloadsOff(versions.map((v) => v.control?.grab_state)) : undefined;
  const labels = Object.fromEntries([...versions, ...removed].flatMap((v) => (v.key ? [[v.key, v.label ?? v.key]] : [])));
  const searchTip = kind === "series" ? "Search every monitored episode now, in every Live library (missing episodes and upgrades)" : "Search every monitored Live library now";

  const more: { id: string; label: string; icon: typeof Search; run: () => void; disabled?: boolean; tip?: string }[] = [];
  if (titlePage) {
    if (perms.admin) more.push({ id: "title-rename", label: "Rename files", icon: PencilLine, run: () => setDialog("rename"), tip: "Show what would be renamed" });
    if (perms.seeFiles && onHistory) more.push({ id: "title-history", label: "History", icon: HistoryIcon, run: onHistory });
    if (perms.manage) more.push({ id: "title-refresh", label: refresh.isSuccess ? "Queued" : "Refresh metadata", icon: RefreshCw, run: () => refresh.mutate(), disabled: refresh.isPending, tip: "Fetch the TMDb metadata and art again" });
    if (perms.manage && versions.length > 0) more.push({ id: "title-remove", label: "Remove title", icon: Trash2, run: () => setDialog("remove") });
  }

  return (
    <div className="space-y-2" data-testid="title-toolbar">
      <div className="flex flex-wrap items-center gap-2">
        {searchable.length > 0 ? (
          <Button size="sm" variant="outline" className="max-md:h-9" onClick={runSearch} disabled={searchNow.isPending} title={searchTip} data-testid="title-search-now">
            {searchNow.isPending ? <Loader2 className="animate-spin" /> : <Search />} Search now
          </Button>
        ) : null}
        {live ? (
          <Button size="sm" variant="outline" className="max-md:h-9" onClick={() => setDialog("interactive")} title="Search now and choose the release yourself" data-testid="interactive-search-open">
            <UserSearch /> Interactive search
          </Button>
        ) : null}
        {!titlePage && perms.add ? (
          <Button size="sm" variant="outline" onClick={() => openAdd(target)} data-testid="title-add-versions">
            <Plus /> Add to another library
          </Button>
        ) : null}
        {more.length && wide
          ? more.map((m) => (
            <Button key={m.id} size="sm" variant="outline" onClick={m.run} disabled={m.disabled} title={m.tip} data-testid={m.id}>
              <m.icon className={cn(m.id === "title-refresh" && refresh.isPending && "animate-spin")} /> {m.label}
            </Button>
          ))
          : null}
        {more.length && !wide ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="sm" variant="outline" className="h-9" data-testid="title-more">
                <MoreHorizontal /> More
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {more.map((m) => (
                <DropdownMenuItem key={m.id} onSelect={m.run} disabled={m.disabled} data-testid={m.id}>
                  <m.icon /> {m.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}
      </div>
      {pageOff ? <DownloadsOffNotice off={pageOff} canManage={perms.admin} /> : null}
      {watchOnly ? (
        <p className="text-xs text-[var(--ink-3)]" data-testid="title-no-interactive">Interactive search needs a Live library. This film's libraries are Watch only.</p>
      ) : null}
      {refresh.error ? (
        <p role="alert" className="rounded-[5px] border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-3 py-2 text-xs" data-testid="title-refresh-error">
          Refresh failed: {refresh.error.message}
        </p>
      ) : null}
      {searchNow.error ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 rounded-[5px] border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-3 py-2 text-xs" data-testid="title-search-error">
          <AlertTriangle className="size-3.5 shrink-0 text-[var(--bad-ink)]" />
          <span className="min-w-0 flex-1 text-[var(--bad-ink)] [overflow-wrap:anywhere]">
            Search now failed: {searchNow.error instanceof ApiError ? searchNow.error.message : "could not reach the server"}
          </span>
          <Button size="xs" variant="outline" onClick={runSearch} disabled={searchNow.isPending} data-testid="title-search-retry">
            {searchNow.isPending ? <Loader2 className="animate-spin" /> : <RotateCcw />} Retry
          </Button>
        </div>
      ) : outcome ? (
        <p className={cn("rounded-[5px] border px-3 py-2 text-xs", outcome.started ? "border-[var(--th-info-border)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line)] text-[var(--ink-3)]")} data-testid="title-search-outcome">
          {outcome.message}
        </p>
      ) : searchable.length === 0 && why && !opts.isPending ? (
        <div className="text-xs text-[var(--ink-3)]" data-testid="title-no-search">{why}</div>
      ) : null}
      {dialog === "interactive" ? <InteractiveSearch titleId={titleId} onClose={() => setDialog(null)} /> : null}
      {dialog === "rename" ? <RenameDialog titleId={titleId} labels={labels} onClose={() => setDialog(null)} /> : null}
      {dialog === "remove" ? <RemoveDialog titleId={titleId} onClose={() => setDialog(null)} /> : null}
    </div>
  );
}
