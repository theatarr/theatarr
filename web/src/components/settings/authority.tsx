import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { confirmationHeaders, type SettingsOverview } from "@/api/settings";
import { qk } from "@/api/query-keys";
import { authority, explainError, type Explained } from "@/lib/settings";
import { AuthorityBanner, ExplainBox } from "./parts";
import { useWriteConfirmation } from "./confirm-write";
/** A task page shows the settings notice only when something is off (startup fallback, the service unavailable); the
 * normal "saved in Theatarr" note is on Settings home. */
export const settingsBannerNeeded = (o: SettingsOverview) => { const a = authority(o); return a.mode !== "db" || !!a.fallback; };
/** The existing reload flow, also available beside the task pages after startup fallback. */
export function SettingsAuthority({ overview, compact = false }: { overview: SettingsOverview; compact?: boolean }) {
 const qc = useQueryClient(); const confirm = useWriteConfirmation();
 const [reloading, setReloading] = useState(false); const [error, setError] = useState<Explained>();
 const reload = async (token?: string) => {
  setReloading(true); setError(undefined);
  try { await api("/settings/reload", { method: "POST", body: { generation: overview.generation }, headers: confirmationHeaders(token) }); }
  catch (e) { if (!confirm.ask(e, next => void reload(next))) setError(explainError(e)); }
  finally { setReloading(false); void qc.invalidateQueries({ queryKey: qk.settings.all }); }
 };
 return <><AuthorityBanner overview={overview} compact={compact} onReload={() => void reload()} reloading={reloading} />{error ? <ExplainBox error={error} /> : null}{confirm.dialog}</>;
}
