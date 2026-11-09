// The title page's request card for accounts without the add permission (docs/design/users.md): your requests for this
// title and where they are, and one action that follows the request state (request-state.ts): Request (opens the
// request sheet), View request (your open request, selected on the Requests page), or, when every version you could ask
// for is monitored, what those account-applicable copies say about readiness (Available only when it can be watched,
// else Getting ready or Waiting for a copy, or Partly available / Not available yet when nothing fetches them). On the Episodes tab RequestMoreSeasons offers the seasons a series still lacks.
import { CheckCircle2, Clock, Inbox, Plus, Send } from "lucide-react";
import { Link } from "react-router";
import { useAuth } from "@/api/auth";
import { useAddOptions, type AddTarget } from "@/api/add";
import type { Version } from "@/api/library";
import { REQUEST_STATUS, useRequests } from "@/api/requests";
import { useSearchUI } from "@/components/search/search-context";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtAgo } from "@/lib/format";
import {
  applicableRequestVersionKeys,
  isOpen,
  moreSeasonsOnly,
  requestAction,
  requestLink,
  requestStateWords,
  requestVersions,
  settledWords,
  versionFetching,
} from "@/lib/request-state";
import { readiness } from "@/lib/title-layout";
import { cn } from "@/lib/utils";

type Target = AddTarget & { title_id: number };

/** The caller's requests for the title and the action they lead to (shared by the card and the hero button). */
function useTitleRequest(target: Target) {
  const me = useAuth();
  const opts = useAddOptions(target, Boolean(target.anime));
  const mine = useRequests({ title_id: target.title_id, mine: true, status: "all", limit: 10 });
  const wanted = requestVersions(opts.data?.versions ?? [], me.data?.request_versions);
  const applicable = applicableRequestVersionKeys(opts.data?.versions ?? [], me.data?.request_versions);
  const items = mine.data?.items ?? [];
  const open = items.find((r) => isOpen(r.status));
  // On the title page itself "Available" needs no link.
  const action = requestAction({ mine: open, requestable: opts.data ? wanted.length : undefined });
  return { opts, wanted, items, action, applicable, open };
}

/** The hero's action beside the title: Request (or Request more seasons) or View request (nothing when everything is here). */
export function TitleRequestButton({ target, className }: { target: Target; className?: string }) {
  const { openAdd } = useSearchUI();
  const { opts, wanted, action } = useTitleRequest(target);
  if (action.kind === "view-request") {
    return (
      <Button asChild size="sm" variant="outline" className={className} data-testid="hero-view-request">
        <Link to={action.to}>
          <Inbox /> View request
        </Link>
      </Button>
    );
  }
  if (action.kind === "request" && !opts.isPending) {
    return (
      <Button size="sm" onClick={() => openAdd(target)} className={className} data-testid="hero-request">
        {moreSeasonsOnly(wanted) ? (
          <>
            <Plus /> Request more seasons
          </>
        ) : (
          <>
            <Send /> Request
          </>
        )}
      </Button>
    );
  }
  return null;
}

export function TitleRequest({ target, versions }: { target: Target; versions: Version[] }) {
  const { openAdd } = useSearchUI();
  const { opts, wanted, items, action, applicable, open } = useTitleRequest(target);
  const applicableSet = new Set(open?.versions.map((v) => v.key) ?? applicable);
  const applicableVersions = versions.filter((v) => applicableSet.has(v.key ?? ""));
  const settled = settledWords(readiness(target.kind, applicableVersions).tone, versionFetching(applicableVersions), applicableVersions.map((v) => v.label || v.key || ""));
  const more = moreSeasonsOnly(wanted);
  return (
    <Card data-testid="title-request" data-action={action.kind}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h2 className="font-semibold">Request</h2>
          <p className="text-sm text-muted-foreground">
            {opts.isPending
              ? "…"
              : action.kind === "view-request"
                ? requestStateWords(action.status, versionFetching(applicableVersions))
                : action.kind === "available"
                  ? settled.sentence
                  : more
                    ? "Some seasons are not in the library yet."
                    : `Not here yet in ${wanted.map((v) => v.label).join(" and ")}.`}
          </p>
        </div>
        {action.kind === "view-request" ? (
          <Button asChild variant="outline" data-testid="title-view-request">
            <Link to={action.to}>
              <Inbox /> View request
            </Link>
          </Button>
        ) : action.kind === "request" && !opts.isPending ? (
          <Button onClick={() => openAdd(target)} data-testid="title-request-button">
            {more ? <Plus /> : <Send />}
            {more ? "Request more seasons" : "Request"}
          </Button>
        ) : action.kind === "available" ? (
          <span
            data-testid="title-request-settled"
            data-tone={settled.tone}
            className={cn(
              "inline-flex items-center gap-1.5 text-sm font-medium",
              settled.tone === "positive" ? "text-[var(--ok-ink)]" : settled.tone === "partial" ? "text-[var(--accent-ink)]" : "text-muted-foreground",
            )}
          >
            {settled.tone === "positive" ? <CheckCircle2 className="size-4" /> : <Clock className="size-4" />} {settled.badge}
          </span>
        ) : null}
      </div>
      {items.length > 0 ? (
        <ul className="mt-3 divide-y divide-[var(--line)]">
          {items.map((r) => {
            const st = REQUEST_STATUS[r.status] ?? { label: r.status, tone: "neutral" as const };
            return (
              <li key={r.id} className="flex flex-wrap items-center gap-2 py-2 text-sm">
                <Badge tone={st.tone}>{st.label}</Badge>
                <Link to={requestLink(r.id)} className="min-w-0 flex-1 text-muted-foreground hover:text-foreground hover:underline">
                  {r.versions.map((v) => v.label).join(", ")} · asked {fmtAgo(r.created_at)}
                  {r.status === "declined" && r.reason ? ` · ${r.reason}` : ""}
                </Link>
              </li>
            );
          })}
        </ul>
      ) : null}
      <Link to="/requests" className="mt-3 inline-flex items-center gap-1.5 text-xs font-medium text-[var(--accent-ink)] hover:underline">
        <Inbox className="size-3.5" /> All your requests
      </Link>
    </Card>
  );
}

/**
 * The Episodes tab's entry for a series the library has: when the caller may request and some wanted version still
 * lacks seasons, "Request more seasons" opens the request sheet with just those seasons to pick. Nothing otherwise
 * (the request card above says the rest).
 */
export function RequestMoreSeasons({ target }: { target: Target }) {
  const { openAdd } = useSearchUI();
  const { opts, wanted, action } = useTitleRequest(target);
  if (opts.isPending || action.kind !== "request" || !moreSeasonsOnly(wanted)) return null;
  const seasons = [...new Set(wanted.flatMap((v) => v.more ?? []))].sort((a, b) => a - b);
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_8%,transparent)] px-3 py-2.5 text-sm" data-testid="request-more-seasons-entry">
      <span className="min-w-0">
        <span className="font-medium">Not every season is here.</span>
        <span className="text-muted-foreground"> You can ask for {seasons.length === 1 ? `season ${seasons[0]}` : `seasons ${seasons.join(", ")}`}.</span>
      </span>
      <Button size="sm" onClick={() => openAdd(target)} data-testid="request-more-seasons-button">
        <Plus /> Request more seasons
      </Button>
    </div>
  );
}
