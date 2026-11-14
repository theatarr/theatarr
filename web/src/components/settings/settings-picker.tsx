import { useId } from "react";
import { useNavigate } from "react-router";
import { Select } from "@/components/ui/select";
import { SETTINGS_PAGES } from "@/lib/settings-map";
/** Phones (docs/design/ui.md "Settings"): the side list is hidden there, so one labelled select at the top of
 * Settings moves between the settings pages and their tabs (grouped by page), in place of a page picker and a
 * section picker. */
export function SettingsPicker({ current, extra }: { current: string; extra?: { route: string; label: string } }) {
 const id = useId();
 const navigate = useNavigate();
 return <div className="mb-4 grid gap-1 md:hidden" data-testid="settings-picker">
  <label htmlFor={id} className="text-xs font-medium text-[var(--ink-3)]">Settings page</label>
  <Select id={id} className="h-10 text-[15px]" value={current} onChange={(e) => navigate(e.target.value)}>
   {SETTINGS_PAGES.filter(p => !p.system).map(p => p.tabs.length > 1
    ? <optgroup key={p.id} label={p.label}>{p.tabs.map(t => <option key={t.id} value={t.route}>{t.label === p.label ? p.label : `${p.label} › ${t.label}`}</option>)}</optgroup>
    : <option key={p.id} value={p.route}>{p.label}</option>)}
   {extra ? <option value={extra.route}>{extra.label}</option> : null}
  </Select>
 </div>;
}
