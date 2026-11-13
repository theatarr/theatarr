import { usePerms } from "@/api/library";
// Small pieces of the season, episode and version controls (docs/design/manage.md): the monitor switch, the episode's
// upgrade block and the "why not" note. The switch is the shared Switch primitive.
import { downloadReason, downloadSettings, verdict } from "@/lib/version-grab";
import { Lock, Search } from "lucide-react";
import { Link } from "react-router";
import type { UpgradeInfo, VersionControl } from "@/api/manage";
import { UpgradeStatus } from "@/components/title/upgrade-why";
import { availableIn, nextLine, upgradeBadge } from "@/lib/upgrade";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";

/** A monitor switch. Disabled with a reason, it shows a lock and the reason as its title. */
export function MonitorSwitch({
  on,
  onChange,
  disabled,
  reason,
  label,
  testid,
  size = "md",
}: {
  on: boolean;
  onChange?: (v: boolean) => void;
  disabled?: boolean;
  reason?: string;
  label: string;
  testid?: string;
  size?: "sm" | "md";
}) {
  const sm = size === "sm";
  return (
    <span className="inline-flex shrink-0 items-center gap-1">
      <Switch
        checked={on}
        size={sm ? "sm" : "default"}
        aria-label={label}
        aria-disabled={disabled || undefined}
        title={reason || label}
        data-testid={testid}
        onClick={(e) => {
          e.stopPropagation();
          if (!disabled) onChange?.(!on);
        }}
      />
      {disabled ? <Lock className={cn("text-[var(--ink-3)]", sm ? "size-2.5" : "size-3")} aria-hidden /> : null}
    </span>
  );
}

/** The note under read-only or search-less controls. globalSaid: the page's notice already says Pause is on for every
 * library, so the note does not repeat it. */
export function ControlNote({ c, kind, globalSaid = false, verdictSaid = false }: { c: VersionControl | undefined | null; kind: "edit" | "search" | "both"; globalSaid?: boolean; verdictSaid?: boolean }) {
  const { admin } = usePerms();
  if (!c) return null;
  const lines: { text: string; search?: boolean }[] = [];
  if ((kind === "edit" || kind === "both") && !c.can_edit && c.edit_reason) lines.push({ text: c.edit_reason });
  const g = c.grab_state;
  const global = globalSaid && !!g && g.mode === "active" && verdict(g).word === "Paused";
  // verdictSaid: the library-state chip beside this note already names the verdict (its popover has the gates), so
  // the note drops a line that would only repeat it; a server's own hold reason and a library gone from the policy stay.
  const repeat = verdictSaid && !!g && g.will_grab === false && g.mode !== "off" && verdict(g).word !== "Downloads off";
  if ((kind === "search" || kind === "both") && (!c.can_search || g?.will_grab === false) && !global && !repeat) lines.push({ text: downloadReason(c), search: true });
  if (lines.length === 0) return null;
  const fix = downloadSettings(c.grab_state);
  return (
    <div className="space-y-0.5 text-[11.5px] text-[var(--ink-3)]" data-testid="control-note">
      {lines.map((l, i) => (
        <p key={i} className="flex items-start gap-1.5">
          <Search className="mt-0.5 size-3 shrink-0" />
          <span>{l.text}{l.search ? <> — {admin && fix.to ? <Link className="underline" to={fix.to}>{fix.label}</Link> : fix.label}</> : null}</span>
        </p>
      ))}
    </div>
  );
}

/**
 * The upgrade explanation for an episode: its status in the one vocabulary with Why? (the cutoff line, scores and the
 * source ceiling are in there), what Theatarr would take next (or that nothing better was seen), and a link to the
 * releases. People without "see files" get plain words: the API gives them no states, scores or releases.
 */
export function UpgradeBlock({
  u,
  quality,
  seeFiles,
  releases,
  titleId,
  what,
}: {
  u: UpgradeInfo | undefined | null;
  quality?: string;
  seeFiles: boolean;
  /** Where "Show releases" goes: a path, or a click handler. */
  releases?: string | (() => void);
  titleId: number;
  /** What the status is about, for the Why? heading. */
  what: string;
}) {
  const badge = upgradeBadge(u);
  if (!u || !badge) return null;
  const q = quality ?? u.quality;
  if (!seeFiles) return <p className="text-sm">{availableIn(q)}</p>;
  const below = u.state === "below_cutoff" || u.state === "upgrade_available";
  const next = nextLine(u);
  return (
    <div
      className={cn(
        "rounded-[5px] border px-3 py-2.5 text-sm",
        u.state === "upgrade_available" ? "border-[var(--th-info-border)] bg-[var(--accent-soft)]" : "border-[var(--line)] bg-[var(--surface)]",
      )}
      data-testid="upgrade-block"
      data-state={u.state}
    >
      <UpgradeStatus u={u} what={what} titleId={titleId} ceilingControl={false} />
      {below && next ? (
        <p className={cn("mt-1 text-xs [overflow-wrap:anywhere]", u.state === "upgrade_available" ? "text-[var(--accent-ink)]" : "text-[var(--ink-3)]")} data-testid="upgrade-next">
          {next}
          {releases ? (
            <>
              {" "}
              {typeof releases === "string" ? (
                <Link to={releases} className="whitespace-nowrap font-medium text-primary hover:underline" data-testid="show-releases-link">
                  Show releases
                </Link>
              ) : (
                <button type="button" onClick={releases} className="whitespace-nowrap font-medium text-primary hover:underline" data-testid="show-releases-link">
                  Show releases
                </button>
              )}
            </>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}
