import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";
import { confirmationHeaders, useSettings } from "@/api/settings";
import { useWriteConfirmation } from "./confirm-write";

// Pause stops searches, new downloads, list syncs and library changes for every library; RSS skips its runs while paused.
// Notifications are the notify section's own "Send notifications" setting.

/** One PUT /settings/switches with paused; a refusal that needs confirmation (Pause off) opens the shared dialog
 * listing every effect, and confirming sends the same body again with its token. */
function useSwitchSave() {
  const qc = useQueryClient();
  const confirm = useWriteConfirmation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const save = async (paused: boolean, generation: number, token?: string) => {
    setBusy(true);
    setError(undefined);
    try {
      await api("/settings/switches", { method: "PUT", body: { generation, paused }, headers: confirmationHeaders(token) });
      void qc.invalidateQueries({ queryKey: qk.settings.all });
      void qc.invalidateQueries({ queryKey: qk.policy.all });
      // Wanted and the status read Pause: their due rows and search buttons follow the save.
      void qc.invalidateQueries({ queryKey: qk.status });
      void qc.invalidateQueries({ queryKey: ["wanted"] });
      // An open title page's Search now and library rows read Pause from the add options and the title.
      void qc.invalidateQueries({ queryKey: qk.addOptions.all });
      void qc.invalidateQueries({ queryKey: ["title"] });
    } catch (e) {
      if (!confirm.ask(e, (next) => void save(paused, generation, next))) setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return { save, busy, error, dialog: confirm.dialog };
}

const PILL = "flex h-8 shrink-0 items-center gap-1.5 rounded-[5px] px-2 text-[12.5px] text-[var(--ink-2)]";

/** The admin header's Pause. Forced by THEATARR_FORCE_OFF it is a locked pill; read-only settings disable it. */
export function PauseSwitch() {
  const o = useSettings().data;
  const { save, busy, error, dialog } = useSwitchSave();
  if (!o) return null;
  if (o.switches.pause_forced) {
    return (
      <span className={PILL} data-testid="pause" data-paused data-forced aria-disabled="true" title="Paused by THEATARR_FORCE_OFF. Remove it and restart to resume.">
        <span aria-hidden className="size-2 shrink-0 rounded-full border-[1.5px] border-[var(--bad)]" />
        <span className="whitespace-nowrap font-medium text-[var(--warn-ink)]">Paused (locked)</span>
      </span>
    );
  }
  const paused = o.switches.paused;
  return (
    <>
      <label className={PILL} data-testid="pause" data-paused={paused || undefined}
        title={paused ? "Paused: nothing is searched, downloaded or linked. Turn off to resume." : "Pause stops searches, new downloads, list syncs and changes in your library folders, for every library. Downloads already started finish at the provider and are imported when you turn Pause off."}>
        <input type="checkbox" role="switch" aria-label="Pause" checked={paused} disabled={busy || !o.writable || o.source !== "db"}
          onChange={(e) => void save(e.target.checked, o.generation)} />
        <span className={paused ? "whitespace-nowrap font-medium text-[var(--warn-ink)]" : "whitespace-nowrap"}>{paused ? "Paused" : "Pause"}</span>
      </label>
      {error ? <span role="alert" className="text-xs text-destructive">{error}</span> : null}
      {dialog}
    </>
  );
}
