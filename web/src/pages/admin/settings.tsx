// Settings › Indexers, Download clients; Advanced › Request limits, Import & export, Audit (docs/design/settings.md): the
// settings database's sources over the settings API. Every tab starts with where the settings come from (the database,
// or the policy files under --settings-source yaml, when everything here is read-only) and the global switches link.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/api/client";
import { usePolicy } from "@/api/hooks";
import { qk } from "@/api/query-keys";
import { confirmationHeaders, useSettings, useSettingsSchema, type SettingsOverview, type SettingsSchema } from "@/api/settings";
import { ErrorState, Loading } from "@/components/page";
import { AuditPanel } from "@/components/settings/audit";
import { useWriteConfirmation } from "@/components/settings/confirm-write";
import { ImportPanel } from "@/components/settings/import";
import { AuthorityBanner, ExplainBox, ManagedInSettings } from "@/components/settings/parts";
import { BudgetsPanel, SourcesPanel } from "@/components/settings/sources";
import { authority, explainReloadError, type Explained } from "@/lib/settings";

export const SETTINGS_TABS = ["indexers", "providers", "budgets", "import", "audit"] as const;
export type SettingsTabId = (typeof SETTINGS_TABS)[number];

/** One settings tab over loaded data: the plain composition the tests render. */
export function SettingsTabView({
  tab,
  overview,
  schema,
  versions,
  onReload,
  reloading,
  reloadError,
}: {
  tab: SettingsTabId;
  overview: SettingsOverview;
  schema: SettingsSchema;
  versions: { key: string; providers: string[] }[];
  onReload?: () => void;
  reloading?: boolean;
  reloadError?: Explained;
}) {
  const a = authority(overview);
  const readOnly = a.readOnly;
  return (
    <div data-testid="settings-tab" data-tab={tab} data-readonly={readOnly ? "true" : "false"}>
      <AuthorityBanner overview={overview} onReload={onReload} reloading={reloading} />
      {reloadError ? <ExplainBox error={reloadError} className="mb-4" /> : null}
      <ManagedInSettings />
      {tab === "indexers" || tab === "providers" ? (
        // Keyed by entity: a tab change starts a new panel, so no editor, test or confirmation crosses tabs.
        <SourcesPanel key={tab} entity={tab} overview={overview} ent={schema[tab]} versions={versions} readOnly={readOnly} />
      ) : tab === "budgets" ? (
        <BudgetsPanel overview={overview} ent={schema.budgets} readOnly={readOnly} />
      ) : tab === "import" ? (
        <ImportPanel overview={overview} readOnly={readOnly} />
      ) : (
        <AuditPanel readOnly={readOnly} />
      )}
    </div>
  );
}

export function SettingsTab({ tab }: { tab: SettingsTabId }) {
  const o = useSettings();
  const schema = useSettingsSchema();
  const pol = usePolicy();
  const qc = useQueryClient();
  const [reloading, setReloading] = useState(false);
  const [reloadError, setReloadError] = useState<Explained>();
  const confirm = useWriteConfirmation();
  if (o.isPending || schema.isPending) return <Loading />;
  if (o.error) return <ErrorState error={o.error} />;
  if (schema.error) return <ErrorState error={schema.error} />;
  // After a startup fallback the head can enable writes the running generation does not: a 422 lists them, and
  // the reload is sent again with the confirmed token.
  const reload = async (token?: string) => {
    setReloading(true);
    setReloadError(undefined);
    try {
      await api("/settings/reload", { method: "POST", body: { generation: o.data.generation }, headers: confirmationHeaders(token) });
    } catch (e) {
      if (!confirm.ask(e, (next) => void reload(next))) setReloadError(explainReloadError(e));
    } finally {
      setReloading(false);
      void qc.invalidateQueries({ queryKey: qk.settings.all });
    }
  };
  return (
    <>
    {confirm.dialog}
    <SettingsTabView
      tab={tab}
      overview={o.data}
      schema={schema.data}
      versions={(pol.data?.versions ?? []).map((v) => ({ key: v.key, providers: v.providers }))}
      onReload={() => void reload()}
      reloading={reloading}
      reloadError={reloadError}
    />
    </>
  );
}
