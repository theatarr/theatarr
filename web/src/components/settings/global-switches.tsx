import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";
import { confirmationHeaders, type SettingsOverview } from "@/api/settings";
import { Card } from "@/components/ui/card";
import { useWriteConfirmation } from "./confirm-write";

const switches = [
  { key: "router", field: "router_enabled", label: "Allow new downloads", help: "Off: Theatarr adds no torrents to your debrid accounts. RSS checks and comparison searches still read indexers. Each library also has its own switch, Allow downloads for this library." },
  { key: "library", field: "library_enabled", label: "Write library links", help: "Off: Theatarr does not create or change links in your library folders." },
  { key: "notify", field: "notify_enabled", label: "Send notifications", help: "Allow messages to configured targets and installed notification clients." },
] as const;

type SwitchField = (typeof switches)[number]["field"];

export function GlobalSwitchesCard({ overview }: { overview: SettingsOverview }) {
 return <>{switches.map(s => <SwitchCard key={s.field} overview={overview} field={s.field} />)}</>;
}
export function SwitchCard({ overview, field: selected }: { overview: SettingsOverview; field: SwitchField }) {
  const qc = useQueryClient();
  const confirm = useWriteConfirmation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const save = async (field: SwitchField, enabled: boolean, generation: number, token?: string) => {
    setBusy(true);
    setError(undefined);
    try {
      await api("/settings/switches", { method: "PUT", body: { generation, [field]: enabled }, headers: confirmationHeaders(token) });
      void qc.invalidateQueries({ queryKey: qk.settings.all });
      void qc.invalidateQueries({ queryKey: qk.policy.all });
    } catch (e) {
      if (!confirm.ask(e, (next) => void save(field, enabled, generation, next))) setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
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
      {confirm.dialog}
    </Card>
  );
}
