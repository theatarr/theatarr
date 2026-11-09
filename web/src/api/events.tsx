// The live event stream (GET /api/theatarr/v1/events, Server-Sent Events). One connection per tab, opened once signed in.
// jobs events refresh the job queries and carry the queue counts; gateway events carry the full /gateway state and
// replace the cached query; download and title events refresh the activity, Continue row and calendar, and a
// download's state change shows a toast (live-activity.ts); request events refresh the requests and tell the
// requester in a toast; users events refresh the users screen. The server sends each listener only the events its
// account may see (jobs and gateway to admins). The browser reconnects on its own (retry: 5000).
import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { API_BASE } from "./client";
import type { Gateway } from "./hooks";
import { qk } from "./query-keys";
import { onDownloadEvent, onTitleEvent } from "./live-activity";
import { requestToast, type RequestEvent } from "./requests";
import { toast, Toaster } from "@/components/toaster";

export type Live = {
  connected: boolean;
  /** When the current connection opened (ms); a snapshot from before it is from an earlier connection. */
  connectedAt?: number;
  /** When the connection last dropped (ms), if it has. */
  lostAt?: number;
  jobCounts?: Record<string, number>;
  jobsAt?: number;
  gateway?: Gateway;
  gatewayAt?: number;
  lastEventAt?: number;
};

const LiveContext = createContext<Live>({ connected: false });

export function LiveProvider({ enabled, seeFiles = false, children }: { enabled: boolean; seeFiles?: boolean; children: ReactNode }) {
  const qc = useQueryClient();
  const [live, setLive] = useState<Live>({ connected: false });
  // Read at each event, so a permission change does not reopen the stream.
  const seeFilesRef = useRef(seeFiles);
  seeFilesRef.current = seeFiles;
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const es = new EventSource(`${API_BASE}/events`);
    es.onopen = () => setLive((l) => ({ ...l, connected: true, connectedAt: Date.now() }));
    es.onerror = () => setLive((l) => (l.connected || !l.lostAt ? { ...l, connected: false, lostAt: Date.now() } : l));
    es.addEventListener("jobs", (e) => {
      const d = JSON.parse((e as MessageEvent).data) as { counts?: Record<string, number>; changed?: unknown[] };
      const now = Date.now();
      setLive((l) => ({ ...l, connected: true, connectedAt: l.connected ? l.connectedAt : now, jobCounts: d.counts, jobsAt: now, lastEventAt: now }));
      if (d.changed && d.changed.length > 0) {
        qc.invalidateQueries({ queryKey: qk.jobs.all });
        qc.invalidateQueries({ queryKey: qk.status });
        qc.invalidateQueries({ queryKey: qk.imports.all });
      }
    });
    es.addEventListener("gateway", (e) => {
      const g = JSON.parse((e as MessageEvent).data) as Gateway;
      qc.setQueryData(qk.gateway, g);
      const now = Date.now();
      setLive((l) => ({ ...l, connected: true, connectedAt: l.connected ? l.connectedAt : now, gateway: g, gatewayAt: now, lastEventAt: now }));
    });
    es.addEventListener("download", (e) => onDownloadEvent(qc, (e as MessageEvent).data, Date.now(), seeFilesRef.current));
    es.addEventListener("title", (e) => onTitleEvent(qc, (e as MessageEvent).data));
    es.addEventListener("request", (e) => {
      const ev = JSON.parse((e as MessageEvent).data) as RequestEvent;
      void qc.invalidateQueries({ queryKey: ["requests"] });
      void qc.invalidateQueries({ queryKey: ["search"] });
      if (ev.title_id) void qc.invalidateQueries({ queryKey: ["title"] });
      const t = ev.mine ? requestToast(ev) : null;
      if (t) toast({ ...t, to: "/requests" });
    });
    es.addEventListener("users", () => void qc.invalidateQueries({ queryKey: ["users"] }));
    return () => es.close();
  }, [enabled, qc]);
  return (
    <LiveContext.Provider value={live}>
      {children}
      <Toaster />
    </LiveContext.Provider>
  );
}

export function useLive() {
  return useContext(LiveContext);
}
