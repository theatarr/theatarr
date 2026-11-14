import { SetupReminder } from "@/components/setup-reminder";
import { SettingsAuthority } from "@/components/settings/authority";
import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { usePerms } from "@/api/library";
import { useSettings, useSettingsSchema, useSettingsSections } from "@/api/settings";
import { PageHeader } from "@/components/page";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import { SETTINGS_PAGES, ShowAdvancedToggle, useShowAdvanced } from "@/lib/settings-map";
import { findSettings, settingsIndex } from "@/lib/settings-search";
const COUNTED: Record<string, [string, string]> = { libraries: ["library", "libraries"], indexers: ["indexer", "indexers"], downloads: ["provider", "providers"] };
/** A settings tile's count: "1 library", "2 libraries". */
function countLabel(id: string, n: number): string {
  const [one, many] = COUNTED[id] ?? [id, id];
  return `${n} ${n === 1 ? one : many}`;
}
export function SettingsHome() {
 const schema = useSettingsSchema(); const sections = useSettingsSections(); const overview = useSettings(); const perms = usePerms();
 const [query, setQuery] = useState(""); const navigate = useNavigate();
 const index = useMemo(() => settingsIndex(schema.data, sections.data), [schema.data, sections.data]);
 const [advanced] = useShowAdvanced();
 // Advanced fields (credential environment variables and the like) are found only with Show advanced settings on.
 const results = findSettings(advanced ? index : index.filter(r => !r.advanced), query);
 const counts: Record<string, number | undefined> = { libraries: overview.data?.switches.versions.length, indexers: overview.data?.indexers.length, downloads: overview.data?.providers.length };
 return <div>
  <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Settings" description="Find settings by task, name or the app you used before." actions={<ShowAdvancedToggle />} />
  <SetupReminder />
  {overview.data ? <SettingsAuthority overview={overview.data} compact /> : null}
  <label className="block mb-5">Find a setting<Input type="search" value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => { if (e.key === "Enter" && results[0]) { e.preventDefault(); void navigate(results[0].href); } }} /></label>
  {query.trim() ? <ul className="mb-6 space-y-2" aria-label="Settings results">{results.map((r, i) => <li key={`${r.href}-${i}`}><Link className="block rounded border p-3 hover:bg-muted" to={r.href} title={r.path}><span>{r.label}</span>{r.field ? <span className="block text-sm">{r.field}</span> : null}</Link></li>)}{!results.length ? <li>No settings found.</li> : null}</ul> : null}
  <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{SETTINGS_PAGES.filter(p => !p.system).map(p => <Link key={p.id} to={p.route}><Card className="h-full"><h2 className="text-lg font-semibold">{p.label}</h2>{p.arr ? <p className="text-xs text-muted-foreground">{p.id === "quality-sync" ? p.arr : p.id === "indexers" ? "Prowlarr: Indexers" : p.id === "apps" ? `Prowlarr: ${p.arr}` : `Sonarr: ${p.arr}`}</p> : null}<p className="mt-2 text-sm">{p.description}</p>{counts[p.id] !== undefined ? <p className="mt-2 text-xs text-muted-foreground">{countLabel(p.id, counts[p.id]!)}</p> : null}</Card></Link>)}
  {perms.users ? <Link to="/admin/users"><Card><h2 className="text-lg font-semibold">Users &amp; access</h2><p>Accounts, permissions and API keys.</p></Card></Link> : null}</div>
  <p className="mt-6 text-sm text-muted-foreground"><Link to="/admin/setup">Setup checklist</Link>. YAML settings, settings history and request limits are under <Link to="/admin/advanced">Advanced</Link>.</p>
 </div>;
}
