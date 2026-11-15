import type { Schemas } from "@/api/client";

// Draft settings also display individual gates before the server can compute will_grab.
export type VersionGrabState = Pick<Schemas["VersionGrabState"], "paused" | "pause_forced"> & Partial<Pick<Schemas["VersionGrabState"], "will_grab" | "library_writes">>;

/** A library's download verdict (docs/design/ui.md "Library state"): one word per state, the same on every page. */
export type Verdict = "Downloads on" | "Paused" | "Downloads off";
export type VerdictTone = "ok" | "info" | "neutral" | "warn" | "bad";

/** Whether the library grabs: the server's will_grab, else (drafts) Pause off. */
export function willGrab(state: VersionGrabState): boolean {
  return state.will_grab ?? (!state.paused && !state.pause_forced);
}

/** The verdict: will_grab decides on or not; the gates only pick the word. A library reads Paused while Pause is on;
 * a server hold with every gate open (or a library gone from the settings) reads as Downloads off. */
export function verdict(state: VersionGrabState): { word: Verdict; tone: VerdictTone } {
  if (willGrab(state)) return { word: "Downloads on", tone: "ok" };
  if (state.paused || state.pause_forced) return { word: "Paused", tone: state.pause_forced ? "bad" : "warn" };
  return { word: "Downloads off", tone: "neutral" };
}

/** "New files won't be linked": Pause off and the library's own write gate closed (no roots). Pause says it for
 * itself, so a paused library is not marked. */
export function linksOff(state: VersionGrabState): boolean {
  return !state.paused && !state.pause_forced && state.library_writes === false;
}

/** The library's state in plain words on Settings › Libraries: "Downloads on", "Paused" (the header's Pause), "Not
 * set up" (a draft the running settings do not have yet); a library the server holds, or one that links nothing,
 * says so. */
export function stateWords(state: VersionGrabState | undefined): { text: string; tone: VerdictTone } {
  if (!state) return { text: "Not set up", tone: "neutral" };
  const v = verdict(state);
  if (v.word === "Downloads off") return { text: "Downloads held by the server", tone: "neutral" };
  if (linksOff(state)) return { text: "New files won't be linked", tone: "warn" };
  return { text: v.word, tone: v.tone };
}

/** A closed gate and where it is changed; Pause has no settings page (it is at the top of every admin page), so its
 * gates carry no link. */
export type Gate = { text: string; label?: string; to?: string };

/** Every closed gate, each with where it is changed, so fixing one never hides another. key opens the library's
 * editor on Settings › Libraries. */
export function closedGates(state: VersionGrabState, key?: string): Gate[] {
  const library = { label: "Settings › Libraries", to: "/admin/settings/libraries" + (key ? `?edit=${encodeURIComponent(key)}` : "") };
  const gates: Gate[] = [];
  if (state.pause_forced) gates.push({ text: "Paused by the environment (THEATARR_FORCE_OFF)" });
  else if (state.paused) gates.push({ text: "Paused: turn Pause off at the top of the page" });
  if (state.will_grab === false && !gates.length) gates.push({ text: "Automatic downloads are paused by the server", ...library });
  if (linksOff(state)) gates.push({ text: "This library writes no links: new files won't be linked", ...library });
  return gates;
}

/** Where to fix what downloadReason says: Pause at the top of the page (no link) when paused, otherwise Libraries. */
export function downloadSettings(state?: VersionGrabState): { label: string; to?: string } {
  return state && (state.paused || state.pause_forced)
    ? { label: state.pause_forced ? "THEATARR_FORCE_OFF" : "Pause, at the top of the page" }
    : { label: "Settings › Libraries", to: "/admin/settings/libraries" };
}

const VERDICT_REASON: Record<Verdict, string> = {
  "Downloads on": "Downloads on",
  "Downloads off": "Downloads off",
  Paused: "Paused: nothing is searched, downloaded or linked",
};

/** Closed download gates read as the verdict; other search blocks retain the server's explanation. */
export function downloadReason(c: Schemas["VersionControl"]): string {
  const state = c.grab_state;
  const server = c.search_reason && !/router\.enabled|library\.enabled|^Paused:/.test(c.search_reason) ? c.search_reason : "";
  if (state?.will_grab === false) {
    const word = verdict(state).word;
    if (word === "Paused" && state.pause_forced) return "Paused by the environment (THEATARR_FORCE_OFF)";
    // Every displayed gate open: the server holds downloads for its own reason, kept beside the verdict.
    if (word === "Downloads off") return server ? `${word} (${server})` : word;
    return VERDICT_REASON[word];
  }
  // Older or incomplete responses must not expose settings keys in title notices.
  return server || "Downloads off";
}

/** Pause is on for every library (or forced by the environment): pages that list libraries say it once at the top.
 * Pause is global, so any one library's state carries it. */
export function globalDownloadsOff(states: readonly (VersionGrabState | undefined | null)[]): "locked" | "off" | undefined {
  const s = states.find((x) => x);
  if (!s) return undefined;
  return s.pause_forced ? "locked" : s.paused ? "off" : undefined;
}
