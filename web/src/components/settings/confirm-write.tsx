// The write-enabling confirmation every settings editor shares (docs/design/settings.md): a save the server refuses with
// a 422 listing its effects (a provider an active version can now use, a widened profile, a library going Live, a reload
// after a startup fallback) opens this dialog; confirming sends the same request again with the token the 422
// carried. Cancelling leaves the draft unsaved. EffectsConfirmDialog is the risky-change confirmation: the
// effects exactly as the server returns them, grouped by library, the libraries affected (other libraries named
// apart when the change is about one), Cancel focused.
import { useQueryClient } from "@tanstack/react-query";
import { qk } from "@/api/query-keys";
import type { SettingsOverview } from "@/api/settings";
import { useState, type ReactNode } from "react";
import { Check } from "lucide-react";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import type { Scope } from "@/lib/ops";
import { confirmationOf, type WriteConfirmation } from "@/lib/settings";

type Pending = { fallback?: { active: number; stored: number }; confirmation: WriteConfirmation; retry: (token: string) => void };

/** ask(error, retry) opens the dialog when error is a confirmation refusal and returns true; dialog renders it. */
export function useWriteConfirmation(): { ask: (error: unknown, retry: (token: string) => void) => boolean; dialog: ReactNode } {
  const qc = useQueryClient();
  const [pending, setPending] = useState<Pending>();
  const ask = (error: unknown, retry: (token: string) => void) => {
    const confirmation = confirmationOf(error);
    if (!confirmation) return false;
    const o = qc.getQueryData<SettingsOverview>(qk.settings.overview);
    const fallback = o?.active_generation && o.active_generation !== o.generation ? { active: o.active_generation, stored: o.generation } : undefined;
    setPending({ confirmation, retry, fallback });
    return true;
  };
  const dialog = pending ? (
    <EffectsConfirmDialog
      testid="settings-confirm"
      effects={pending.confirmation.effects}
      scope={{
        title: "Confirm what this change enables",
        does: [],
        keeps: ["Environment force-off overrides stay in force.", "Existing links, mount files and trash entries stay where they are."],
        confirm: "Confirm and save",
      }}
      onClose={() => setPending(undefined)}
      onConfirm={() => {
        setPending(undefined);
        pending.retry(pending.confirmation.token);
      }}
    >
      {pending.fallback ? <p>After a startup fallback, Theatarr is running generation {pending.fallback.active} while generation {pending.fallback.stored} is stored. This change also applies stored settings that are not running yet; they can be the reason confirmation is needed.</p> : null}
    </EffectsConfirmDialog>
  ) : null;
  return { ask, dialog };
}

type Effect = { version?: string; effect: string };

/** The server's effects grouped by library in the order it sent them; effects without a library come first, under
 * the neutral "This change" (an address or metadata change names no library). */
export function groupEffects(effects: readonly Effect[]): { version: string; effects: string[] }[] {
  const groups: { version: string; effects: string[] }[] = [];
  for (const e of effects) {
    const g = groups.find((x) => x.version === (e.version ?? ""));
    if (g) g.effects.push(e.effect);
    else groups.push({ version: e.version ?? "", effects: [e.effect] });
  }
  return groups.sort((a, b) => Number(!!a.version) - Number(!!b.version));
}

export function EffectsConfirmDialog({ effects, library, scope, ...rest }: {
  effects: readonly Effect[];
  /** The library being edited, when the change is about one: other libraries are then named apart. */
  library?: string;
  scope: Scope;
  onConfirm: () => void;
  onClose: () => void;
  busy?: boolean;
  testid?: string;
  subject?: ReactNode;
  children?: ReactNode;
}) {
  const groups = groupEffects(effects);
  const named = groups.map((g) => g.version).filter(Boolean);
  const others = library ? named.filter((v) => v !== library) : [];
  const does = (
    <div className="space-y-3" data-testid="confirm-does">
      {named.length ? (
        <p className="text-[var(--ink-2)]" data-testid="confirm-libraries">
          Libraries affected: <span className="font-mono text-xs">{named.join(", ")}</span>
        </p>
      ) : null}
      {others.length ? (
        <p className="rounded-[5px] border-l-[3px] border-[var(--warn)] bg-[var(--sunken)] px-3 py-2 text-[var(--ink)]" data-testid="confirm-others">
          Also affects other libraries: <span className="font-mono text-xs">{others.join(", ")}</span>
        </p>
      ) : null}
      {groups.map((g) => (
        <section key={g.version} data-testid="confirm-group" data-version={g.version || undefined}>
          <h3 className="mb-1 text-xs font-semibold text-[var(--ink-3)]">
            {g.version ? <span className="font-mono">{g.version}</span> : "This change"}
            {g.version && g.version === library ? " (this library)" : null}
          </h3>
          <ul className="space-y-1.5">
            {g.effects.map((l, i) => (
              <li key={i} className="flex gap-2 text-[var(--ink)] [overflow-wrap:anywhere]">
                <Check className="mt-0.5 size-4 shrink-0 text-[var(--accent-ink)]" aria-hidden />
                {l}
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
  return <ConfirmDialog scope={scope} does={does} {...rest} />;
}
