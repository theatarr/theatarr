import { useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useLocation } from "react-router";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";
import { confirmationHeaders, useSettings, type SettingsOverview } from "@/api/settings";
import { Card } from "@/components/ui/card";
import { useWriteConfirmation } from "./confirm-write";

const switches = [
  { key: "router", field: "router_enabled", label: "Allow new downloads", help: "Off: Theatarr adds no torrents to your debrid accounts. RSS checks and comparison searches still read indexers. Each library is also Live or Watch only on its own." },
  { key: "library", field: "library_enabled", label: "Write library links", help: "Off: Theatarr does not create or change links in your library folders." },
  { key: "notify", field: "notify_enabled", label: "Send notifications", help: "Allow messages to configured targets and installed notification clients." },
] as const;

type SwitchField = (typeof switches)[number]["field"];

export function GlobalSwitchesCard({ overview }: { overview: SettingsOverview }) {
 return <>{switches.map(s => <SwitchCard key={s.field} overview={overview} field={s.field} />)}</>;
}
/** One PUT /settings/switches with the switches in patch; a refusal that needs confirmation opens the shared dialog
 * listing every effect, and confirming sends the same body again with its token. */
function useSwitchSave() {
  const qc = useQueryClient();
  const confirm = useWriteConfirmation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const save = async (patch: Partial<Record<SwitchField, boolean>>, generation: number, token?: string) => {
    setBusy(true);
    setError(undefined);
    try {
      await api("/settings/switches", { method: "PUT", body: { generation, ...patch }, headers: confirmationHeaders(token) });
      void qc.invalidateQueries({ queryKey: qk.settings.all });
      void qc.invalidateQueries({ queryKey: qk.policy.all });
      // Wanted and the status read the switches: their due rows and search buttons follow the save.
      void qc.invalidateQueries({ queryKey: qk.status });
      void qc.invalidateQueries({ queryKey: ["wanted"] });
    } catch (e) {
      if (!confirm.ask(e, (next) => void save(patch, generation, next))) setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return { save, busy, error, dialog: confirm.dialog };
}

export function SwitchCard({ overview, field: selected }: { overview: SettingsOverview; field: SwitchField }) {
  const { save: put, busy, error, dialog } = useSwitchSave();
  const save = (field: SwitchField, enabled: boolean, generation: number) => put({ [field]: enabled }, generation);
  return (
    <Card data-testid="global-switches">
      <div className="mt-4 space-y-4">
        {switches.filter(s => s.field === selected).map(({ key, field, label, help }) => {
          const source = overview.switches.sources?.[key] ?? overview.source;
          const forced = source === "forced off by environment";
          return (
            <div key={key}>
              <label className="flex items-center gap-2 font-medium">
                <input type="checkbox" role="switch" checked={overview.switches[field]} disabled={busy || !overview.writable || forced}
                  onChange={(e) => void save(field, e.target.checked, overview.generation)} />
                {label}
              </label>
              <p className="mt-1 text-sm text-muted-foreground">{help}</p>
              {forced ? <p className="text-xs text-muted-foreground">Forced off by environment</p> : null}
            </div>
          );
        })}
      </div>
      {error ? <p role="alert" className="mt-4 text-sm">{error}</p> : null}
      {dialog}
    </Card>
  );
}

const forcedOff = (o: SettingsOverview, key: "router" | "library") => o.switches.sources?.[key] === "forced off by environment";

/** Where each paused-together switch lives. */
const SWITCH_PAGE = { router_enabled: "/admin/settings/downloads", library_enabled: "/admin/settings/libraries" } as const;

/** The Pause state of the two switches it controls: on (both off), off (both on), custom (they differ), or
 * unavailable (settings read-only, or a switch forced off by the environment: the header's DownloadsSignal says so). */
export function pauseState(o: SettingsOverview): "paused" | "running" | "custom" | "unavailable" {
  if (o.source !== "db" || !o.writable || forcedOff(o, "router") || forcedOff(o, "library")) return "unavailable";
  const { router_enabled: router, library_enabled: library } = o.switches;
  return router === library ? (router ? "running" : "paused") : "custom";
}

/** The admin header's Pause: one switch for Allow new downloads and Write library links together. On turns both off,
 * off turns both on in one save, with the same confirmation the two switches ask for. When the two differ it shows
 * Custom and links to the one that is off; it never toggles them then. fallback (the header's downloads signal)
 * renders while Pause cannot be used, and beside Custom when new downloads are the switch that is off. */
export function PauseSwitch({ fallback = null }: { fallback?: ReactNode }) {
  const o = useSettings().data;
  const { save, busy, error, dialog } = useSwitchSave();
  if (!o) return <>{fallback}</>;
  const state = pauseState(o);
  if (state === "unavailable") return <>{fallback}</>;
  const pill = "flex h-8 shrink-0 items-center gap-1.5 rounded-[5px] px-2 text-[12.5px] text-[var(--ink-2)]";
  if (state === "custom") {
    const off = o.switches.router_enabled ? "library_enabled" : "router_enabled";
    // New downloads off keeps its own signal (fallback) beside Custom, as before Pause.
    return (
      <>
      {off === "router_enabled" ? fallback : null}
      <Link to={SWITCH_PAGE[off] + "#switches"} data-testid="pause-custom" className={pill + " transition-colors hover:bg-[var(--hover)] hover:text-[var(--ink)]"}
        title={off === "router_enabled" ? "Allow new downloads is off and Write library links is on." : "Write library links is off and Allow new downloads is on."}>
        Pause: Custom
      </Link>
      </>
    );
  }
  const paused = state === "paused";
  return (
    <>
      <label className={pill} data-testid="pause" data-paused={paused || undefined}
        title={paused ? "Paused: no new downloads and no new library links anywhere. Queued searches wait and start when you turn this off." : "Pause stops new downloads and new library links for every library. Queued searches wait and start when you resume."}>
        <input type="checkbox" role="switch" aria-label="Pause" checked={paused} disabled={busy}
          onChange={(e) => void save({ router_enabled: !e.target.checked, library_enabled: !e.target.checked }, o.generation)} />
        <span className={paused ? "whitespace-nowrap font-medium text-[var(--warn-ink)]" : "whitespace-nowrap"}>{paused ? "Paused" : "Pause"}</span>
      </label>
      {error ? <span role="alert" className="text-xs text-destructive">{error}</span> : null}
      {dialog}
    </>
  );
}

/** Allow new downloads or Write library links under an Advanced fold on its settings page: the header's Pause sets
 * both. The fold starts open when the switch is off or a link points at it (#switches). */
export function AdvancedSwitch({ overview, field }: { overview: SettingsOverview; field: "router_enabled" | "library_enabled" }) {
  const { hash, key } = useLocation();
  const [open, setOpen] = useState(() => !overview.switches[field] || hash === "#switches");
  // A #switches link followed while already on this page (Pause: Custom) opens it too.
  const [seen, setSeen] = useState(key);
  if (key !== seen) {
    setSeen(key);
    if (hash === "#switches") setOpen(true);
  }
  return (
    <details id="switches" className="mb-4" open={open} data-testid="switches-advanced">
      <summary className="cursor-pointer text-sm font-medium" onClick={(e) => { e.preventDefault(); setOpen(!open); }}>Advanced</summary>
      {open ? <div className="mt-2 space-y-2">
        <p className="text-sm text-muted-foreground">Pause, at the top of every admin page, turns Allow new downloads and Write library links off or on together.</p>
        <SwitchCard overview={overview} field={field} />
      </div> : null}
    </details>
  );
}
