import { PlexPlaylists } from "./plex-playlists";
import { authority } from "@/lib/settings";
import { Link, useLocation, useNavigationType } from "react-router";
import { useLayoutEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { qk } from "@/api/query-keys";
import { useSettings, useSettingsSchema } from "@/api/settings";
import { PageHeader, RouteTabs, Loading, ErrorState } from "@/components/page";
import { SETTINGS_PAGES, ADVANCED_SETTINGS_PAGES, SETTINGS_PANELS, ShowAdvancedToggle } from "@/lib/settings-map";
import { SectionPanel } from "@/components/settings/section-panel";
import { SettingsAuthority, settingsBannerNeeded as bannerNeeded } from "@/components/settings/authority";
import { SourcesPanel, BudgetsPanel } from "@/components/settings/sources";
import { ViewsCard } from "@/components/settings/views";
import { SectionsCard } from "@/components/settings/sections";
import { ImportPanel } from "@/components/settings/import";
import { AuditPanel } from "@/components/settings/audit";
import { VersionsPage } from "./versions";
import { ProfilesPage } from "./profiles";
import { ReleaseCheck } from "./profiles/check";
import { FormatsPage } from "./formats";
import { QualityPanel } from "./quality";
import { ProviderStatus, IndexerStatus } from "../sources";
import { VpnStatus, RequestLimitsStatus } from "./network";
import { PolicyFiles } from "../policy";
import { LeaveGuard } from "@/components/settings/unsaved-bar";
import { SettingsPicker } from "@/components/settings/settings-picker";
export { SettingsPicker };
const HELP: Record<string, string> = {
 libraries: "Each library is a folder Theatarr fills and the Plex library that shows it, for example Movies 4K and Movies 1080p. Each has its own quality profile, indexers and debrid providers.",
 downloads: "Debrid providers are tried in the order shown. Download clients take the grabs of a library set to send them there: a decypharr client adds to its own debrid account. A seedbox qBittorrent client is not supported yet: nothing sends grabs to it.",
 apps: "Sonarr- and Radarr-compatible addresses for Seerr and other apps. Each shows the libraries you choose.",
};
export function SettingsPage() {
 const { pathname } = useLocation();
 // Another settings page opens at its top (the window scrolls); Back and Forward leave the scroll to the browser, and
 // a change on the same page (an editor's ?edit=) keeps it.
 const navigation = useNavigationType();
 const shown = useRef<string>(undefined);
 useLayoutEffect(() => {
  if (shown.current === pathname) return;
  shown.current = pathname;
  if (navigation !== "POP") window.scrollTo(0, 0);
 }, [pathname, navigation]);
 // System › Backup reads the torrent-list schedule: a saved change shows there at once.
 const qc = useQueryClient();
 const overview = useSettings(); const schema = useSettingsSchema();
 const page = [...SETTINGS_PAGES, ...ADVANCED_SETTINGS_PAGES].find(p => pathname === p.route || pathname.startsWith(p.route + "/"));
 if (!page) return null;
 const tab = page.tabs.find(t => t.route === pathname)?.id ?? page.tabs[0]?.id;
 const o = overview.data; const s = schema.data;
 const phonePicker = SETTINGS_PAGES.some(p => p.id === page.id && !p.system);
 // The raw format set editor sits under Profiles without a tab of its own: it names itself and leads back.
 const formatSets = page.id === "profiles" && pathname.endsWith("/format_sets");
 return <div>
  <LeaveGuard />
  <PageHeader crumb={[{ label: "Admin", to: "/admin" }, { label: "Settings", to: "/admin/settings" }, ...(formatSets ? [{ label: page.label, to: page.route }] : [])]} title={formatSets ? "Format sets" : page.label} description={formatSets ? "The raw format set editor: each profile scores the formats of its own set." : HELP[page.id] ?? page.description} actions={<ShowAdvancedToggle className="max-md:hidden" />} />
  {formatSets ? <p className="mb-4"><Link className="text-sm text-[var(--accent-ink)] underline" to={page.route} data-testid="back-to-profiles">Back to profiles</Link></p> : null}
  {phonePicker ? <SettingsPicker current={formatSets ? pathname : page.tabs.find(t => t.id === tab)?.route ?? page.route} extra={formatSets ? { route: pathname, label: "Profiles › Format sets" } : undefined} /> : null}
  {phonePicker ? <ShowAdvancedToggle className="mb-4 md:hidden" /> : null}
  {o && bannerNeeded(o) && !["profiles", "formats", "quality-sync"].includes(page.id) && !(page.id === "libraries" && tab === "naming") ? <SettingsAuthority overview={o} compact /> : null}
  {overview.error || schema.error ? <ErrorState error={overview.error ?? schema.error} /> : null}
  {!o || !s ? <Loading /> : <>
   {page.tabs.length ? <div className={phonePicker ? "max-md:hidden" : undefined}><RouteTabs tabs={page.tabs.map(t => ({ ...t, to: t.route }))} active={tab!} /></div> : null}
   {page.id === "libraries" && tab === "libraries" ? <VersionsPage embedded /> : null}
   {page.id === "libraries" && tab === "naming" ? <QualityPanel compact entity="naming" /> : null}
   {page.id === "profiles" ? pathname.endsWith("/format_sets") ? <QualityPanel compact entity="format_sets" /> : tab === "check" ? <ReleaseCheck /> : <ProfilesPage compact /> : null}
   {page.id === "formats" ? <FormatsPage compact /> : null}
   {page.id === "quality-sync" ? <QualityPanel compact entity="sync_sources" /> : null}
   {page.id === "apps" ? <ViewsCard readOnly={!o.writable} /> : null}
   {page.id === "indexers" && tab === "indexers" ? <><SourcesPanel entity="indexers" overview={o} ent={s.indexers} readOnly={authority(o).readOnly} /><IndexerStatus /></> : null}
   {page.id === "downloads" && tab === "providers" ? <><SourcesPanel entity="providers" overview={o} ent={s.providers} readOnly={authority(o).readOnly} /></> : null}
   {page.id === "indexers" && tab === "vpn" ? <VpnStatus /> : null}
   {SETTINGS_PANELS.filter(p => p.page === page.id && p.tab === tab).map(p => <SectionPanel key={p.id} {...p} onSaved={p.id === "torrent-backups" ? () => { void qc.invalidateQueries({ queryKey: qk.torrents.backups }); } : undefined} />)}
   {/* Provider status loads after the panels above, so a link to #provider-sync is not pushed away. */}
   {page.id === "downloads" && tab === "providers" ? <ProviderStatus /> : null}
   {page.id === "plex" ? <PlexPlaylists /> : null}
  </>}
 </div>;
}
const RAW_TABS = ADVANCED_SETTINGS_PAGES.find(p => p.id === "config")!.tabs.map(t => ({ ...t, to: t.route }));
export function AdvancedSettingsPage() {
 const { pathname } = useLocation();
 const overview = useSettings(); const schema = useSettingsSchema();
 const limits = pathname.endsWith("/limits"); const history = pathname.endsWith("/history");
 const tab = RAW_TABS.find(t => t.to === pathname)?.id ?? "files";
 return <div><LeaveGuard /><PageHeader crumb={[{ label: "Admin", to: "/admin" }, { label: "Advanced", to: "/admin/advanced" }]} title={ADVANCED_SETTINGS_PAGES.find(p => p.id === (limits ? "limits" : history ? "history" : "config"))!.label} />
  {limits ? <p><Link className="text-primary underline" to="/admin/settings/indexers/vpn">VPN status: Indexers › VPN</Link></p> : null}
  {overview.data && (!limits && !history || bannerNeeded(overview.data)) ? <SettingsAuthority overview={overview.data} /> : null}
  {!limits && !history ? <RouteTabs tabs={RAW_TABS} active={tab} /> : null}
  {overview.error || schema.error ? <ErrorState error={overview.error ?? schema.error} /> : !overview.data || !schema.data ? <Loading /> : limits ? <><BudgetsPanel overview={overview.data} ent={schema.data.budgets} readOnly={authority(overview.data).readOnly} /><RequestLimitsStatus /></> : history ? <AuditPanel readOnly={authority(overview.data).readOnly} /> : tab === "files" ? <PolicyFiles /> : tab === "sections" ? <SectionsCard readOnly={!overview.data.writable} /> : <ImportPanel overview={overview.data} readOnly={authority(overview.data).readOnly} />}
 </div>;
}
