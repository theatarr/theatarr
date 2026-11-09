// The library state (docs/design/ui.md "Library state"), the same on every page: the mode marker (Shadow / Active), one
// verdict from the server's will_grab (lib/version-grab.ts), the separate "New files won't be linked" mark, and
// behind "Why?" every closed gate with where it is changed (links for admins).
import { useId, useState } from "react";
import { Popover } from "radix-ui";
import { Link } from "react-router";
import { Badge, Tag } from "@/components/ui/badge";
import { closedGates, linksOff, MODE_WORDS, verdict, type VersionGrabState } from "@/lib/version-grab";

export function VersionStatus({ state, showMode = false, settingsKey, canManage = false, why = true }: {
  state?: VersionGrabState;
  showMode?: boolean;
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
      {canManage ? <> · <Link className="text-[var(--accent-ink)] underline" to={g.to}>{g.label}</Link></> : null}
    </li>
  ));
  const badges = <>
    {showMode ? <Tag title={mode.help} data-mode={state.mode}>{mode.label}</Tag> : null}
    <Badge tone={v.tone} title={!why && gates.length ? gates.map((g) => g.text).join(". ") + "." : undefined}>{v.word}</Badge>
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

/** One notice per page when new downloads are off for every library (or locked off), so rows show only their own
 * verdict. off comes from lib/version-grab.ts globalDownloadsOff; "unknown" when the cause is not known yet. */
export function DownloadsOffNotice({ off, canManage = false, className }: { off?: "locked" | "off" | "unknown"; canManage?: boolean; className?: string }) {
  if (!off) return null;
  return (
    <p role="status" data-testid="downloads-off-notice" data-forced={off === "locked" || undefined}
      className={"rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)] " + (className ?? "")}>
      {off === "locked" ? "New downloads are locked off by the environment (THEATARR_FORCE_OFF) for every library." : off === "unknown" ? "New downloads are off for every library." : "New downloads are paused for every library: Allow new downloads is off."}
      {canManage ? <> <Link className="text-[var(--accent-ink)] underline" to="/admin/settings/downloads">Settings › Download clients</Link></> : null}
    </p>
  );
}
