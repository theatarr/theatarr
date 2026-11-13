// The library state (docs/design/ui.md "Library state"), the same on every page: the mode marker (Watch only / Live), one
// verdict from the server's will_grab (lib/version-grab.ts), the separate "New files won't be linked" mark, and
// behind "Why?" every closed gate with where it is changed (links for admins).
import { useId, useState } from "react";
import { Popover } from "radix-ui";
import { Link } from "react-router";
import { Badge, Tag } from "@/components/ui/badge";
import { closedGates, linksOff, MODE_WORDS, repeatsChoice, stateWords, verdict, type VersionGrabState } from "@/lib/version-grab";

export function VersionStatus({ state, showMode = false, words = false, beside, settingsKey, canManage = false, why = true }: {
  state?: VersionGrabState;
  showMode?: boolean;
  /** Settings › Libraries: one plain phrase ("Live, paused") in place of the mode tag and the verdict, beside the
   * library's Watch only / Live control. Why? stays. */
  words?: boolean;
  /** With words: the stored mode the Watch only / Live control beside them shows; words that only repeat it are left out. */
  beside?: string;
  settingsKey?: string;
  canManage?: boolean;
  /** false: dense rows (library tables, request chips). Why? is a small "?" button and the list opens in a popover
   * instead of pushing the row open; reachable by keyboard and touch, Escape closes it and returns focus. */
  why?: boolean;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  if (!state) return null;
  const v = verdict(state);
  const gates = closedGates(state, settingsKey);
  const mode = MODE_WORDS[state.mode] ?? { label: state.mode, help: "" };
  const items = gates.map((g) => (
    <li key={g.text} className="[overflow-wrap:anywhere]">
      {g.text}
      {canManage && g.to ? <> · <Link className="text-[var(--accent-ink)] underline" to={g.to}>{g.label}</Link></> : null}
    </li>
  ));
  // words: shown beside the Watch only / Live control, so only when they add to it (such as Live, paused).
  const phrase = words && !repeatsChoice(state, beside) ? stateWords(state) : undefined;
  const badges = words ? phrase ? <>
    <Badge tone={phrase.tone} data-testid="state-words" title={!why && gates.length ? gates.map((g) => g.text).join(". ") + "." : undefined}>{phrase.text}</Badge>
    {linksOff(state) && phrase.text !== "Live, but new files won't be linked" ? <Badge tone="ghost" data-testid="links-off">New files won't be linked</Badge> : null}
  </> : null : <>
    {showMode ? <Tag title={mode.help} data-mode={state.mode}>{mode.label}</Tag> : null}
    {/* A Watch-only library's verdict is its mode: the tag beside it says it once. */}
    {showMode && v.word === mode.label ? null : <Badge tone={v.tone} title={!why && gates.length ? gates.map((g) => g.text).join(". ") + "." : undefined}>{v.word}</Badge>}
    {linksOff(state) ? <Badge tone="ghost" data-testid="links-off">New files won't be linked</Badge> : null}
  </>;
  if (!why) {
    // Dense rows: a small "?" opens the list in a portalled popover (Radix: kept on screen, outside clipping rows;
    // Escape returns focus to the button).
    return (
      <span className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-1.5" data-slot="library-state" data-verdict={v.word}>
        {badges}
        {gates.length ? (
          <Popover.Root open={open} onOpenChange={setOpen}>
            <Popover.Trigger aria-label={`Why ${v.word}?`}
              className="inline-flex size-6 items-center justify-center rounded-full border border-[var(--line)] text-[11px] font-semibold text-[var(--accent-ink)] hover:bg-[var(--sunken)] focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--line-focus)]">
              ?
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content side="bottom" align="start" sideOffset={4} collisionPadding={8}
                className="z-50 w-64 max-w-[calc(100vw-16px)] rounded-[5px] border border-[var(--line)] bg-popover p-2 text-xs text-[var(--ink-2)] shadow-lg">
                <ul className="space-y-0.5" data-testid="state-why">{items}</ul>
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
        ) : null}
      </span>
    );
  }
  return (
    <span className="inline-flex min-w-0 max-w-full flex-col items-start gap-1" data-slot="library-state" data-verdict={v.word}>
      <span className="inline-flex flex-wrap items-center gap-1.5">
        {badges}
        {gates.length ? (
          <button type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}
            className="rounded-[3px] px-1 text-xs font-medium text-[var(--accent-ink)] underline-offset-2 hover:underline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--line-focus)]">
            Why?
          </button>
        ) : null}
      </span>
      {open && gates.length ? <ul id={id} className="space-y-0.5 border-l-2 border-[var(--line-2)] pl-2 text-xs text-[var(--ink-2)]" data-testid="state-why">{items}</ul> : null}
    </span>
  );
}

/** Pause in words, for a notice or a disabled control's tooltip. */
export const pausedWords = (off: "locked" | "off" | "unknown") =>
  off === "locked" ? "Paused by the environment (THEATARR_FORCE_OFF): nothing is searched, downloaded or linked for any library." : "Paused: nothing is searched, downloaded or linked for any library.";

/** One notice per page when Pause is on for every library (or forced by the environment), so rows show only their
 * own verdict. off comes from lib/version-grab.ts globalDownloadsOff; "unknown" when the cause is not known yet. */
export function DownloadsOffNotice({ off, canManage = false, className }: { off?: "locked" | "off" | "unknown"; canManage?: boolean; className?: string }) {
  if (!off) return null;
  return (
    <p role="status" data-testid="downloads-off-notice" data-forced={off === "locked" || undefined}
      className={"rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)] " + (className ?? "")}>
      {pausedWords(off)}
      {canManage && off !== "locked" ? " Turn Pause off at the top of the page to resume." : null}
    </p>
  );
}
