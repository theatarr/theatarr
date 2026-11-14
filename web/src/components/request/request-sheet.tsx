// The request sheet: what an account without the add permission gets in place of the add sheet (docs/design/users.md).
// It shows the versions the request will ask for (the account's request set, kept to the versions that fit the title;
// by default every one that fits), the seasons of a series, and a note, and posts to
// POST /requests. Users do not pick versions; an admin sets them per account. The layout is the add sheet's (Scryer's
// add-to-catalog dialog, GPL-3.0) with Seerr's RequestModal (MIT, github.com/seerr-team/seerr src/components/
// RequestModal @ c167958) for the wording and the season list. When the account already has an open request for the
// title, or everything it could ask for is in the library or monitored, the sheet says so with a direct link (View
// request, View availability) in place of a disabled Request button (request-state.ts). For a series the library has,
// the seasons already monitored in every wanted version show as such and only the others can be picked ("Request more
// seasons"; approval monitors just those).
import { AlertTriangle, ArrowUpRight, CheckCircle2, Clock, Inbox, Loader2, Send, X } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/api/auth";
import { ApiError } from "@/api/client";
import { useAddOptions, type AddTarget } from "@/api/add";
import { REQUEST_STATUS, useCreateRequest, useMyOpenRequests, type CreateResult } from "@/api/requests";
import { FacetPill, facetOf, Poster } from "@/components/search/search-results";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import {
  applicableRequestVersionKeys,
  approvalNotice,
  versionFetching,
  findRequest,
  moreSeasonsOnly,
  requestableSeasons,
  requestAction,
  requestDoneWords,
  requestLink,
  requestStateWords,
  requestVersions,
  seasonsToSend,
} from "@/lib/request-state";
import { cn } from "@/lib/utils";
import { nameWithoutYear } from "@/lib/format";

function seasonLabel(n: number) {
  return n === 0 ? "Specials" : `Season ${n}`;
}

export function RequestSheet({ target, onClose }: { target: AddTarget; onClose: () => void }) {
  const me = useAuth();
  const inLibrary = target.title_id !== undefined;
  const opts = useAddOptions(target, Boolean(target.anime));
  const create = useCreateRequest();
  const mine = findRequest(useMyOpenRequests(), target);
  const [seasons, setSeasons] = useState<Record<number, boolean>>({});
  const [note, setNote] = useState("");
  const [done, setDone] = useState<CreateResult | null>(null);

  const o = opts.data;
  const wanted = requestVersions(o?.versions ?? [], me.data?.request_versions);
  const applicable = new Set(mine?.versions?.map((v) => v.key) ?? applicableRequestVersionKeys(o?.versions ?? [], me.data?.request_versions));
  const fetching = versionFetching((o?.versions ?? []).filter((v) => applicable.has(v.key)));
  const allSeasons = (o?.seasons ?? []).filter((s) => s.season > 0);
  const allNums = allSeasons.map((s) => s.season);
  // A series the library has: only the seasons some wanted version does not monitor yet can be asked for.
  const open = requestableSeasons(wanted, allNums);
  const more = moreSeasonsOnly(wanted);
  const seasonOn = (n: number) => open.includes(n) && (seasons[n] ?? true);
  const picked = allNums.filter(seasonOn);
  const sendSeasons = seasonsToSend(picked, open, allNums);
  // Request, View request or Available (nothing left to ask for); unknown until the options load.
  const action = requestAction({ mine, requestable: o ? wanted.length : undefined, titleId: target.title_id });

  const submit = () => {
    if (!o || !me.data) return;
    create.mutate(
      {
        kind: o.kind,
        tmdb_id: target.tmdb_id || undefined,
        title_id: target.title_id,
        name: inLibrary ? undefined : target.name,
        year: inLibrary ? undefined : target.year,
        anime: inLibrary ? undefined : Boolean(target.anime),
        poster_path: target.poster_path,
        seasons: o.kind === "series" ? sendSeasons : undefined,
        note: note.trim() || undefined,
      },
      { onSuccess: setDone },
    );
  };

  return (
    <Modal
      label={more ? `Request more seasons of ${target.name}` : `Request ${target.name}`}
      onClose={onClose}
      testid="request-sheet"
      className="z-[70]"
      backdropClassName="bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelSlot="sheet-content"
      panelClassName="absolute inset-x-0 bottom-0 flex max-h-[92dvh] flex-col rounded-t-[8px] border-t border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-8 sm:inset-y-0 sm:left-auto sm:right-0 sm:max-h-none sm:w-[min(460px,100vw)] sm:rounded-none sm:border-l sm:border-t-0 sm:slide-in-from-bottom-0 sm:slide-in-from-right-8"
    >
        <div className="relative flex gap-4 border-b border-[var(--line)] p-4 pr-12">
          <Poster src={target.poster} title={target.name} className="relative aspect-[2/3] w-[72px] shrink-0 rounded-lg ring-1 ring-[var(--line)]" />
          <div className="min-w-0">
            <div className="mb-1.5 flex flex-wrap items-center gap-1.5">
              <FacetPill facet={facetOf(target.kind, target.anime)} />
              {inLibrary ? <Badge tone="positive">In library</Badge> : null}
            </div>
            <h2 className="text-lg font-semibold leading-tight text-[var(--ink)] [overflow-wrap:anywhere]">
              {nameWithoutYear(target.name, target.year)}
              {target.year ? <span className="font-normal text-[var(--ink-3)]"> ({target.year})</span> : null}
            </h2>
            {target.overview ? <p className="mt-1 line-clamp-3 text-xs text-[var(--ink-3)]">{target.overview}</p> : null}
          </div>
          <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
            <X className="size-4" />
          </button>
        </div>

        {done ? (
          <RequestDone result={done} manages={Boolean(me.data?.permissions?.includes("manage_requests"))} onClose={onClose} />
        ) : action.kind !== "request" ? (
          <RequestSettled action={action} fetching={fetching} onClose={onClose} />
        ) : (
          <>
            <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-4">
              {opts.isPending ? (
                <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
                  <Loader2 className="size-4 animate-spin" /> Loading…
                </div>
              ) : opts.error ? (
                <p className="text-sm text-[var(--bad-ink)]">{String((opts.error as Error).message)}</p>
              ) : o ? (
                <>
                  <section>
                    <h3 className="mb-2 text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">You are asking for</h3>
                    {more ? (
                      <p className="mb-2 text-sm" data-testid="request-more-seasons">This series is in the library. Pick the seasons to add; the ones already there stay as they are.</p>
                    ) : null}
                    {wanted.length > 0 ? (
                      <div className="flex flex-wrap gap-2" data-testid="request-versions">
                        {wanted.map((v) => (
                          <span key={v.key} data-vkey={v.key} className="rounded-lg border border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_10%,transparent)] px-2.5 py-1 text-sm font-medium text-[var(--accent-ink)]">
                            {v.label}
                          </span>
                        ))}
                      </div>
                    ) : null}
                    <p className="mt-2 text-xs text-[var(--ink-3)]">Your account’s library defaults are applied automatically.</p>
                    <p className="mt-2 text-sm" data-testid="request-approval-notice">
                      {approvalNotice(Boolean(me.data?.permissions?.includes("auto_approve")), Boolean(me.data?.permissions?.includes("manage_requests")))}
                    </p>
                  </section>
                  {o.kind === "series" && allSeasons.length > 0 ? (
                    <section>
                      <h3 className="mb-2 text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Seasons</h3>
                      <div className="grid grid-cols-2 gap-2" data-testid="request-seasons">
                        {allSeasons.map((s) => {
                          const here = !open.includes(s.season);
                          return (
                            <label
                              key={s.season}
                              data-season={s.season}
                              data-here={here || undefined}
                              className={cn(
                                "flex items-center gap-2.5 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2 text-sm transition",
                                here ? "cursor-default opacity-60" : "cursor-pointer hover:border-[var(--line-2)]",
                                seasonOn(s.season) && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]",
                              )}
                            >
                              <input type="checkbox" className="size-4 accent-[var(--accent)]" disabled={here} checked={seasonOn(s.season)} onChange={(e) => setSeasons((p) => ({ ...p, [s.season]: e.target.checked }))} />
                              <span className="font-medium">{seasonLabel(s.season)}</span>
                              <span className="text-xs text-[var(--ink-3)]">{here ? "Already monitored" : `${s.episodes} ep.`}</span>
                            </label>
                          );
                        })}
                      </div>
                    </section>
                  ) : null}
                  <section>
                    <label htmlFor="request-note" className="mb-2 block text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Note (optional)</label>
                    <textarea
                      id="request-note"
                      value={note}
                      maxLength={500}
                      onChange={(e) => setNote(e.target.value)}
                      rows={2}
                      placeholder="Anything the admins should know"
                      className="w-full resize-none rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2 text-sm outline-none placeholder:text-[var(--ink-3)] focus:border-[var(--th-baccent)]"
                    />
                  </section>
                </>
              ) : null}
            </div>
            <div className="flex items-center justify-between gap-3 border-t border-[var(--line)] p-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
              {create.error ? (
                <span className="flex min-w-0 items-center gap-1.5 text-xs text-[var(--bad-ink)]">
                  <AlertTriangle className="size-3.5 shrink-0" />
                  <span className="truncate">{create.error instanceof ApiError ? create.error.message : "Could not request"}</span>
                </span>
              ) : (
                <span className="text-xs text-[var(--ink-3)]">{o?.kind === "series" && sendSeasons ? `${picked.length} of ${open.length} seasons` : ""}</span>
              )}
              <div className="flex shrink-0 gap-2">
                <Button variant="ghost" onClick={onClose}>Cancel</Button>
                <Button onClick={submit} disabled={!o || !me.data || wanted.length === 0 || (o.kind === "series" && open.length > 0 && picked.length === 0) || create.isPending} data-testid="request-submit">
                  {create.isPending ? <Loader2 className="animate-spin" /> : <Send />}
                  Request
                </Button>
              </div>
            </div>
          </>
        )}
    </Modal>
  );
}

/** The sheet when there is nothing new to ask: the caller's open request, or everything already here or monitored. */
function RequestSettled({ action, fetching, onClose }: { fetching: boolean; action: Exclude<ReturnType<typeof requestAction>, { kind: "request" }>; onClose: () => void }) {
  const mine = action.kind === "view-request";
  const st = mine ? (REQUEST_STATUS[action.status] ?? { label: action.status, tone: "neutral" as const }) : null;
  return (
    <div className="flex-1 space-y-4 overflow-y-auto p-4" data-testid={mine ? "request-existing" : "request-nothing"}>
      <div className="flex items-start gap-3 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] p-3 text-sm">
        {mine ? <Clock className="mt-0.5 size-4 shrink-0 text-[var(--accent-ink)]" /> : <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-[var(--ok-ink)]" />}
        <div className="min-w-0">
          {st ? <Badge tone={st.tone} className="mb-1">{st.label}</Badge> : null}
          <div className="font-medium text-[var(--ink)]">
            {mine ? requestStateWords(action.status, fetching) : "Everything you can ask for is already in the library or monitored."}
          </div>
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        {action.kind === "view-request" ? (
          <Button asChild data-testid="request-view">
            <Link to={action.to} onClick={onClose}>
              <Inbox /> View request
            </Link>
          </Button>
        ) : action.to ? (
          <Button asChild data-testid="request-view-title">
            <Link to={action.to} onClick={onClose}>
              <ArrowUpRight /> {action.label}
            </Link>
          </Button>
        ) : null}
        <Button variant="outline" onClick={onClose}>Close</Button>
      </div>
    </div>
  );
}

function RequestDone({ result, manages, onClose }: { result: CreateResult; manages: boolean; onClose: () => void }) {
  const r = result.request;
  const text = requestDoneWords(result, manages);
  return (
    <div className="flex-1 space-y-4 overflow-y-auto p-4" data-testid="request-done">
      <div className="flex items-start gap-3 rounded-[5px] border border-[var(--th-success-border)] bg-[var(--ok-soft)] p-3 text-sm text-[var(--ok-ink)]">
        {result.approved ? <CheckCircle2 className="mt-0.5 size-4 shrink-0" /> : <Clock className="mt-0.5 size-4 shrink-0" />}
        <div>
          <div className="font-semibold">{r.name} requested</div>
          <div className="mt-0.5 opacity-90">{text}</div>
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        {r.versions.map((v) => (
          <span key={v.key} className="rounded-lg border border-[var(--line)] px-2.5 py-1 text-sm">{v.label}</span>
        ))}
      </div>
      <div className="flex gap-2">
        <Button asChild>
          <Link to={requestLink(r.id)} onClick={onClose}>
            <Inbox /> View request
          </Link>
        </Button>
        <Button variant="outline" onClick={onClose}>Close</Button>
      </div>
    </div>
  );
}
