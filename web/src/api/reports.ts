// Problem reports (docs/design/users.md "Report a problem"): GET reports, POST reports, POST reports/{id}/fix,
// POST reports/{id}/replace. problem events from the stream refresh these (events.tsx) and tell the reporter when a
// report is marked fixed.
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";

export type ProblemReport = Schemas["ProblemReport"];
export type ReportFile = Schemas["ReportFile"];
export type ReportPage = Schemas["ReportPage"];
export type CreateReport = Schemas["CreateReport"];
export type ReplaceResult = Schemas["ReplaceResult"];
export type ProblemEvent = Schemas["ProblemEvent"];

/** What can be wrong, in the order the sheet offers them. */
export const REPORT_KINDS: { id: CreateReport["kind"]; label: string }[] = [
  { id: "picture", label: "Picture" },
  { id: "sound", label: "Sound" },
  { id: "subtitles", label: "Subtitles" },
  { id: "wont_play", label: "Won't play" },
  { id: "other", label: "Something else" },
];

/** Managers: every open report; everyone else (or mine): their own. */
export const useReports = (mine = false) =>
  useQuery({ queryKey: ["reports", mine ? "mine" : "list"], queryFn: () => api<ReportPage>("/reports" + (mine ? "?mine=1" : "")) });

function refresh(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: ["reports"] });
  void qc.invalidateQueries({ queryKey: ["requests", "summary"] });
}

export function useCreateReport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateReport) => api<ProblemReport>("/reports", { method: "POST", body }),
    onSuccess: () => refresh(qc),
  });
}

export function useFixReport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reply }: { id: number; reply: string }) => api<ProblemReport>(`/reports/${id}/fix`, { method: "POST", body: { reply } }),
    onSuccess: () => refresh(qc),
  });
}

export function useReplaceReported() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, link_id }: { id: number; link_id: number }) => api<ReplaceResult>(`/reports/${id}/replace`, { method: "POST", body: { link_id } }),
    onSuccess: () => {
      refresh(qc);
      void qc.invalidateQueries({ queryKey: ["title"] });
    },
  });
}

/** "Alpha" or "Delta S01E02". */
export function reportSubject(r: { name: string; episode?: string }): string {
  return r.episode ? `${r.name} ${r.episode}` : r.name;
}

/** A problem event's toast for its reporter: only when it is marked fixed. */
export function problemToast(ev: ProblemEvent): { title: string; body: string; tone: "ok" } | null {
  if (ev.change !== "fixed") return null;
  return { title: reportSubject(ev), body: ev.reply ? `Your problem report was marked fixed: ${ev.reply}` : "Your problem report was marked fixed.", tone: "ok" };
}

/** The text before Replace file runs: what is removed and where it is missing until a new one is found. */
export function replaceScope(f: Pick<ReportFile, "libraries" | "episodes" | "pack">, report = true): { title: string; does: string[]; keeps: string[]; confirm: string } {
  const libs = joinAnd(f.libraries);
  const does = [`The file is removed now and a new one is searched. Until one is found it is missing in ${libs}.`];
  if (f.episodes && f.episodes.length > 0) does.push(`Episodes that lose the file: ${f.episodes.join(", ")}.`);
  does.push(
    f.pack
      ? `This file comes from a pack: the whole pack is excluded in ${libs}, not only this file.`
      : `Its release is excluded in ${libs}, so it is not downloaded again.`,
  );
  return {
    title: "Replace this file?",
    does,
    keeps: ["The debrid torrent and every other file stay as they are.", ...(report ? ["The report stays open until you mark it fixed."] : [])],
    confirm: "Replace file",
  };
}

function joinAnd(xs: string[]): string {
  if (xs.length < 2) return xs.join("");
  return `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;
}
