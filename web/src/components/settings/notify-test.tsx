import { useState } from "react";
import { api, ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";

/** Send test for a saved notification destination (POST settings/notify/targets/{name}/test): a Discord channel or
 * ntfy topic gets a short test message now. It tests what is saved, so unsaved edits are not part of it. */
export function NotifyTestButton({ name, disabled }: { name: string; disabled?: boolean }) {
  const [state, setState] = useState<{ busy?: boolean; ok?: boolean; error?: string }>({});
  const send = async () => {
    setState({ busy: true });
    try {
      const r = await api<{ ok: boolean; error?: string }>(`/settings/notify/targets/${encodeURIComponent(name)}/test`, { method: "POST" });
      setState({ ok: r.ok, error: r.error });
    } catch (e) {
      setState({ ok: false, error: e instanceof ApiError ? e.message : String(e) });
    }
  };
  return <div className="flex flex-wrap items-center gap-2">
    <Button type="button" variant="outline" size="sm" disabled={disabled || state.busy} onClick={() => void send()}>Send test</Button>
    <span className="text-xs text-muted-foreground">Sends to the saved destination; save changes first.</span>
    {state.ok ? <p role="status" className="text-sm">Test sent.</p> : state.error ? <p role="alert" className="text-sm text-destructive">{state.error}</p> : null}
  </div>;
}
