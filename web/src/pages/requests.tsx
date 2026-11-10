// /requests (docs/design/ui.md, docs/design/users.md): a user's own requests by status; for managers and admins also the
// queue to decide (approve, approve with changed versions or seasons, decline with a reason) and every request.
// Request rows follow Seerr's RequestCard and RequestList (MIT, github.com/seerr-team/seerr src/components/
// RequestList @ c167958): poster, title, status badge, requested by and when, the decision; ours adds a chip per
// version that turns green when that version is in the library. /requests/<id> opens its timeline; old
// ?request=<id> links redirect there. The server answers only a request the caller may see.
import { Check, CheckCircle2, Inbox, Loader2, Pencil, Send, X } from "lucide-react";
import { useState } from "react";
import { Link, Navigate, useParams, useSearchParams } from "react-router";
import { useAddOptions } from "@/api/add";
import { ApiError } from "@/api/client";
import { usePerms } from "@/api/library";
import {
  REQUEST_STATUS,
  useApproveRequest,
  useCancelRequest,
  useDeclineRequest,
  useRequest,
  useRequests,
  type Request,
  type RequestDetail,
} from "@/api/requests";
import { useUpgradeHolds } from "@/api/upgrades";
import { Empty, ErrorState, Loading, PageHeader, RouteTabs } from "@/components/page";
import { facetOf, Poster } from "@/components/search/search-results";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { versionFetching } from "@/lib/request-state";
import { UpgradesWaiting } from "@/components/upgrades/upgrades-waiting";
import { Badge, Tag } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { fmtAgo, fmtTime } from "@/lib/format";
import { approvalWords, requestTimeline } from "@/lib/request-timeline";
import { cn } from "@/lib/utils";

const FILTERS = [
  { id: "open", label: "Open" },
  { id: "pending", label: "Pending" },
  { id: "available", label: "Available" },
  { id: "declined,canceled,failed", label: "Closed" },
  { id: "all", label: "All" },
];

function seasonsText(r: Request): string {
  if (r.kind !== "series") return "";
  if (!r.seasons || r.seasons.length === 0) return "Every season";
  return r.seasons.length === 1 ? `Season ${r.seasons[0]}` : `Seasons ${r.seasons.join(", ")}`;
}

function who(r: Request): string {
  const n = r.user?.display_name || r.user?.username || "Someone";
  const others = r.others ?? 0;
  return others > 0 ? `${n} and ${others} other${others === 1 ? "" : "s"}` : n;
}

export function RequestsPage() {
  const { tab } = useParams();
  const [params] = useSearchParams();
  const legacyId = Number(params.get("request"));
  if (Number.isSafeInteger(legacyId) && legacyId > 0) return <Navigate to={`/requests/${legacyId}`} replace />;
  if (tab && /^\d+$/.test(tab)) return <RequestDetailPage id={Number(tab)} />;
  return <RequestQueue />;
}

function RequestDetailPage({ id }: { id: number }) {
  const q = useRequest(id);
  const perms = usePerms();
  return (
    <div className="min-w-0">
      <Button asChild variant="ghost" className="mb-3"><Link to="/requests">Back to requests</Link></Button>
      <PageHeader title="Request detail" description="What was requested, and where it is now." />
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : q.data ? (
        <div className="grid min-w-0 gap-4 lg:grid-cols-2" data-testid="request-detail">
          <div className="min-w-0 space-y-4">
            <RequestRow r={q.data} manager={perms.manageRequests} detail />
            {perms.manageRequests ? <Card className="space-y-3 p-4">
              <h2 className="font-semibold">What approval does</h2>
              {q.data.versions.map((v) => <p key={v.key} className="text-sm [overflow-wrap:anywhere]"><strong>{v.label}:</strong> {q.data?.progress.find((p) => p.key === v.key)?.approval}</p>)}
              <p className="text-xs text-[var(--ink-3)]">Approval uses the requested seasons for series. Available means every requested library is ready.</p>
            </Card> : null}
          </div>
          <RequestTimeline r={q.data} />
        </div>
      ) : null}
    </div>
  );
}

function RequestTimeline({ r }: { r: RequestDetail }) {
  const perms = usePerms();
  const versions = r.versions;
  const decision = r.decided_by?.display_name || r.decided_by?.username;
  return (
    <Card className="min-w-0 p-4">
      <h2 className="mb-4 font-semibold">Timeline</h2>
      <DownloadsOffNotice off={globalDownloadsOff(versions.map((v) => v.grab_state))} canManage={perms.admin} className="mb-4" />
      <ol className="space-y-5" data-testid="request-timeline">
        {requestTimeline(r, versionFetching(versions)).map((s) => <li key={s.key} className="flex min-w-0 gap-3" data-state={s.state}>
          <span aria-hidden className={cn("mt-1 size-3 shrink-0 rounded-full border border-[var(--line)]", s.state === "done" && "bg-[var(--ok-ink)]", s.state === "current" && "bg-[var(--accent-ink)]", s.state === "stopped" && "bg-[var(--bad-ink)]")} />
          <div className="min-w-0 flex-1 space-y-1 text-sm [overflow-wrap:anywhere]">
            <div className="font-semibold">{s.label}{s.state === "current" ? " · Now" : ""}</div>
            {s.at ? <time dateTime={s.at} className="block text-xs text-[var(--ink-3)]" title={new Date(s.at).toLocaleString()}>{fmtTime(s.at)}</time> : null}
            {s.key === "requested" ? r.user ? <p>By {r.user.display_name || r.user.username}</p> : r.mine ? <p>Someone else asked first; you joined</p> : null : null}
            {(s.key === "ready" || s.state === "stopped") && decision ? <p>{s.key === "ready" ? "Approved" : s.label} by {decision}</p> : null}
            {s.words ? <p>{s.words}</p> : null}
            {s.key === "ready" && (s.state === "current" || r.status === "failed") ? r.progress.map((p, i) => <div key={`${p.key}-${i}`} className="rounded-[5px] border border-[var(--line)] p-2">
              <p className="font-medium">{r.versions.find((v) => v.key === p.key)?.label || p.key}</p>
              <p>{p.words}</p>
              <VersionStatus state={versions.find((v) => v.key === p.key)?.grab_state} showMode settingsKey={p.key} canManage={perms.admin} />
              {p.episodes_total !== undefined ? <p>{p.episodes_available} of {p.episodes_total} requested episodes available</p> : null}
              {p.updated_at ? <p className="text-xs text-[var(--ink-3)]">Updated {fmtTime(p.updated_at)}</p> : null}
            </div>) : null}
          </div>
        </li>)}
      </ol>
    </Card>
  );
}

function RequestQueue() {
  const perms = usePerms();
  const { tab: rawTab } = useParams();
  const manager = perms.manageRequests;
  const upgrades = perms.manage && rawTab === "upgrades";
  const waiting = useUpgradeHolds({ state: "pending", limit: 1 }, perms.manage).data?.counts?.pending ?? 0;
  const tab = upgrades ? "upgrades" : manager ? (rawTab === "all" || rawTab === "mine" ? rawTab : "queue") : "mine";
  const [filter, setFilter] = useState(manager && tab === "queue" ? "pending" : "open");
  const status = tab === "queue" ? "pending" : filter;
  const list = useRequests({ status, mine: tab === "mine", limit: 100 }, !upgrades);
  const counts = list.data?.counts ?? {};
  const items = list.data?.items ?? [];
  return (
    <div>
      <PageHeader
        title="Requests"
        description={
          manager
            ? "What the household asked for. Approving adds the title with the requested libraries; nothing is searched while a library is in shadow."
            : "What you asked for, and where it is. Find something new with the search box and press Request."
        }
      />
      {manager || perms.manage ? (
        <RouteTabs
          active={tab}
          tabs={[
            ...(manager ? [{ id: "queue", label: "To decide", to: "/requests" }, { id: "all", label: "All", to: "/requests/all" }] : []),
            { id: "mine", label: "Mine", to: "/requests/mine" },
            ...(perms.manage ? [{ id: "upgrades", label: waiting > 0 ? `Upgrades awaiting approval (${waiting})` : "Upgrades awaiting approval", to: "/requests/upgrades" }] : []),
          ]}
        />
      ) : null}
      {upgrades ? <UpgradesWaiting /> : null}
      {upgrades ? null : tab !== "queue" ? (
        <div className="mb-4 flex flex-wrap gap-1.5" role="group" aria-label="Status">
          {FILTERS.map((f) => {
            const n = f.id === "all" ? Object.values(counts).reduce((a, b) => a + b, 0) : f.id === "open" ? (counts.pending ?? 0) + (counts.approved ?? 0) + (counts.processing ?? 0) : f.id.split(",").reduce((a, s) => a + (counts[s] ?? 0), 0);
            return (
              <button
                key={f.id}
                type="button"
                aria-pressed={filter === f.id}
                onClick={() => setFilter(f.id)}
                className={cn(
                  "rounded-[4px] border border-[var(--line)] px-2.5 py-1 text-[13px] font-medium text-[var(--ink-3)] transition hover:border-[var(--line-2)]",
                  filter === f.id && "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]",
                )}
              >
                {f.label}
                {list.data ? <span className="ml-1.5 tabular-nums opacity-70">{n}</span> : null}
              </button>
            );
          })}
        </div>
      ) : null}
      {upgrades ? null : list.isPending ? (
        <Loading />
      ) : list.error ? (
        <ErrorState error={list.error} />
      ) : items.length === 0 ? (
        <Card>
          <Empty icon={tab === "queue" ? CheckCircle2 : Inbox}>
            {tab === "queue" ? "Nothing waits for a decision." : "No requests here."}
          </Empty>
        </Card>
      ) : (
        <ul className="space-y-3" data-testid="request-list">
          {items.map((r) => (
            <li key={r.id}>
              <RequestRow r={r} manager={manager} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function RequestRow({ r, manager, detail }: { r: Request; manager: boolean; detail?: boolean }) {
  const st = REQUEST_STATUS[r.status] ?? { label: r.status, tone: "neutral" as const };
  const admin = usePerms().admin;
  const [mode, setMode] = useState<"" | "edit" | "decline">("");
  const approve = useApproveRequest();
  const cancel = useCancelRequest();
  const err = approve.error ?? cancel.error;
  const title = (
    <>
      {r.name}
      {r.year ? <span className="font-normal text-[var(--ink-3)]"> ({r.year})</span> : null}
    </>
  );
  return (
    <Card
      className="p-0"
      data-testid="request-row"
      data-request-id={r.id}
      data-status={r.status}
    >
      <div className="flex gap-3 p-3 sm:gap-4 sm:p-4">
        <Poster src={r.poster} title={r.name} className="relative aspect-[2/3] w-14 shrink-0 rounded-[4px] ring-1 ring-[var(--line)] sm:w-[68px]" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0">
              <div className="mb-1 flex flex-wrap items-center gap-1.5">
                <Tag className="uppercase tracking-wider">{{ movie: "Movie", series: "Series", anime: "Anime" }[facetOf(r.kind, r.anime)]}</Tag>
                <Badge tone={st.tone} data-testid="request-status">{st.label}</Badge>
              </div>
              <h3 className="text-[15px] font-semibold leading-snug text-[var(--ink)] [overflow-wrap:anywhere]">
                {r.title_id ? <Link to={`/title/${r.title_id}`} className="hover:underline">{title}</Link> : title}
              </h3>
            </div>
            <div className="flex flex-wrap gap-1.5">
              {!detail ? <Button asChild size="sm" variant="outline"><Link to={`/requests/${r.id}`}>View request</Link></Button> : null}
              {manager && r.status === "pending" ? (
                <>
                  <Button size="sm" onClick={() => approve.mutate({ id: r.id })} disabled={approve.isPending} data-testid="request-approve">
                    {approve.isPending ? <Loader2 className="animate-spin" /> : <Check />} Approve
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setMode(mode === "edit" ? "" : "edit")}
                    aria-label="Edit request"
                    title="Edit request: change libraries or seasons, then approve"
                    aria-expanded={mode === "edit"}
                    data-testid="request-edit"
                  >
                    <Pencil /> <span className="hidden sm:inline">Edit request</span>
                  </Button>
                  <Button size="sm" variant="ghost" className="text-[var(--bad-ink)]" onClick={() => setMode(mode === "decline" ? "" : "decline")} data-testid="request-decline">
                    <X /> Decline
                  </Button>
                </>
              ) : r.mine && r.status === "pending" ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => { if (window.confirm(`Cancel your request for ${r.name}?`)) cancel.mutate(r.id); }}
                  disabled={cancel.isPending}
                  data-testid="request-cancel"
                >
                  Cancel
                </Button>
              ) : null}
            </div>
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {r.versions.map((v) => (
              <span
                key={v.key}
                data-vkey={v.key}
                data-available={v.available ? "1" : "0"}
                className={cn(
                  "inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium",
                  v.available
                    ? "border-[var(--th-success-border)] bg-[var(--ok-soft)] text-[var(--ok-ink)]"
                    : "border-[var(--line)] text-[var(--ink-3)]",
                )}
              >
                {v.available ? <Check className="size-3" /> : null}
                {v.label}
                {!v.available ? <VersionStatus state={v.grab_state} why={false} canManage={admin} settingsKey={v.key} /> : null}
              </span>
            ))}
            {seasonsText(r) ? <span className="text-xs text-[var(--ink-3)]">{seasonsText(r)}</span> : null}
          </div>
          <p className="mt-2 text-xs text-[var(--ink-3)]">
            {manager || !r.mine ? `${who(r)} · ` : ""}asked {fmtAgo(r.created_at)}
            {r.decided_by && r.status !== "pending" && r.status !== "canceled" ? ` · ${r.status === "declined" ? "declined" : "approved"} by ${r.decided_by.display_name || r.decided_by.username} ${fmtAgo(r.decided_at)}` : ""}
          </p>
          {r.note ? <p className="mt-1.5 text-sm text-[var(--ink)] [overflow-wrap:anywhere]">“{r.note}”</p> : null}
          {r.status === "declined" && r.reason ? (
            <p className="mt-1.5 rounded-[5px] bg-[var(--bad-soft)] px-2.5 py-1.5 text-xs text-[var(--bad-ink)]" data-testid="request-reason">
              {r.reason}
            </p>
          ) : null}
          {err ? <p className="mt-1.5 text-xs text-[var(--bad-ink)]">{err instanceof ApiError ? err.message : String(err)}</p> : null}
        </div>
      </div>
      {mode === "edit" ? <ApproveEditor r={r} onDone={() => setMode("")} /> : null}
      {mode === "decline" ? <DeclineForm r={r} onDone={() => setMode("")} /> : null}
    </Card>
  );
}

/** Approve with changed versions or seasons. */
function ApproveEditor({ r, onDone }: { r: Request; onDone: () => void }) {
  const opts = useAddOptions({ kind: r.kind as "movie" | "series", name: r.name, tmdb_id: r.tmdb_id, title_id: r.title_id, anime: r.anime }, r.anime);
  const approve = useApproveRequest();
  const [versions, setVersions] = useState<string[]>(r.versions.map((v) => v.key));
  const [seasons, setSeasons] = useState<number[] | null>(null);
  const all = (opts.data?.seasons ?? []).filter((s) => s.season > 0).map((s) => s.season);
  const chosenSeasons = seasons ?? (r.seasons.length > 0 ? r.seasons : all);
  const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const choices = (opts.data?.versions ?? []).filter((v) => v.fits || versions.includes(v.key) || v.on);
  return (
    <div className="space-y-3 border-t border-[var(--line)] p-3 sm:p-4" data-testid="approve-editor">
      {opts.isPending ? <Loading /> : null}
      <div>
        <div className="mb-1.5 text-xs font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Libraries</div>
        <div className="flex flex-wrap gap-1.5">
          {choices.map((v) => (
            <button
              key={v.key}
              type="button"
              aria-pressed={versions.includes(v.key)}
              disabled={v.on}
              onClick={() => setVersions((l) => toggle(l, v.key))}
              className={cn(
                "rounded-md border border-[var(--line)] px-2.5 py-1 text-xs font-medium text-[var(--ink-3)] disabled:opacity-60",
                (versions.includes(v.key) || v.on) && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_14%,transparent)] text-[var(--accent-ink)]",
              )}
            >
              {v.label || v.key}
              {v.on ? " · in library" : ""}
            </button>
          ))}
        </div>
        {choices.filter((v) => versions.includes(v.key)).map((v) => <p key={v.key} className="mt-2 text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]"><strong>{v.label || v.key}:</strong> {approvalWords(v.searchable)}</p>)}
      </div>
      {r.kind === "series" && all.length > 0 ? (
        <div>
          <div className="mb-1.5 text-xs font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Seasons</div>
          <div className="flex flex-wrap gap-1.5">
            {all.map((n) => (
              <button
                key={n}
                type="button"
                aria-pressed={chosenSeasons.includes(n)}
                onClick={() => setSeasons(toggle(chosenSeasons, n))}
                className={cn(
                  "rounded-md border border-[var(--line)] px-2.5 py-1 text-xs font-medium text-[var(--ink-3)]",
                  chosenSeasons.includes(n) && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_14%,transparent)] text-[var(--accent-ink)]",
                )}
              >
                Season {n}
              </button>
            ))}
          </div>
        </div>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          disabled={versions.length === 0 || approve.isPending}
          onClick={() =>
            approve.mutate(
              { id: r.id, versions, seasons: r.kind === "series" && seasons !== null ? (seasons.length === all.length ? [] : seasons) : undefined },
              { onSuccess: onDone },
            )
          }
        >
          {approve.isPending ? <Loader2 className="animate-spin" /> : <Check />} Approve these
        </Button>
        <Button size="sm" variant="ghost" onClick={onDone}>Cancel</Button>
        {approve.error ? <span className="text-xs text-[var(--bad-ink)]">{String((approve.error as Error).message)}</span> : null}
      </div>
    </div>
  );
}

function DeclineForm({ r, onDone }: { r: Request; onDone: () => void }) {
  const decline = useDeclineRequest();
  const [reason, setReason] = useState("");
  return (
    <form
      className="flex flex-wrap items-center gap-2 border-t border-[var(--line)] p-3 sm:p-4"
      onSubmit={(e) => {
        e.preventDefault();
        decline.mutate({ id: r.id, reason: reason.trim() }, { onSuccess: onDone });
      }}
    >
      <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Reason (the requester sees it)" maxLength={500} className="min-w-0 flex-1 basis-56" aria-label="Reason" data-testid="decline-reason" autoFocus />
      <Button size="sm" variant="destructive" type="submit" disabled={decline.isPending} data-testid="decline-confirm">
        {decline.isPending ? <Loader2 className="animate-spin" /> : <Send />} Decline
      </Button>
      <Button size="sm" variant="ghost" type="button" onClick={onDone}>Back</Button>
      {decline.error ? <span className="text-xs text-[var(--bad-ink)]">{String((decline.error as Error).message)}</span> : null}
    </form>
  );
}
