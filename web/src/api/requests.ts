// Requests (docs/design/users.md): GET requests, GET requests/summary, POST requests, POST requests/{id}/approve|decline,
// DELETE requests/{id}. request events from the stream refresh these (events.tsx).
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { indexRequests, type RequestIndex } from "@/lib/request-state";
import { api, qs, type Schemas } from "./client";

export type Request = Schemas["Request"];
export type RequestDetail = Schemas["RequestDetail"];
export type RequestPage = Schemas["RequestPage"];
export type CreateRequest = Schemas["CreateRequest"];
export type CreateResult = Schemas["CreateResult"];
export type RequestSummary = Schemas["RequestSummary"];
export type RequestEvent = Schemas["RequestEvent"];

export type RequestFilter = { status?: string; mine?: boolean; title_id?: number; tmdb_id?: number; kind?: string; offset?: number; limit?: number };

export const useRequests = (f: RequestFilter, enabled = true) =>
  useQuery({
    queryKey: ["requests", "list", f],
    queryFn: () => api<RequestPage>("/requests" + qs({ ...f, mine: f.mine ? 1 : undefined })),
    enabled,
  });

/** One request (GET requests/{id}): a manager any, a user their own (404 otherwise). */
export const useRequest = (id: number | undefined) =>
  useQuery({ queryKey: ["requests", "one", id], queryFn: () => api<RequestDetail>(`/requests/${id}`), enabled: !!id, refetchInterval: 15_000 });

/**
 * The caller's open requests, indexed by title (request-state.ts), for the request actions on search cards and the
 * title page. One call: GET requests?mine=1&status=open (the caller's own only; users never see others').
 */
export function useMyOpenRequests(enabled = true): RequestIndex {
  const q = useRequests({ status: "open", mine: true, limit: 200 }, enabled);
  return useMemo(() => indexRequests(q.data?.items), [q.data]);
}

export const useRequestSummary = () =>
  useQuery({ queryKey: ["requests", "summary"], queryFn: () => api<RequestSummary>("/requests/summary"), staleTime: 30_000 });

function refresh(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: ["requests"] });
  void qc.invalidateQueries({ queryKey: ["search"] });
  void qc.invalidateQueries({ queryKey: ["titles"] });
  void qc.invalidateQueries({ queryKey: ["title"] });
}

export function useCreateRequest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateRequest) => api<CreateResult>("/requests", { method: "POST", body }),
    onSuccess: () => refresh(qc),
  });
}

export function useApproveRequest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, versions, seasons }: { id: number; versions?: string[]; seasons?: number[] }) =>
      api<Schemas["ApproveResult"]>(`/requests/${id}/approve`, { method: "POST", body: { versions, seasons } }),
    onSuccess: () => refresh(qc),
  });
}

export function useDeclineRequest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reason }: { id: number; reason: string }) =>
      api<Request>(`/requests/${id}/decline`, { method: "POST", body: { reason } }),
    onSuccess: () => refresh(qc),
  });
}

export function useCancelRequest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api<void>(`/requests/${id}`, { method: "DELETE" }),
    onSuccess: () => refresh(qc),
  });
}

export const REQUEST_STATUS: Record<string, { label: string; tone: "neutral" | "info" | "positive" | "warning" | "negative" | "accent" }> = {
  pending: { label: "Pending", tone: "warning" },
  approved: { label: "Approved", tone: "info" },
  processing: { label: "Processing", tone: "accent" },
  available: { label: "Available", tone: "positive" },
  failed: { label: "Failed", tone: "negative" },
  declined: { label: "Declined", tone: "negative" },
  canceled: { label: "Canceled", tone: "neutral" },
};

/** A request event's toast for its requester, in plain words (null: no toast). */
export function requestToast(ev: RequestEvent): { title: string; body: string; tone: "ok" | "info" | "warn" | "bad" } | null {
  switch (ev.change === "status" || ev.change === "approved" || ev.change === "declined" ? ev.status : "") {
    case "approved":
      return { title: ev.name, body: "Your request was approved.", tone: "info" };
    case "processing":
      return { title: ev.name, body: "On its way: downloading now.", tone: "info" };
    case "available":
      return { title: ev.name, body: "Now available.", tone: "ok" };
    case "failed":
      return { title: ev.name, body: "The download failed; it will be tried again.", tone: "warn" };
    case "declined":
      return { title: ev.name, body: "Your request was declined.", tone: "bad" };
  }
  return null;
}
