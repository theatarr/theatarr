import type { Schemas } from "@/api/client";

// Draft settings also display individual gates before the server can compute will_grab.
export type VersionGrabState = Pick<Schemas["VersionGrabState"], "mode" | "grab" | "router_enabled" | "router_forced_off"> & Partial<Pick<Schemas["VersionGrabState"], "will_grab" | "library_enabled" | "library_writes">>;

/** A library's download verdict (docs/design/ui.md "Library state"): one word per state, the same on every page. */
export type Verdict = "Downloads on" | "Preview only" | "Downloads off" | "Paused globally" | "Locked off";
export type VerdictTone = "ok" | "info" | "neutral" | "warn" | "bad";

/** Whether the library grabs: the server's will_grab, else (drafts) every download gate open. */
export function willGrab(state: VersionGrabState): boolean {
  return state.will_grab ?? (state.mode === "active" && state.grab && state.router_enabled && !state.router_forced_off);
}

/** The verdict: will_grab decides on or not; the gates only pick the word. A shadow library previews whatever the
 * switches say; for an active one the environment override wins, then the global switch, then its own (the order
 * downloadSettings fixes them in). A library not in the policy (mode off) and a server hold with every gate open read
 * as Downloads off. */
export function verdict(state: VersionGrabState): { word: Verdict; tone: VerdictTone } {
  if (willGrab(state)) return { word: "Downloads on", tone: "ok" };
  if (state.mode === "shadow") return { word: "Preview only", tone: "info" };
  if (state.mode === "active" && state.router_forced_off) return { word: "Locked off", tone: "bad" };
  if (state.mode === "active" && !state.router_enabled) return { word: "Paused globally", tone: "warn" };
  return { word: "Downloads off", tone: "neutral" };
}

/** Whether the library writes links: the server's per-library write gate, else (drafts) the switch on and mode active. */
function libraryWrites(state: VersionGrabState): boolean {
  return state.library_enabled !== false && (state.library_writes ?? state.mode === "active");
}

/** "New files won't be linked": an active library whose links are off. Separate from the download verdict; a shadow
 * library writes no links by design, so it is not marked. */
export function linksOff(state: VersionGrabState): boolean {
  return state.mode === "active" && !libraryWrites(state);
}

export const MODE_WORDS: Record<string, { label: string; help: string }> = {
  active: { label: "Active", help: "Active manages files: it downloads and links what the profile wants." },
  shadow: { label: "Shadow", help: "Shadow previews and compares: it records what it would download, nothing more." },
  off: { label: "Not set up", help: "This library is not in the saved settings yet." },
};

export type Gate = { text: string; label: string; to: string };

/** Every closed gate, each with where it is changed, so fixing one never hides another. key opens the library's
 * editor on Settings › Libraries. */
export function closedGates(state: VersionGrabState, key?: string): Gate[] {
  const library = { label: "Settings › Libraries", to: "/admin/settings/libraries" + (key ? `?edit=${encodeURIComponent(key)}` : "") };
  const downloads = { label: "Settings › Download clients", to: "/admin/settings/downloads" };
  const gates: Gate[] = [];
  if (state.mode === "off") gates.push({ text: "This library is not in the saved settings", ...library });
  if (state.mode === "shadow") gates.push({ text: "Mode is Shadow: it previews and compares, it downloads nothing", ...library });
  if (!state.grab) gates.push({ text: "Allow downloads for this library is off", ...library });
  if (state.router_forced_off) gates.push({ text: "New downloads are locked off by the environment (THEATARR_FORCE_OFF)", ...downloads });
  else if (!state.router_enabled) gates.push({ text: "Allow new downloads is off for every library", ...downloads });
  if (state.will_grab === false && !gates.length) gates.push({ text: "Automatic downloads are paused by the server", ...downloads });
  if (state.library_enabled === false) gates.push({ text: "Write library links is off: new files won't be linked", label: "Settings › Libraries", to: "/admin/settings/libraries" });
  else if (linksOff(state)) gates.push({ text: "This library writes no links: new files won't be linked", ...library });
  return gates;
}

/** Where to fix what downloadReason says, in the current Settings navigation: Download clients when the verdict is
 * Locked off or Paused globally (an active library), otherwise Libraries (a shadow library reads Preview only even
 * under force-off; a library gone from the policy, mode off, is fixed there too). */
export function downloadSettings(state?: VersionGrabState): { label: string; to: string } {
  return state && state.mode === "active" && (state.router_forced_off || !state.router_enabled)
    ? { label: "Settings › Download clients", to: "/admin/settings/downloads" }
    : { label: "Settings › Libraries", to: "/admin/settings/libraries" };
}

const VERDICT_REASON: Record<Verdict, string> = {
  "Downloads on": "Downloads on",
  "Preview only": "Preview only (this library is in Shadow: it records what it would download)",
  "Downloads off": "Downloads off (Allow downloads for this library is off)",
  "Paused globally": "Paused globally (Allow new downloads is off)",
  "Locked off": "Locked off (new downloads are forced off by the environment)",
};

/** Closed download gates read as the verdict; other search blocks retain the server's explanation. */
export function downloadReason(c: Schemas["VersionControl"]): string {
  const state = c.grab_state;
  // Mode off: the library is no longer in the policy; the server's reason says so before any switch matters.
  const server = c.search_reason && !/router\.enabled|library\.enabled/.test(c.search_reason) ? c.search_reason : "";
  if (state?.will_grab === false && state.mode !== "off") {
    const word = verdict(state).word;
    // Every displayed gate open: the server holds downloads for its own reason, kept beside the verdict.
    if (word === "Downloads off" && state.grab) return server ? `${word} (${server})` : word;
    return VERDICT_REASON[word];
  }
  // Older or incomplete responses must not expose settings keys in title notices.
  return server || "Downloads off";
}

/** New downloads are off for every library (the global switch, or locked off by the environment): pages that list
 * libraries say it once at the top. The router fields are global, so any one library's state carries them. */
export function globalDownloadsOff(states: readonly (VersionGrabState | undefined | null)[]): "locked" | "off" | undefined {
  const s = states.find((x) => x);
  if (!s) return undefined;
  return s.router_forced_off ? "locked" : !s.router_enabled ? "off" : undefined;
}
