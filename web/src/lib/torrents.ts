// The Torrents page's logic (docs/design/torrents.md): filters, sorting, the delete selection and the wording of the
// delete and restore confirmations. The server decides; this only presents its preview.
import type { Schemas } from "@/api/client";
import type { Scope } from "@/lib/ops";
import { fmtBytes, fmtNumber, fmtTime } from "@/lib/format";
import { wordMatcher } from "@/lib/words";

export type Torrent = Schemas["Torrent"];
export type TorrentReport = Schemas["TorrentReport"];
export type DeletePlan = Schemas["TorrentDeletePlan"];
export type DeleteItem = Schemas["TorrentDeleteItem"];
export type DeleteSelection = Schemas["TorrentDeleteSelection"];
export type RestorePlan = Schemas["TorrentRestorePlan"];

export const GROUPS = [
  { id: "", label: "All" },
  { id: "unused", label: "Not used by Theatarr" },
  { id: "duplicate_hash", label: "Duplicate hash" },
  { id: "duplicate_title", label: "Duplicate title" },
  { id: "failed", label: "Failed" },
  { id: "stalled", label: "Stalled" },
  { id: "largest", label: "Largest" },
] as const;
export type GroupID = (typeof GROUPS)[number]["id"];

export type SortKey = "size" | "age" | "name";

const CACHE_CODES: Record<string, string> = { realdebrid: "rd", alldebrid: "ad", torbox: "tb" };

/** Each account's cache code (rd, ad, tb) by its kind, for the Provider tag; an account of another kind keeps its key. */
export function providerCodes(r: TorrentReport): Record<string, string> {
  return Object.fromEntries(r.providers.map((p) => [p.provider, CACHE_CODES[p.kind ?? ""] ?? p.provider]));
}

/** The key a torrent is selected by. */
export const keyOf = (t: { provider: string; id: string }) => `${t.provider}:${t.id}`;

/** The keys of the torrents in a group of the report. */
export function groupKeys(r: TorrentReport, g: GroupID): Set<string> | null {
  if (!g) return null;
  const out = new Set<string>();
  const members = (ms: { provider: string; id: string }[]) => ms.forEach((m) => out.add(keyOf(m)));
  switch (g) {
    case "unused":
      members(r.unused);
      break;
    case "failed":
      members(r.failed);
      break;
    case "stalled":
      members(r.stalled);
      break;
    case "largest":
      members(r.largest);
      break;
    case "duplicate_hash":
      r.duplicate_hash.forEach((x) => members(x.members));
      break;
    case "duplicate_title":
      r.duplicate_title.forEach((x) => members(x.members));
      break;
  }
  return out;
}

/** A parsed label's episode ranges written out again ("1–291" as 1-2-…-291, as the server labelled them before ranges;
 * a season's "S07E01–E04, E06" as S07E01 S07E02 S07E03 S07E04 S07E06), so a filter for one episode inside a pack
 * still finds it. Matching only; the page shows the ranges. */
export function spelledOut(parsed: string | undefined): string | undefined {
  const episodes = parsed?.replace(/S(\d+)(E\d+(?:–E\d+)?(?:, E\d+(?:–E\d+)?)*)/g, (_all, season: string, list: string) =>
    list.split(", ").map((part) => {
      const [from, to = from] = part.split("–").map((e) => Number(e.slice(1)));
      if (!(to >= from && to - from < 5000)) return part;
      return Array.from({ length: to - from + 1 }, (_, i) => `S${season}E${String(from + i).padStart(2, "0")}`).join(" ");
    }).join(" "));
  return episodes?.replace(/(\d+)–(\d+)/g, (all, a: string, b: string) => {
    const from = Number(a), to = Number(b);
    return to > from && to - from < 5000 ? Array.from({ length: to - from + 1 }, (_, i) => from + i).join("-") : all;
  });
}

/** Each torrent's expanded episode text (spelledOut), made once per torrent of a report, not on every filter edit. */
const spelled = new WeakMap<Torrent, string | undefined>();
const spelledOf = (t: Torrent) => {
  if (!spelled.has(t)) spelled.set(t, t.parsed && /\d–E?\d/.test(t.parsed) ? spelledOut(t.parsed) : undefined);
  return spelled.get(t);
};

/** The torrents of one provider ("" for every one) in a group, matching text (name, hash, the parsed title as shown,
 * or its ranges written out). */
export function filterTorrents(r: TorrentReport, provider: string, group: GroupID, text: string): Torrent[] {
  const keys = groupKeys(r, group);
  const q = text.trim().toLowerCase();
  const match = wordMatcher(text);
  const out: Torrent[] = [];
  for (const p of r.providers) {
    if (provider && p.provider !== provider) continue;
    for (const t of p.torrents) {
      if (keys && !keys.has(keyOf(t))) continue;
      if (q && !match(t.name, t.parsed, spelledOf(t)) && !t.hash.startsWith(q)) continue;
      out.push(t);
    }
  }
  return out;
}

/** Sorted copy: size and age largest and oldest first by default, name A to Z. */
export function sortTorrents(list: Torrent[], key: SortKey, desc = key !== "name"): Torrent[] {
  const time = (t: Torrent) => (t.added ? Date.parse(t.added) : 0);
  const cmp = (a: Torrent, b: Torrent): number => {
    if (key === "size") return a.bytes - b.bytes;
    if (key === "age") return time(b) - time(a); // older is "more"
    return a.name.localeCompare(b.name);
  };
  return [...list].sort((a, b) => (desc ? -cmp(a, b) : cmp(a, b)) || keyOf(a).localeCompare(keyOf(b)));
}

/** A reference kind in words. */
export const REF_LABEL: Record<string, string> = { link: "library link", download: "download", scan: "scanned link" };

/** "2 library links, 1 download": a torrent's references counted by kind. */
export function refSummary(refs?: Record<string, number>): string {
  if (!refs) return "";
  return Object.entries(refs)
    .filter(([, n]) => n > 0)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, n]) => `${n} ${REF_LABEL[k] ?? k}${n === 1 ? "" : "s"}`)
    .join(", ");
}

/** A failed copy that library links use, when the record holds another complete copy of its hash (served_by:
 *  their cache codes): "This copy failed. Another complete copy is on TB." ("on RD and TB" for two). Only what the
 *  record shows, not which copy the mount plays; "" without one. */
export function servedByLine(t: { served_by?: string[] }): string {
  const names = (t.served_by ?? []).map((c) => c.toUpperCase());
  if (names.length === 0) return "";
  const list = names.length === 1 ? names[0] : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
  return `This copy failed. Another complete copy is on ${list}.`;
}

/** What the usage badge's hover says: the references (or why it is unknown), after servedByLine when there is one. */
export function usageDetail(t: { usage?: string; refs?: Record<string, number>; ref_list?: { kind: string; detail: string }[]; reasons?: string[]; served_by?: string[] }): string {
  const served = servedByLine(t);
  if (t.usage === "used") {
    const lines = (t.ref_list ?? []).map((r) => `${REF_LABEL[r.kind] ?? r.kind}: ${r.detail}`);
    return [...(served ? [served] : []), refSummary(t.refs), ...lines].join("\n");
  }
  if (t.usage === "unknown") return (t.reasons ?? []).join("\n");
  return "No file of it is referenced";
}

export function usageTone(u?: string): "positive" | "warning" | "neutral" {
  return u === "used" ? "positive" : u === "unused" ? "warning" : "neutral";
}

/** The selection to send: the chosen torrents with the overrides the person ticked. */
export function deleteSelection(chosen: Torrent[], overrides: Record<string, string>, allowRecent: boolean): DeleteSelection[] {
  return chosen.map((t) => {
    const tok = overrides[keyOf(t)];
    return { provider: t.provider, id: t.id, hash: t.hash, ...(tok ? { override: true, override_token: tok } : {}), ...(allowRecent ? { allow_recent: true } : {}) };
  });
}

/** The delete confirmation: count, size, the backup it takes first, the warning, and what is refused. */
export function deleteScope(p: DeletePlan): Scope {
  const n = p.count;
  return {
    title: `Delete ${fmtNumber(n)} torrent${n === 1 ? "" : "s"} (${fmtBytes(p.bytes)})`,
    does: [
      "Take a full backup of every provider first; if it fails, nothing is deleted",
      `Delete ${fmtNumber(n)} torrent${n === 1 ? "" : "s"} from the provider, one by one, each written to the torrent journal`,
      p.warning,
    ],
    keeps: [
      p.refused > 0 ? `${fmtNumber(p.refused)} chosen torrent${p.refused === 1 ? " is" : "s are"} refused and kept (see below)` : "Every torrent you did not choose",
      "Library links: nothing in the library is touched",
    ],
    confirm: `Delete ${fmtNumber(n)}`,
    blocked: n === 0 ? "Nothing chosen can be deleted as it is: see why below." : undefined,
  };
}

/** The restore confirmation from its preview. */
export function restoreScope(p: RestorePlan): Scope {
  const c = p.counts ?? {};
  const skipped = (c.skip_invalid ?? 0) + (c.skip_target ?? 0);
  const resume = p.resume ?? 0;
  const n = p.readd + resume;
  return {
    title: `Restore ${fmtNumber(n)} torrent${n === 1 ? "" : "s"}`,
    does: [
      `Re-add ${fmtNumber(p.readd)} torrent${p.readd === 1 ? "" : "s"} (${fmtBytes(p.bytes)}) as a background job, paced by each provider's limits`,
      ...(resume > 0 ? [`Finish the file selection of ${fmtNumber(resume)} torrent${resume === 1 ? "" : "s"} an earlier restore added`] : []),
      "Select the backed-up files where the provider asks for a selection (Real-Debrid); a torrent whose backup knows no selection is left for you to select",
      "Report uncached, incomplete and failed re-adds in the torrent journal",
    ],
    keeps: [
      `${fmtNumber(c.present ?? 0)} already on the provider`,
      `${fmtNumber(skipped)} skipped (not a valid info hash, or the target provider is not available)`,
    ],
    confirm: `Restore ${fmtNumber(n)}`,
    blocked: n === 0 ? "Nothing to re-add." : undefined,
  };
}

/** An account whose last read failed, in plain words (docs/design/ui.md, Admin table, Torrents): one sentence naming
 * the account by its label (RD) and why, the next try in local time when the gateway gave one, then which record the
 * page still shows ("Showing the record as of …") when there is an earlier one; the raw error for the details toggle. */
export function providerProblem(p: { provider: string; error?: string; as_of?: string }, label: string, now: Date = new Date()): { message: string; raw: string } {
  const raw = p.error ?? "";
  let text = raw;
  // The provider client's own "rd: " prefix, which the sentence's label already says.
  while (text.startsWith(`${p.provider}: `)) text = text.slice(p.provider.length + 2);
  const retry = /\(retry at ([^)]+)\)/.exec(text)?.[1];
  const again = retry && !Number.isNaN(Date.parse(retry)) ? ` Next try ${fmtTime(retry, now)}.` : "";
  const failures = /(\d+) failures? in a row(?:, last: ([^(]+))?/.exec(text);
  let message: string;
  // The gateway's rate-limit back-off: a 429, or a 503 with Retry-After (a counted streak of 503s is a failure).
  const limited = /HTTP (429|503), Retry-After|HTTP (429)\b/.exec(text);
  if (text.startsWith("gateway: backing off") && failures) {
    message = `${label} is not asked for now: paused after ${failures[1]} failed requests in a row${failures[2] ? ` (last: ${failures[2].trim()})` : ""}.${again}`;
  } else if (text.startsWith("gateway: backing off") && limited) {
    message = `${label} is not asked for now: the provider asked to slow down (HTTP ${limited[1] ?? limited[2]}).${again}`;
  } else if (text.startsWith("gateway: backing off")) {
    message = `${label} is paused for now.${again}`;
  } else if (text.startsWith("gateway: budget exhausted")) {
    message = `${label} is not asked for now: its request limit is used up.${again}`;
  } else if (text.startsWith("gateway: egress is not confirmed through the VPN")) {
    message = `${label} is not asked: the VPN is not confirmed.`;
  } else {
    message = `Could not read ${label} just now: ${text.replace(/\s*\(retry at [^)]+\)/, "")}.${again}`;
  }
  if (p.as_of) message += ` Showing the record as of ${fmtTime(p.as_of, now)}.`;
  return { message, raw };
}

/** The reason of a torrent or hash whose usage was not worked out yet (internal/torrents record.go reasonNotWorkedOut). */
const NOT_WORKED_OUT = "its usage is worked out after the next provider sync";

/** A usage in words: "used", "unused", "not worked out" (no stored answer yet: Refresh works it out) or "unknown"
 *  (Theatarr lacks the data; the reasons say which). */
export function usageWord(u?: string, reasons?: readonly string[]): string {
  if (u === "unknown" && (reasons ?? []).some((r) => r.startsWith(NOT_WORKED_OUT))) return "not worked out";
  return u ?? "—";
}
