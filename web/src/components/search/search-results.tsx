// Search result cards and groups, shared by the overlay and the /search page.
// Card layouts adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/root/
// global-search-parts.tsx @ 2ef7757c, GPL-3.0 (the "In library" row card: small poster, name, facet chip, actions on
// the right; the section loading and empty states) and from Seerr (github.com/seerr-team/seerr) src/components/
// TitleCard/index.tsx @ c167958a, MIT (the TMDb poster card: type pill, status badge, hover overlay with the add
// button).
import { AlertTriangle, ArrowUpRight, Check, Clock, Inbox, Loader2, Plus, RotateCcw, Search, SearchX } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import type { AddTarget, LibraryHit, SearchAnswer, SearchHit } from "@/api/add";
import { ApiError } from "@/api/client";
import { usePerms } from "@/api/library";
import { useMyOpenRequests } from "@/api/requests";
import { VersionTag } from "@/components/page";
import { useSearchUI } from "./search-context";
import { titleWatchWords } from "@/lib/library";
import { findRequest, requestLink } from "@/lib/request-state";
import { settingHref } from "@/lib/settings-map";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { nameWithoutYear, titleWithYear } from "@/lib/format";

function artIndex(s: string): number {
  let h = 2166136261;
  for (const c of s) {
    h ^= c.charCodeAt(0);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0) % 12;
}

/** A poster on this origin, or generated art with the title when there is none. Position it with className. */
export function Poster({ src, title, className }: { src?: string; title: string; className?: string }) {
  return (
    <div className={cn("overflow-hidden bg-[var(--bg)]", className)}>
      {src ? (
        <img src={src} alt="" loading="lazy" decoding="async" className="absolute inset-0 size-full object-cover" />
      ) : (
        <div data-art={artIndex(title)} className="absolute inset-0">
          <div className="absolute inset-x-2 bottom-2 line-clamp-4 text-[11px] font-extrabold leading-tight text-white/95 [overflow-wrap:anywhere]">
            {title}
          </div>
        </div>
      )}
    </div>
  );
}

export type Facet = "movie" | "series" | "anime";

export function facetOf(kind?: string, anime?: boolean): Facet {
  return anime ? "anime" : kind === "series" ? "series" : "movie";
}

const FACET_LABEL: Record<Facet, string> = { movie: "Movie", series: "Series", anime: "Anime" };

const FACET_MARK: Record<Facet, string> = { movie: "bg-[var(--th-facet-movie)]", series: "bg-[var(--accent)]", anime: "bg-[var(--th-facet-anime)]" };

/** The kind of a result as a square chip with its facet mark. onArt: over a poster, on the art scrim (dark marks). */
export function FacetPill({ facet, onArt, className }: { facet: Facet; onArt?: boolean; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-[3px] px-1.5 text-[10px] font-semibold uppercase tracking-wider",
        onArt ? "dark bg-[var(--art-scrim)] text-white" : "bg-[var(--chip)] text-[var(--ink-2)]",
        className,
      )}
    >
      <span aria-hidden className={cn("size-1.5 shrink-0 rounded-[1px]", FACET_MARK[facet])} />
      {FACET_LABEL[facet]}
    </span>
  );
}

/** A library title as a row: poster, name, versions; opens the title, or adds versions to it. */
export function LibraryResultCard({ t, onOpen, onAdd }: { t: LibraryHit; onOpen?: () => void; onAdd: (t: AddTarget) => void }) {
  const { canAdd, canRequest } = useSearchUI();
  const requests = useMyOpenRequests(!canAdd && canRequest);
  const mine = canAdd ? undefined : findRequest(requests, { kind: t.kind, title_id: t.id, tmdb_id: t.tmdb_id });
  const facet = facetOf(t.kind, t.anime);
  const name = t.name ?? "?";
  // Without "see files" a title reads as watchability, not library keys (docs/design/users.md).
  const seeFiles = usePerms().seeFiles;
  const watch = seeFiles ? null : titleWatchWords(t);
  return (
    <div
      data-search-result="library"
      className="group flex w-full items-center gap-2 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] p-2 transition-colors hover:bg-[var(--hover)]"
    >
      <Link
        to={`/title/${t.id}`}
        onClick={onOpen}
        className="flex min-w-0 flex-1 items-center gap-3 rounded-[3px] outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]"
        aria-label={`Open ${name}`}
      >
        <Poster src={t.poster} title={name} className="relative h-16 w-11 flex-none rounded-[3px] border border-[var(--line)]" />
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold text-[var(--ink)]">
            {nameWithoutYear(name, t.year)}
            {t.year ? <span className="font-normal text-[var(--ink-3)]"> ({t.year})</span> : null}
          </p>
          <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-1.5">
            <FacetPill facet={facet} />
            {seeFiles ? (t.versions ?? []).map((v) => (
              <VersionTag key={v.key} k={v.key ?? ""} className={cn("px-1.5 py-0 text-[11px]", !v.has_file && "opacity-60")} />
            )) : watch ? <Badge tone={watch.tone === "partial" ? "warning" : watch.tone} data-testid="search-watch">{watch.label}</Badge> : null}
          </div>
        </div>
      </Link>
      <div className="flex shrink-0 items-center gap-1.5">
        {!canAdd && mine ? (
          <Link
            to={requestLink(mine.id)}
            onClick={onOpen}
            aria-label={`View your request for ${name}`}
            title="View request"
            data-testid="view-request"
            className={cn(buttonVariants({ variant: "warning", size: "icon" }))}
          >
            <Inbox className="size-4" />
          </Link>
        ) : canAdd || canRequest ? (
        <Button
          variant="outline"
          size="icon"
          onClick={() => onAdd({ kind: t.kind === "series" ? "series" : "movie", name, year: t.year, poster: t.poster, title_id: t.id, tmdb_id: t.tmdb_id, anime: t.anime })}
          aria-label={canAdd ? `Libraries of ${name}` : `Request ${name}`}
          title={canAdd ? "Add libraries" : "Request"}
          className="text-[var(--accent-ink)]"
        >
          <Plus className="size-4" />
        </Button>
        ) : null}
        <Link
          to={`/title/${t.id}`}
          onClick={onOpen}
          aria-label={`View ${name}`}
          title="View"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }))}
        >
          <ArrowUpRight className="size-4" />
        </Link>
      </div>
    </div>
  );
}

export function hitTarget(h: SearchHit): AddTarget {
  return {
    kind: h.kind === "series" ? "series" : "movie",
    name: h.name,
    year: h.year,
    poster: h.poster,
    poster_path: h.poster_path,
    overview: h.overview,
    tmdb_id: h.tmdb_id,
    title_id: h.title_id,
    anime: h.anime,
  };
}

/** A TMDb result as a poster card. Not in the library: the card opens the add sheet. In it: a badge, and the card
 * opens the title (with a + for more versions). */
export function TmdbResultCard({ h, onAdd, onOpen, className }: { h: SearchHit; onAdd: (t: AddTarget) => void; onOpen?: () => void; className?: string }) {
  const { canAdd, canRequest } = useSearchUI();
  // An account that requests: its own open request for this title turns Request into View request.
  const requests = useMyOpenRequests(!canAdd && canRequest);
  const mine = canAdd ? undefined : findRequest(requests, { kind: h.kind, title_id: h.title_id, tmdb_id: h.tmdb_id });
  const label = titleWithYear(h.name, h.year);
  const verb = canAdd ? "Add" : "Request";
  // An account that may neither add nor request only sees the result (and opens it when it is in the library).
  const act = canAdd || canRequest;
  const body = (
    <>
      <div className="relative overflow-hidden rounded-[5px] pb-[150%] ring-1 ring-[var(--line)] transition duration-300 group-hover:ring-[var(--line-2)] group-focus-visible:ring-2 group-focus-visible:ring-[var(--line-focus)]">
        <Poster src={h.poster} title={h.name} className="absolute inset-0" />
        {/* Only the kind sits on the art: a status badge beside it does not fit a phone-width poster. */}
        <div className="absolute left-0 top-0 p-1.5" data-testid="poster-badges">
          <FacetPill facet={facetOf(h.kind, h.anime)} onArt />
        </div>
        <div className="absolute inset-0 flex flex-col justify-end bg-[linear-gradient(180deg,var(--art-scrim)_0%,var(--art-scrim-strong)_100%)] p-2 text-white opacity-0 transition-opacity duration-200 group-hover:opacity-100 group-focus-visible:opacity-100">
          {h.overview ? <p className="mb-2 line-clamp-5 text-[11px] leading-snug text-white/85">{h.overview}</p> : null}
          {h.in_library || mine || act ? (
            <span className="flex h-7 items-center justify-center gap-1 rounded-[5px] bg-[var(--accent)] text-xs font-semibold text-[var(--on-accent)]">
              {h.in_library ? <ArrowUpRight className="size-3.5" /> : mine ? <Inbox className="size-3.5" /> : <Plus className="size-3.5" />}
              {h.in_library ? "View" : mine ? "View request" : verb}
            </span>
          ) : null}
        </div>
      </div>
      <div className="mt-1.5 px-0.5">
        <div className="line-clamp-2 text-[13px] font-semibold leading-snug text-[var(--ink)] [overflow-wrap:anywhere]" title={h.name}>{h.name}</div>
        <div className="truncate text-[11.5px] text-[var(--ink-3)]">
          {h.year ?? "—"}
          {h.rating ? ` · ★ ${h.rating.toFixed(1)}` : ""}
        </div>
        {h.in_library ? (
          <div className="mt-0.5 flex items-center gap-1 text-[11.5px] font-medium text-[var(--ok-ink)]" data-testid="status-badge">
            <Check className="size-3 shrink-0" aria-hidden />
            In library
          </div>
        ) : h.requested || mine ? (
          <div className="mt-0.5 flex items-center gap-1 text-[11.5px] font-medium text-[var(--warn-ink)]" data-testid="requested-badge">
            <Clock className="size-3 shrink-0" aria-hidden />
            Requested
          </div>
        ) : null}
      </div>
    </>
  );
  const cls = cn("group block w-full text-left outline-none", className);
  return (
    <div className="relative" data-search-result="tmdb" data-in-library={h.in_library ? "1" : "0"}>
      {h.in_library && h.title_id ? (
        <Link to={`/title/${h.title_id}`} onClick={onOpen} className={cls} aria-label={`Open ${label}`}>
          {body}
        </Link>
      ) : mine ? (
        <Link to={requestLink(mine.id)} onClick={onOpen} className={cls} aria-label={`View your request for ${label}`} data-testid="view-request">
          {body}
        </Link>
      ) : act ? (
        <button type="button" onClick={() => onAdd(hitTarget(h))} className={cls} aria-label={`${verb} ${label}`}>
          {body}
        </button>
      ) : (
        <div className={cls}>
          {body}
        </div>
      )}
      {h.in_library && mine ? (
        <Link
          to={requestLink(mine.id)}
          onClick={onOpen}
          aria-label={`View your request for ${label}`}
          title="View request"
          data-testid="view-request"
          className="absolute right-1.5 top-8 grid size-7 place-items-center rounded-[5px] bg-[var(--art-scrim)] text-white outline-none transition-colors hover:bg-[var(--accent)] hover:text-[var(--on-accent)] focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]"
        >
          <Inbox className="size-3.5" />
        </Link>
      ) : h.in_library && act ? (
        <button
          type="button"
          onClick={() => onAdd(hitTarget(h))}
          aria-label={canAdd ? `Libraries of ${label}` : `Request ${label}`}
          title={canAdd ? "Add libraries" : "Request"}
          className="absolute right-1.5 top-8 grid size-7 place-items-center rounded-[5px] bg-[var(--art-scrim)] text-white outline-none transition-colors hover:bg-[var(--accent)] hover:text-[var(--on-accent)] focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]"
        >
          <Plus className="size-3.5" />
        </button>
      ) : null}
    </div>
  );
}

export function SectionLoading({ label }: { label: string }) {
  return (
    <div className="flex min-h-20 items-center justify-center gap-3 rounded-[5px] border border-dashed border-[var(--line-2)] px-4 py-3 text-sm text-[var(--ink-3)]">
      <Loader2 className="size-4 animate-spin text-[var(--line-focus)]" />
      <span className="font-medium">{label}</span>
    </div>
  );
}

export function SectionNote({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-[5px] border border-dashed border-[var(--line-2)] px-4 py-5 text-sm text-[var(--ink-3)]">{children}</p>
  );
}

export function SearchEmpty({ title, description, miss }: { title: string; description: string; miss?: boolean }) {
  const Icon = miss ? SearchX : Search;
  return (
    <div className="flex flex-col items-center justify-center py-12 text-center">
      <div className="mb-3 grid size-10 place-items-center rounded-full bg-[var(--sunken)] text-[var(--ink-3)]">
        <Icon className="size-5" />
      </div>
      <p className="max-w-full break-all text-[15px] font-semibold text-[var(--ink)]">{title}</p>
      <p className="mt-1 max-w-sm text-sm leading-6 text-[var(--ink-3)]">{description}</p>
    </div>
  );
}

/** The search screens' words for what the account can do with a result: add, request, or only look. */
export function searchWords(canAdd: boolean, canRequest: boolean) {
  if (canAdd) {
    return {
      page: "Your library and TMDb. Add a movie or series, or more libraries of a title you have.",
      tmdb: "Add from TMDb",
      empty: "Type a title. Library matches show at once; TMDb results follow, with an Add button for anything not in the library.",
    };
  }
  if (canRequest) {
    return {
      page: "Your library and TMDb. Request a movie or series, or more of a title you have.",
      tmdb: "Request from TMDb",
      empty: "Type a title. Library matches show at once; TMDb results follow, with a Request button for anything not in the library.",
    };
  }
  return {
    page: "Your library and TMDb.",
    tmdb: "On TMDb",
    empty: "Type a title. Library matches show at once; TMDb results follow.",
  };
}

export function GroupHeader({ title, note, count }: { title: string; note: string; count?: number }) {
  return (
    <div className="mb-3 flex items-baseline justify-between gap-3">
      <div className="flex min-w-0 items-baseline gap-2">
        <h3 className="text-[15px] font-semibold text-[var(--ink)]">{title}</h3>
        <span className="truncate text-xs text-[var(--ink-3)]">{note}</span>
      </div>
      {count !== undefined ? <span className="shrink-0 text-xs font-medium tabular-nums text-[var(--ink-3)]">{count === 1 ? "1 result" : `${count} results`}</span> : null}
    </div>
  );
}

/** Why a TMDb part has no results that is not an error: off (no key) or too short. Errors are searchProblem's. */
export function tmdbNote(state?: string): string | null {
  switch (state) {
    case "off":
      return "TMDb search is off: no TMDb key on this server.";
    case "short":
      return "Type at least two characters to search TMDb.";
  }
  return null;
}

/** The TMDb note in the TMDb group. With no key, admins get a link to where it is set (Settings › Metadata & artwork, the
 * metadata services panel, metadata.sources.tmdb); everyone else is told who can add one. onOpen runs when the link is
 * followed (the search overlay closes, so Settings is not left behind the modal). */
export function TmdbNote({ state, admin, onOpen }: { state?: string; admin: boolean; onOpen?: () => void }) {
  const note = tmdbNote(state);
  if (!note) return null;
  return (
    <SectionNote>
      {note}
      {state === "off" ? (
        admin ? (
          <>
            {" "}
            <Link to={settingHref("metadata", "sources.tmdb") ?? "/admin/settings/metadata"} onClick={onOpen} className="font-medium text-[var(--accent-ink)] underline-offset-2 hover:underline" data-testid="tmdb-key-link">
              Set the TMDb key
            </Link>
          </>
        ) : (
          " An administrator can add one."
        )
      ) : null}
    </SectionNote>
  );
}

export type SearchProblem = { kind: "transport" | "api" | "tmdb"; title: string; detail: string };

/** A failed search part, told apart from an empty one: the request never got an answer (transport), the server
 * refused or failed it (api), or the server answered but TMDb did not (tmdb, from tmdb_state). Null when it worked. */
export function searchProblem(error: unknown, data?: Pick<SearchAnswer, "tmdb_state" | "tmdb_error">, part?: "tmdb"): SearchProblem | null {
  if (error instanceof ApiError) {
    return { kind: "api", title: "The search failed", detail: `The server answered ${error.status}: ${error.message}` };
  }
  if (error) {
    return { kind: "transport", title: "Could not reach the server", detail: error instanceof Error && error.message ? error.message : "The request did not get an answer." };
  }
  if (part === "tmdb" && data?.tmdb_state === "error") {
    return { kind: "tmdb", title: "TMDb did not answer", detail: data.tmdb_error || "Try again in a moment." };
  }
  return null;
}

/** admin and onOpen: a TMDb failure tells an admin where the key and address are set (onOpen closes the overlay). */
export function SectionError({ problem, onRetry, retrying, testid, admin, onOpen }: { problem: SearchProblem; onRetry: () => void; retrying?: boolean; testid?: string; admin?: boolean; onOpen?: () => void }) {
  return (
    <div
      role="alert"
      data-testid={testid}
      data-problem={problem.kind}
      className="flex flex-wrap items-center gap-3 rounded-[5px] border border-l-[3px] border-[var(--line)] border-l-[var(--bad)] bg-[var(--surface)] px-4 py-3 text-sm"
    >
      <AlertTriangle className="size-4 shrink-0 text-[var(--bad-ink)]" />
      <div className="min-w-0 flex-1">
        <div className="font-semibold text-[var(--bad-ink)]">{problem.title}</div>
        <div className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]">{problem.detail}</div>
        {problem.kind === "tmdb" ? (
          <div className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]" data-testid="tmdb-next">
            If it keeps failing, the TMDb key may be wrong or Theatarr cannot reach TMDb (network, DNS).{" "}
            {admin ? (
              <Link to={settingHref("metadata", "sources.tmdb") ?? "/admin/settings/metadata"} onClick={onOpen} className="font-medium text-[var(--accent-ink)] underline-offset-2 hover:underline">
                Check the TMDb key and address
              </Link>
            ) : "An administrator can check the key."}
          </div>
        ) : null}
      </div>
      <Button variant="outline" size="sm" onClick={onRetry} disabled={retrying}>
        {retrying ? <Loader2 className="animate-spin" /> : <RotateCcw />}
        Retry
      </Button>
    </div>
  );
}
