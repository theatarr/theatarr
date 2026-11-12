import type { Schemas } from "@/api/client";

// Draft settings also display individual gates before the server can compute will_grab.
export type VersionGrabState = Pick<Schemas["VersionGrabState"], "mode" | "paused" | "pause_forced"> & Partial<Pick<Schemas["VersionGrabState"], "will_grab" | "library_writes">>;

/** A library's download verdict (docs/design/ui.md "Library state"): one word per state, the same on every page. */
export type Verdict = "Downloads on" | "Watch only" | "Paused" | "Downloads off";
export type VerdictTone = "ok" | "info" | "neutral" | "warn" | "bad";

/** Whether the library grabs: the server's will_grab, else (drafts) Live and Pause off. */
export function willGrab(state: VersionGrabState): boolean {
  return state.will_grab ?? (state.mode === "active" && !state.paused && !state.pause_forced);
}

/** The verdict: will_grab decides on or not; the gates only pick the word. A Watch-only library reads Watch only
 * whatever Pause says; a Live one reads Paused while Pause is on. A library not in the policy (mode off) and a server
 * hold with every gate open read as Downloads off. */
export function verdict(state: VersionGrabState): { word: Verdict; tone: VerdictTone } {
  if (willGrab(state)) return { word: "Downloads on", tone: "ok" };
  if (state.mode === "shadow") return { word: "Watch only", tone: "info" };
  if (state.mode === "active" && (state.paused || state.pause_forced)) return { word: "Paused", tone: state.pause_forced ? "bad" : "warn" };
  return { word: "Downloads off", tone: "neutral" };
}

/** "New files won't be linked": a Live library, Pause off, whose own write gate is closed (no roots). A Watch-only
 * library writes no links by design and Pause says it for itself, so neither is marked. */
export function linksOff(state: VersionGrabState): boolean {
  return state.mode === "active" && !state.paused && !state.pause_forced && state.library_writes === false;
}

export const WATCH_ONLY = "Watch only: Theatarr searches, downloads and links nothing for this library.";

export const MODE_WORDS: Record<string, { label: string; help: string }> = {
  active: { label: "Live", help: "Live searches, downloads and links what the profile wants." },
  shadow: { label: "Watch only", help: WATCH_ONLY },
  off: { label: "Not set up", help: "This library is not in the saved settings yet." },
};

/** The per-library control on Settings › Libraries: Watch only is mode shadow, Live is mode active. */
export type LibraryChoice = "watch" | "live";
export const CHOICE_LABEL: Record<LibraryChoice, string> = { watch: "Watch only", live: "Live" };
export const CHOICE_HELP = "Watch only: Theatarr searches, downloads and links nothing for this library. Live searches, downloads and links.";

/** The selector's value for a stored mode; undefined for a mode it cannot express. */
export function choiceOf(mode: string | undefined): LibraryChoice | undefined {
  return mode === "shadow" ? "watch" : mode === "active" ? "live" : undefined;
}

/** A choice applied to a library's settings: only mode changes. */
export function withChoice<C extends { mode: string }>(config: C, choice: LibraryChoice): C {
  const mode = choice === "live" ? "active" : "shadow";
  return config.mode === mode ? config : { ...config, mode };
}

/** The library's state in plain words beside its control: "Watch only", "Live", "Live, paused" (the header's Pause),
 * "Not set up"; a Live library the server holds, or one that links nothing, says so. */
export function stateWords(state: VersionGrabState): { text: string; tone: VerdictTone } {
  if (state.mode === "shadow") return { text: "Watch only", tone: "info" };
  if (state.mode !== "active") return { text: MODE_WORDS.off.label, tone: "neutral" };
  const v = verdict(state);
  if (v.word === "Paused") return { text: "Live, paused", tone: v.tone };
  if (v.word === "Downloads off") return { text: "Live, downloads held by the server", tone: "neutral" };
  if (linksOff(state)) return { text: "Live, but new files won't be linked", tone: "warn" };
  return { text: "Live", tone: "ok" };
}

/** The running state's words only repeat the Watch only / Live control beside them, which shows the stored mode. */
export const repeatsChoice = (state: VersionGrabState, mode: string | undefined) => {
  const choice = choiceOf(mode);
  return choice !== undefined && stateWords(state).text === CHOICE_LABEL[choice];
};

/** A closed gate and where it is changed; Pause has no settings page (it is at the top of every admin page), so its
 * gates carry no link. */
export type Gate = { text: string; label?: string; to?: string };

/** Every closed gate, each with where it is changed, so fixing one never hides another. key opens the library's
 * editor on Settings › Libraries. */
export function closedGates(state: VersionGrabState, key?: string): Gate[] {
  const library = { label: "Settings › Libraries", to: "/admin/settings/libraries" + (key ? `?edit=${encodeURIComponent(key)}` : "") };
  const gates: Gate[] = [];
  if (state.mode === "off") gates.push({ text: "This library is not in the saved settings", ...library });
  if (state.mode === "shadow") gates.push({ text: WATCH_ONLY.slice(0, -1), ...library });
  if (state.pause_forced) gates.push({ text: "Paused by the environment (THEATARR_FORCE_OFF)" });
  else if (state.paused) gates.push({ text: "Paused: turn Pause off at the top of the page" });
  if (state.will_grab === false && !gates.length) gates.push({ text: "Automatic downloads are paused by the server", ...library });
  if (linksOff(state)) gates.push({ text: "This library writes no links: new files won't be linked", ...library });
  return gates;
}

/** Where to fix what downloadReason says: Pause at the top of the page (no link) when a Live library is paused,
 * otherwise Libraries (a Watch-only library reads Watch only even while paused; a library gone from the policy, mode
 * off, is fixed there too). */
export function downloadSettings(state?: VersionGrabState): { label: string; to?: string } {
  return state && state.mode === "active" && (state.paused || state.pause_forced)
    ? { label: state.pause_forced ? "THEATARR_FORCE_OFF" : "Pause, at the top of the page" }
    : { label: "Settings › Libraries", to: "/admin/settings/libraries" };
}

const VERDICT_REASON: Record<Verdict, string> = {
  "Downloads on": "Downloads on",
  "Watch only": WATCH_ONLY.slice(0, -1),
  "Downloads off": "Downloads off",
  Paused: "Paused: nothing is searched, downloaded or linked",
};

/** Closed download gates read as the verdict; other search blocks retain the server's explanation. */
export function downloadReason(c: Schemas["VersionControl"]): string {
  const state = c.grab_state;
  // Mode off: the library is no longer in the policy; the server's reason says so before any switch matters.
  const server = c.search_reason && !/router\.enabled|library\.enabled|^Paused:|^Watch only:/.test(c.search_reason) ? c.search_reason : "";
  if (state?.will_grab === false && state.mode !== "off") {
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
