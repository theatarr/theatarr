import { useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { toast } from "@/components/toaster";
import { Switch } from "@/components/ui/switch";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";
import { confirmationHeaders, useSettings } from "@/api/settings";
import { PAUSE_STOPS } from "@/lib/version-grab";
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
      // The lists' row actions read Pause from each row's libraries.
      void qc.invalidateQueries({ queryKey: qk.titles.all });
      // Pause acts at once, without a confirm: say what it did.
      toast(paused
        ? { tone: "warn", title: "Paused", body: `${PAUSE_STOPS[0].toUpperCase()}${PAUSE_STOPS.slice(1)}. Downloads already started finish at the provider.` }
        : { tone: "ok", title: "Pause is off", body: "Searches, downloads and library changes run again; downloads that finished meanwhile are imported now." });
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
  const id = useId();
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
      <span className={PILL} data-testid="pause" data-paused={paused || undefined}
        title={paused ? `Paused: ${PAUSE_STOPS}. Turn off to resume.` : `Pause: ${PAUSE_STOPS}. List syncs stop too. Downloads already started finish at the provider and are imported when you turn Pause off.`}>
        <Switch id={id} size="sm" aria-label="Pause" checked={paused} disabled={busy || !o.writable || o.source !== "db"}
          onCheckedChange={(on) => void save(on, o.generation)} />
        <label htmlFor={id} className={paused ? "cursor-pointer whitespace-nowrap font-medium text-[var(--warn-ink)]" : "cursor-pointer whitespace-nowrap"}>{paused ? "Paused" : "Pause"}</label>
      </span>
      {error ? <span role="alert" className="text-xs text-destructive">{error}</span> : null}
      {dialog}
    </>
  );
}
