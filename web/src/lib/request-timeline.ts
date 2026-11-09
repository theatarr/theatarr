import type { RequestDetail } from "@/api/requests";

export type RequestStep = {
  key: string;
  label: string;
  state: "done" | "current" | "todo" | "stopped";
  at?: string;
  words?: string;
};

/** What a failed request or download says: that it did not work and who can look at it, without detail. */
export const FAILED_WORDS = "This did not work. A manager can look at it.";

/** Request rows record creation, decision and the latest status time, not a complete transition history. */
export function requestTimeline(r: RequestDetail, fetching = true): RequestStep[] {
  const steps: RequestStep[] = [{ key: "requested", label: "Requested", state: "done", at: r.created_at }];
  const closed = r.status === "declined" || r.status === "canceled";
  if (!r.approved_at_creation) {
    steps.push({ key: "approval", label: "Awaiting approval", state: r.status === "pending" ? "current" : "done", at: r.created_at, words: r.status === "pending" ? "Waiting for approval" : undefined });
  }
  if (closed) {
    steps.push({ key: r.status, label: r.status === "declined" ? "Declined" : "Canceled", state: "stopped", at: r.decided_at, words: r.reason });
    return steps;
  }
  const available = r.versions.length > 0 && r.versions.every((v) => v.available);
  if (r.status === "failed" && !available) {
    // A failure is explained before anything about paused downloads, in plain words for every account.
    steps.push({ key: "ready", label: "Did not work", state: "stopped", at: r.decided_at, words: FAILED_WORDS });
  } else {
    steps.push({ key: "ready", label: available || fetching || r.status === "pending" ? "Getting ready" : "Not being fetched", state: available ? "done" : r.status === "pending" ? "todo" : "current", at: r.status === "pending" ? undefined : r.decided_at, words: !available && !fetching && r.status !== "pending" ? "This request is approved, but downloads are off for its libraries. It is not being fetched yet." : undefined });
  }
  // updated_at is a status time only when the stored status agrees. Live availability may arrive before sync.
  steps.push({ key: "available", label: "Available", state: available ? "done" : "todo", at: available && r.status === "available" ? r.updated_at : undefined });
  return steps;
}

export function approvalWords(searchable: boolean): string {
  return searchable
    ? "Approval turns on this library and searches for a release that matches."
    : "Approval records this request; automatic downloads are paused";
}
