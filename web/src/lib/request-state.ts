// What a title's request action says for the caller (docs/design/ui.md "Requests"): Request, View request (the
// caller already has an open request for it: a link to it), or settled (everything the caller could ask for is
// monitored: in the library, on its way, or monitored with nothing fetching it; settledWords says which from the request's applicable versions, so a
// playable copy outside the account's request set never reads "Available"). A series version the title already has stays requestable for the
// seasons it does not fully monitor (open_seasons from GET add/options: "Request more seasons"). Search cards, the title page and the request sheet share it, so the same title
// never offers Request in one place and a dead end in another.

/** The request fields these helpers read (a GET requests item). */
export type RequestRef = { id: number; status: string; kind: string; title_id?: number | null; tmdb_id?: number | null; versions?: readonly { key: string }[] };

/** A request still in play: it is the one to view, not a new one to make. */
export const OPEN_STATUSES = ["pending", "approved", "processing"] as const;

export function isOpen(status: string): boolean {
  return (OPEN_STATUSES as readonly string[]).includes(status);
}

/** The Requests page with that request selected (and, for managers, in the approval queue). */
export function requestLink(id: number): string {
  return `/requests?request=${id}`;
}

/** The caller's open requests by title: by library title id, and by kind and TMDb id (a title not added yet). */
export type RequestIndex = Map<string, RequestRef>;

export function indexRequests(items: readonly RequestRef[] | undefined): RequestIndex {
  const m: RequestIndex = new Map();
  for (const r of items ?? []) {
    if (!isOpen(r.status)) continue;
    // The newest open request wins (the API lists newest first): keep the first.
    if (r.title_id && !m.has(`t:${r.title_id}`)) m.set(`t:${r.title_id}`, r);
    if (r.tmdb_id && !m.has(`${r.kind}:${r.tmdb_id}`)) m.set(`${r.kind}:${r.tmdb_id}`, r);
  }
  return m;
}

/** The caller's open request for a title, by its library id or its kind and TMDb id. */
export function findRequest(
  idx: RequestIndex,
  t: { kind?: string; title_id?: number | null; tmdb_id?: number | null },
): RequestRef | undefined {
  return (t.title_id ? idx.get(`t:${t.title_id}`) : undefined) ?? (t.tmdb_id && t.kind ? idx.get(`${t.kind === "series" ? "series" : "movie"}:${t.tmdb_id}`) : undefined);
}

export type RequestAction =
  | { kind: "view-request"; id: number; status: string; to: string; label: string }
  | { kind: "available"; to?: string; label: string }
  | { kind: "request"; label: string };

/**
 * The action for a title. mine: the caller's open request for it. requestable: how many versions a new request would
 * ask for (undefined while unknown: then Request, which the sheet settles). titleId: the library title, which the
 * Available action opens.
 */
export function requestAction(o: { mine?: RequestRef; requestable?: number; titleId?: number | null }): RequestAction {
  if (o.mine) return { kind: "view-request", id: o.mine.id, status: o.mine.status, to: requestLink(o.mine.id), label: "View request" };
  if (o.requestable === 0) {
    return o.titleId
      ? { kind: "available", to: `/title/${o.titleId}`, label: "View availability" }
      : { kind: "available", label: "Available" };
  }
  return { kind: "request", label: "Request" };
}

/** The request's state in a sentence for the requester. */
export function requestStateWords(status: string, fetching = true): string {
  switch (status) {
    case "pending":
      return "You asked for this; it is waiting for approval.";
    case "approved":
      return fetching ? "You asked for this; it was approved and is getting ready." : "You asked for this; it was approved but is not being fetched yet because downloads are off for its libraries.";
    case "processing":
      return fetching ? "You asked for this; it is getting ready." : "You asked for this; it is not being fetched yet because downloads are off for its libraries.";
  }
  return "You asked for this.";
}

/** A version a request would ask for; more: a series version the title has, requestable for these seasons only. */
export type WantedVersion = { key: string; label: string; more?: number[] };

/**
 * The versions a request by this account would ask for, from the add options and the account's set (the set's
 * versions that fit, else every version that fits): those the title lacks, and series versions it has with seasons
 * left to monitor.
 */
export function requestVersions(
  versions: { key: string; label?: string; fits: boolean; on: boolean; open_seasons?: number[] }[],
  set: readonly string[] | undefined,
): WantedVersion[] {
  const keys = new Set(applicableRequestVersionKeys(versions, set));
  const out: WantedVersion[] = [];
  for (const v of versions) {
    if (!keys.has(v.key)) continue;
    const label = v.label || v.key;
    if (!v.on) out.push({ key: v.key, label });
    else if (v.open_seasons?.length) out.push({ key: v.key, label, more: v.open_seasons });
  }
  return out;
}

/** The account's applicable versions, whether or not the title already has them. */
export function applicableRequestVersionKeys(
  versions: { key: string; fits: boolean }[],
  set: readonly string[] | undefined,
): string[] {
  const fits = versions.filter((v) => v.fits);
  const mine = set && set.length > 0 ? fits.filter((v) => set.includes(v.key)) : [];
  return (mine.length > 0 ? mine : fits).map((v) => v.key);
}

/** Whether every wanted version is one the title has: the request is for more seasons only. */
export function moreSeasonsOnly(wanted: readonly WantedVersion[]): boolean {
  return wanted.length > 0 && wanted.every((v) => v.more);
}

/** The seasons a request can still ask for: every season for a version the title lacks, else each version's open ones. */
export function requestableSeasons(wanted: readonly WantedVersion[], seasons: readonly number[]): number[] {
  if (wanted.some((v) => !v.more)) return [...seasons];
  return seasons.filter((n) => wanted.some((v) => v.more?.includes(n)));
}

/** The seasons a request sends: none (every season) when all are picked and all are requestable, else the picked. */
export function seasonsToSend(picked: readonly number[], requestable: readonly number[], all: readonly number[]): number[] | undefined {
  return picked.length === all.length && requestable.length === all.length ? undefined : [...picked];
}

/**
 * The settled state in words, from the applicable request versions' readiness: Available only when they can be
 * watched; partly ready; or monitored and waiting for a copy. fetching: whether any of those copies is acquiring now
 * (versionFetching); only then does the rest read as on its way. A copy in shadow mode, with grabbing off or with the
 * router off is monitored but nothing fetches it, and the words say so plainly. labels: those copies' names; the
 * sentence names them, so it reads as about what this account can ask for, not the whole title (whose hero line may
 * say another copy is ready to watch).
 */
export function settledWords(tone: "positive" | "partial" | "neutral" | undefined, fetching = true, labels: readonly string[] = []): { badge: string; sentence: string; tone: "positive" | "partial" | "neutral" } {
  const what = labels.length > 0 ? `What you can ask for (${labels.join(" and ")})` : "Everything you can ask for";
  if (tone === "positive") return { badge: "Available", sentence: `${what} is in the library.`, tone };
  if (tone === "partial") {
    return fetching
      ? { badge: "Getting ready", sentence: `${what} is already monitored; some of it is ready to watch and the rest is on its way.`, tone }
      : { badge: "Partly available", sentence: `${what} is already monitored; some of it is ready to watch. The rest is not being fetched yet.`, tone };
  }
  return fetching
    ? { badge: "Waiting for a copy", sentence: `${what} is already monitored but not ready to watch yet. It is being looked for.`, tone: "neutral" }
    : { badge: "Not available yet", sentence: `${what} is already monitored but not ready to watch yet, and it is not being fetched yet.`, tone: "neutral" };
}

/**
 * The request sheet's promise before sending: an auto-approve account's request is approved at once, except when it
 * joins a pending request that asks for more than it does (POST /requests, not_approved "asks_more"). manages: the
 * account holds manage_requests, so it is told it can approve the request itself rather than wait for a manager.
 */
export function approvalNotice(autoApprove: boolean, manages = false): string {
  if (!autoApprove) return "Your request needs approval from a manager.";
  return manages
    ? "Your request is approved automatically. If someone already asked for more of this title, it waits for approval; you can approve it in Requests."
    : "Your request is approved automatically. If someone already asked for more of this title, a manager approves it.";
}

/** What the request sheet says once a request is sent (a POST /requests answer). */
export function requestDoneWords(result: { approved: boolean; merged: boolean; not_approved?: string }, manages = false): string {
  if (result.approved) return "Approved automatically. Open your request to follow its progress.";
  if (result.merged && result.not_approved === "asks_more") {
    return manages
      ? "Someone asked for this already, for more than you did, so it waits for approval; you can approve it in Requests. You are on that request too."
      : "Someone asked for this already, for more than you did, so a manager approves it. You are on that request too.";
  }
  if (result.merged) return "Someone asked for this already; you are on that request too.";
  return "Sent. An admin will look at it; you will see it change here and on your requests page.";
}

/** The grab gates every account sees on a version (control.grab_state; redaction keeps it). */
type GrabGates = { mode: string; paused: boolean; pause_forced: boolean; will_grab?: boolean };

/**
 * Whether any copy may grab, from the server decision shared with add options and request detail. Older server
 * responses without that decision keep the previous optimistic fallback.
 */
export function versionFetching(versions: readonly { control?: { grab_state?: GrabGates } | null; grab_state?: GrabGates }[]): boolean {
  const gates = versions.map((v) => v.control?.grab_state ?? v.grab_state);
  if (gates.length === 0 || gates.some((g) => !g)) return true;
  return gates.some((g) => g!.will_grab ?? true);
}
