import { useState, type ReactNode } from "react";
import type { SchemaField } from "@/api/settings";
import type { FieldValues, SecretAction } from "@/lib/settings";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ChangedMark, Field, SecretField, SELECT_CLASS } from "./parts";
import { NotifyTestButton } from "./notify-test";
// A number weight keeps a typed lone "-" (a plain number input set it to 0 at once), so a negative can be typed.
import { NumberInput } from "@/pages/admin/settings/quality/fields";
const recordLabel = (f: SchemaField) => f.label || ({ url: "Service URL", budget: "Request budget", pacing: "Add limits", cooldown: "Pause after a refusal", min_gap: "Minimum request gap", stalled_after: "Stalled add timeout", backoff_max: "Maximum retry delay" }[f.name] ?? f.name.replaceAll("_", " ").replace(/^./, c => c.toUpperCase()));
const recordHelp = (f: SchemaField) => f.name === "stalled_after" ? "Wait before a stalled add is given up." : f.help || `Set ${recordLabel(f).toLowerCase()} for this service.`;
const PLACEHOLDER: Record<string, string> = { list: "a, b, c", map: "name=10, other=-5", string_map: "name=value, other=value", duration: "5m", integer: "0" };

export function ConfigFields({
  fields,
  values,
  errors,
  onChange,
  disabled,
  path = "",
  saved,
}: {
  fields: SchemaField[];
  values: FieldValues;
  /** The saved values: edited fields are marked, with Undo back to them. */
  saved?: FieldValues;
  errors: Record<string, string>;
  onChange: (v: FieldValues) => void;
  disabled?: boolean;
  path?: string;
}) {
  return (
    <>
      {fields.map((f) => {
        const name = path + f.name;
        if (f.type === "object") {
          return (
            <fieldset key={name} className="grid gap-3 rounded-lg border border-border p-3 sm:col-span-2 sm:grid-cols-2">
              <legend className="px-1 text-sm font-medium text-[var(--ink)]">{recordLabel(f)}{keyHint(f) ? <> <small className="text-muted-foreground">{f.name}</small></> : null}</legend>
              <p className="text-[12.5px] text-muted-foreground sm:col-span-2">{recordHelp(f)}</p>
              <ConfigFields
                fields={f.fields ?? []}
                values={(values[f.name] ?? {}) as FieldValues}
                errors={errors}
                onChange={(sub) => onChange({ ...values, [f.name]: sub })}
                disabled={disabled}
                path={name + "."}
                saved={saved ? ((saved[f.name] ?? {}) as FieldValues) : undefined}
              />
            </fieldset>
          );
        }
        return <FieldInput key={name} record field={f} value={values[f.name]} path={name} errors={errors} disabled={disabled} tracked={!!saved} saved={saved?.[f.name]} onChange={(v) => onChange({ ...values, [f.name]: String(v ?? "") })} />;
      })}
    </>
  );
}


export type Config = Record<string, unknown>;
export const objectValue = (v: unknown): Config => v && typeof v === "object" && !Array.isArray(v) ? v as Config : {};
export function changeField(config: Config, name: string, value: unknown): Config {
  const next = { ...config };
  if (value === undefined) delete next[name]; else next[name] = value;
  return next;
}
export type SecretDraft = { actions: Record<string, SecretAction>; values: Record<string, string> };
export type SecretInfo = { path: string; set: boolean; env?: string };
type FieldInputProps = {
  /** Record editors retain their text draft format and input behavior. */
  record?: boolean;
  field: SchemaField; value: unknown; onChange: (value: unknown) => void; path: string;
  disabled?: boolean; advanced?: boolean; errors?: Record<string, string>;
  secrets?: SecretInfo[]; secretDraft?: SecretDraft; onSecrets?: (draft: SecretDraft) => void;
  /** Mark edits against saved (the stored value at this path; undefined when unset). */
  tracked?: boolean; saved?: unknown;
  /** The panel's heading: an object field named the same (router.wanted under "Automatic search") is not headed twice. */
  panelTitle?: string;
};
const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
/** The record's key beside its label only when it says something the label does not ("Requests", not "Requests requests"). */
const keyHint = (f: SchemaField) => recordLabel(f).toLowerCase().replace(/\s+/g, "_") !== f.name;

/** Shared recursive inputs use YAML-shaped values; untouched keys never pass through a coercion. */
export function FieldInput(p: FieldInputProps) {
  const { field: f, value, onChange, path, disabled } = p;
  const [customDuration, setCustomDuration] = useState(false);
  const [newName, setNewName] = useState("");
  const [chosenByteUnit, setByteUnit] = useState<string>();
  if (p.record) {
    const v = typeof value === "string" ? value : "";
    const changed = p.tracked && v !== (typeof p.saved === "string" ? p.saved : "");
    return (
          <Field changed={changed} onUndo={() => onChange(p.saved ?? "")} disabled={disabled} name={recordLabel(f)} label={<>{recordLabel(f)}{keyHint(f) ? <> <small className="font-normal text-muted-foreground">{f.name}</small></> : null}{f.required ? <span className="text-[var(--bad-ink)]"> *</span> : null}</>} help={recordHelp(f)} error={p.errors?.[path]} wide={f.name === "url" || f.type === "list" || f.type === "map" || f.type === "string_map"}>
            {f.type === "enum" ? (
              <select className={SELECT_CLASS} value={v} disabled={disabled} onChange={(e) => onChange(e.target.value)} data-field={path}>
                {f.required ? null : <option value="">(unset)</option>}
                {(f.enum ?? []).map((o) => <option key={o} value={o}>{o}</option>)}
              </select>
            ) : (
              <Input
                className="h-9"
                value={v}
                inputMode={f.type === "integer" ? "numeric" : undefined}
                placeholder={PLACEHOLDER[f.type]}
                disabled={disabled}
                onChange={(e) => onChange(e.target.value)}
                data-field={path}
              />
            )}
          </Field>
    );
  }
  const label = f.label || f.name;
  const clear = () => onChange(undefined);
  const id = `field-${path.replaceAll(".", "-")}`;
  const error = p.errors?.[path];
  const describe = { "aria-describedby": [error ? `${id}-error` : "", f.help ? `${id}-help` : ""].filter(Boolean).join(" ") || undefined, "aria-invalid": error ? true : undefined };
  const checked = value ?? (f.default === "true");
  const input = (type = "text") => <Input aria-label={label} {...describe} data-field={path} type={type} value={typeof value === "string" || typeof value === "number" ? value : ""} min={f.min} max={f.max} step={f.type === "integer" ? 1 : "any"} placeholder={f.default} disabled={disabled} onChange={(e) => onChange(e.target.value === "" ? (f.nullable ? null : undefined) : type === "number" ? Number(e.target.value) : e.target.value)} />;
  const children = (config: Config, prefix: string, change: (v: Config) => void, saved: unknown) => <SectionFields {...p} fields={f.fields ?? []} config={config} path={prefix} onChange={change} saved={p.tracked ? objectValue(saved) : undefined} />;
  let control;
  switch (f.type) {
    case "secret": {
      const info = p.secrets?.find((s) => s.path === path);
      const draft = p.secretDraft ?? { actions: {}, values: {} };
      return <div data-field={path}><SecretField label={label} set={info?.set ?? false} hint={info?.env ? `Environment fallback: ${info.env}` : undefined} action={draft.actions[path] ?? "keep"} value={draft.values[path] ?? ""} onAction={(action) => p.onSecrets?.({ ...draft, actions: { ...draft.actions, [path]: action } })} onValue={(text) => p.onSecrets?.({ ...draft, values: { ...draft.values, [path]: text } })} disabled={disabled} />{p.errors?.[path] ? <p role="alert">{p.errors[path]}</p> : null}</div>;
    }
    case "boolean": control = <Button type="button" variant="outline" role="switch" aria-label={label} {...describe} aria-checked={checked === true} disabled={disabled} className="w-fit" onClick={() => onChange(checked !== true)}>{value === true ? "On" : value === false ? "Off" : `Default${f.default ? ` (${f.default})` : ""}`}</Button>; break;
    case "integer": case "number": control = input("number"); break;
    case "enum": control = <select aria-label={label} {...describe} className={SELECT_CLASS} disabled={disabled} value={String(value ?? "")} onChange={(e) => onChange(e.target.value || undefined)}><option value="">Default{f.default ? ` (${f.default})` : ""}</option>{f.enum?.map((v) => <option key={v}>{v}</option>)}</select>; break;
    case "duration": {
      const v = String(value ?? "");
      const seconds = parseDuration(v);
      const allowed = durationPresets(f, seconds).filter(([, n]) => n >= (f.min ?? 0) && n <= (f.max ?? Infinity));
      const unsetIsOff = f.zero_allowed && (!f.default || parseDuration(f.default) === 0);
      const preset = allowed.find(([, n]) => n === seconds)?.[0];
      const selected = customDuration ? "custom" : seconds === 0 && f.zero_allowed ? "0s" : preset ?? (v ? "custom" : unsetIsOff ? "0s" : "");
      control = <><select aria-label={label} {...describe} className={SELECT_CLASS} disabled={disabled} value={selected} onChange={(e) => { setCustomDuration(e.target.value === "custom"); if (e.target.value !== "custom") onChange(e.target.value === "0s" && unsetIsOff ? undefined : e.target.value || undefined); }}><option value="">Default{f.default ? ` (${f.default})` : ""}</option>{f.zero_allowed ? <option value="0s">Off</option> : null}{allowed.map(([s]) => <option key={s} value={s}>{s === "168h" ? "7 days" : s}</option>)}<option value="custom">Custom</option></select>{selected === "custom" ? input() : null}</>; break;
    }
    case "bytes": {
      const units: Record<string, number> = { B: 1, KB: 1e3, MB: 1e6, GB: 1e9 };
      // Stored byte counts and unit strings are displayed without rewriting them until edited.
      const match = typeof value === "string" ? value.match(/^([\d.]+)\s*(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$/) : null;
      const multipliers: Record<string, number> = { B: 1, KB: 1e3, MB: 1e6, GB: 1e9, TB: 1e12, KiB: 1024, MiB: 1048576, GiB: 1073741824, TiB: 1099511627776 };
      const bytes = typeof value === "number" ? value : match ? Number(match[1]) * (multipliers[match[2] || "B"]) : undefined;
      const byteUnit = chosenByteUnit ?? (["GB", "MB", "KB"].find((unit) => bytes !== undefined && bytes > 0 && bytes % units[unit] === 0) || (bytes ? "B" : "MB"));
      control = <div className="flex gap-2"><Input aria-label={label} {...describe} type="number" min={f.min === undefined ? undefined : f.min / units[byteUnit]} max={f.max === undefined ? undefined : f.max / units[byteUnit]} step="any" placeholder={f.default} disabled={disabled} value={bytes === undefined ? "" : bytes / units[byteUnit]} onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value) * units[byteUnit])} /><select aria-label={`${label} unit`} className={SELECT_CLASS} disabled={disabled} value={byteUnit} onChange={(e) => setByteUnit(e.target.value)}><option>B</option><option>KB</option><option>MB</option><option>GB</option></select></div>; break;
    }
    // An object the saved settings did not have goes back to absent once its last key is undone, so Undo returns the
    // draft to the saved value (no stray {} to count or send).
    case "object": control = <fieldset className="grid gap-4 rounded border p-3">{children(objectValue(value), path, (v) => onChange(p.tracked && p.saved == null && !Object.keys(objectValue(v)).length ? undefined : v), p.saved)}</fieldset>; break;
    case "keyed": {
      const records = objectValue(value);
      const managed = path === "library.versions";
      control = <div className="grid gap-3">{Object.entries(records).map(([key, record]) => <KeyedRecord key={key} {...p} name={key} records={records} managed={managed}>{children(objectValue(record), `${path}.${key}`, (v) => onChange({ ...records, [key]: v }), objectValue(p.saved)[key])}</KeyedRecord>)}{!managed ? <div className="flex gap-2"><Input aria-label={`New ${label} name`} placeholder="Name" value={newName} disabled={disabled} onChange={(e) => setNewName(e.target.value)} /><Button type="button" disabled={disabled || !newName.trim() || Object.hasOwn(records, newName.trim())} onClick={() => { onChange({ ...records, [newName.trim()]: {} }); setNewName(""); }}>Add {label}</Button>{newName && Object.hasOwn(records, newName.trim()) ? <p role="alert">That name already exists.</p> : null}</div> : null}</div>; break;
    }
    case "object_list": case "list": {
      const rows = Array.isArray(value) ? value : [];
      const update = (i: number, v: unknown) => onChange(rows.map((r, n) => n === i ? v : r));
      const move = (i: number, d: number) => { const next = [...rows]; [next[i], next[i + d]] = [next[i + d], next[i]]; onChange(next); };
      // Each row's control names its own indexed error, the list's error and the help.
      const rowDescribe = (i: number) => {
        const rowError = p.errors?.[`${path}.${i}`];
        return { "aria-describedby": [rowError ? `${id}-${i}-error` : "", describe["aria-describedby"] ?? ""].filter(Boolean).join(" ") || undefined, "aria-invalid": rowError || error ? true : undefined };
      };
      control = <div className="grid gap-2">{rows.map((row, i) => <div key={i} className="rounded border p-2">{f.type === "object_list" ? children(objectValue(row), `${path}.${i}`, (v) => update(i, v), Array.isArray(p.saved) ? p.saved[i] : undefined) : f.enum ? <select aria-label={`${label} ${i + 1}`} {...rowDescribe(i)} className={SELECT_CLASS} disabled={disabled} value={String(row)} onChange={(e) => update(i, e.target.value)}><option value="">Choose…</option>{f.enum.map((v) => <option key={v}>{v}</option>)}</select> : <Input aria-label={`${label} ${i + 1}`} {...rowDescribe(i)} value={String(row)} disabled={disabled} onChange={(e) => update(i, e.target.value)} />}{p.errors?.[`${path}.${i}`] ? <p id={`${id}-${i}-error`} role="alert">{p.errors[`${path}.${i}`]}</p> : null}<Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(rows.filter((_, n) => n !== i))}>Remove {i + 1}</Button>{f.type === "object_list" ? <><Button type="button" variant="ghost" disabled={disabled || i === 0} onClick={() => move(i, -1)}>Move up</Button><Button type="button" variant="ghost" disabled={disabled || i === rows.length - 1} onClick={() => move(i, 1)}>Move down</Button></> : null}</div>)}<Button type="button" variant="outline" disabled={disabled} onClick={() => onChange([...rows, f.type === "object_list" ? {} : ""])}>Add {label}</Button></div>; break;
    }
    case "map": case "string_map": {
      const entries = objectValue(value);
      control = <div className="grid gap-2">{Object.entries(entries).map(([key, v]) => <div key={key} className="flex gap-2"><Input aria-label={`${label} key ${key}`} defaultValue={key} disabled={disabled} onBlur={(e) => { const name = e.target.value; if (name !== key && !Object.hasOwn(entries, name)) onChange({ ...changeField(entries, key, undefined), [name]: v }); }} />{f.type === "map" ? <NumberInput aria-label={`${label} value ${key}`} value={String(v)} disabled={disabled} onChange={(n) => onChange({ ...entries, [key]: Number(n) })} /> : <Input aria-label={`${label} value ${key}`} value={String(v)} disabled={disabled} onChange={(e) => onChange({ ...entries, [key]: e.target.value })} />}<Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(changeField(entries, key, undefined))}>Remove {key}</Button></div>)}<Button type="button" variant="outline" disabled={disabled || Object.hasOwn(entries, "")} onClick={() => onChange({ ...entries, "": f.type === "map" ? 0 : "" })}>Add {label}</Button></div>; break;
    }
    default: control = input();
  }
  const leaf = !["object", "keyed"].includes(f.type);
  const changed = p.tracked && leaf && !same(value, p.saved);
  return <div data-field={path} data-changed={changed || undefined} className="grid min-w-0 gap-1"><div className="flex items-start justify-between gap-x-2">{f.type === "object" && p.panelTitle === label ? null : <span className="min-w-0 text-sm font-medium [overflow-wrap:anywhere]">{label}</span>}{leaf && value !== undefined && value !== null ? <Button type="button" size="xs" variant="ghost" className="shrink-0" disabled={disabled} onClick={clear} aria-label={`Reset ${label}`} title={f.default ? `Back to the default (${f.default})` : "Back to the default"}>Reset</Button> : null}</div>{control}{f.unit ? <span className="text-xs">{f.unit}</span> : null}{error ? <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{error}</p> : null}{f.help ? <p id={`${id}-help`} className="text-xs text-muted-foreground">{f.help}</p> : null}{changed ? <ChangedMark name={label} disabled={disabled} onUndo={() => onChange(structuredClone(p.saved))} /> : null}</div>;
}

export function SectionFields(p: Omit<FieldInputProps, "field" | "value" | "onChange" | "saved" | "tracked"> & { fields: SchemaField[]; config: Config; onChange: (v: Config) => void; saved?: Config }) {
  const visible = p.fields.filter((f) => !f.advanced || p.advanced);
  const groups = [...new Set(visible.map((f) => f.group || ""))];
  return <>{groups.map((group) => <div key={group} className={group ? "grid gap-4 rounded border p-3" : "grid gap-4"}>{group ? <h3>{group}</h3> : null}{visible.filter((f) => (f.group || "") === group).map((f) => <FieldInput {...p} key={f.name} field={f} tracked={!!p.saved} saved={p.saved && (f.name ? p.saved[f.name] : p.saved)} path={f.name ? `${p.path}.${f.name}` : p.path} value={f.name ? p.config[f.name] : p.config} onChange={(v) => p.onChange(f.name ? changeField(p.config, f.name, v) : objectValue(v))} />)}</div>)}</>;
}

// Go policy.Duration accepts h/m/s (not days). Match compound serialized values too.
// Duration presets on the field's scale: its default (else its minimum) under a minute picks seconds, under an hour
// minutes, otherwise hours. A 6m default is not offered beside 1h and longer only.
const DURATION_PRESETS = {
  seconds: [["5s", 5], ["10s", 10], ["30s", 30], ["1m", 60], ["5m", 300]],
  minutes: [["1m", 60], ["5m", 300], ["10m", 600], ["30m", 1800], ["1h", 3600]],
  hours: [["1h", 3600], ["6h", 21600], ["12h", 43200], ["24h", 86400], ["168h", 604800]],
} as const;
// A saved value that is a preset of another scale stays listed, so it shows as that preset rather than Custom.
function durationPresets(f: SchemaField, saved?: number): readonly (readonly [string, number])[] {
  const scale = (f.default ? parseDuration(f.default) : undefined) || f.min || 0;
  const presets: readonly (readonly [string, number])[] = scale > 0 && scale < 60 ? DURATION_PRESETS.seconds : scale > 0 && scale < 3600 ? DURATION_PRESETS.minutes : DURATION_PRESETS.hours;
  if (saved === undefined || presets.some(([, n]) => n === saved)) return presets;
  const other = Object.values(DURATION_PRESETS).flat().find(([, n]) => n === saved);
  return other ? [...presets, other].sort((a, b) => a[1] - b[1]) : presets;
}

export function parseDuration(value: string): number | undefined {
  if (value === "0") return 0;
  if (!/^(?:\d+(?:\.\d+)?[hms])+$/.test(value)) return undefined;
  return [...value.matchAll(/(\d+(?:\.\d+)?)([hms])/g)].reduce((sum, m) => sum + Number(m[1]) * ({ h: 3600, m: 60, s: 1 }[m[2]] ?? 0), 0);
}

function KeyedRecord(p: FieldInputProps & { name: string; records: Config; managed: boolean; children: ReactNode }) {
  const { name, records, path, disabled } = p;
  const [nameDraft, setName] = useState(name);
  const [url, setURL] = useState("");
  // A Find a setting result for this record (?open=<path>.<name>) arrives with it unfolded.
  const [unfolded] = useState(() => new URLSearchParams(window.location.search).get("open") === `${path}.${name}`);
  const nextName = nameDraft.trim();
  const prefix = `${path}.${name}.`;
  const draft = p.secretDraft ?? { actions: {}, values: {} };
  const stored = p.secrets?.filter((s) => s.set && s.path.startsWith(prefix)) ?? [];
  const record = objectValue(records[name]);
  const maskedURL = path === "notify.targets" && typeof record.url === "string" && /\[redacted\]|\*{8}/.test(record.url);
  const renaming = nameDraft !== name;
  const invalid = !nextName ? "Enter a name." : nextName !== name && Object.hasOwn(records, nextName) ? "That name already exists." : "";
  const credentials = stored.map((s) => ({ path: s.path, label: s.path.slice(prefix.length).replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase()) }));
  const ready = credentials.every((s) => draft.actions[s.path] === "clear" || draft.actions[s.path] === "replace" && !!draft.values[s.path]) && (!maskedURL || !!url.trim() && !/\[redacted\]|\*{8}/.test(url));
  const moveDraft = (destination?: string) => {
    const next: SecretDraft = { actions: {}, values: {} };
    for (const [key, action] of Object.entries(draft.actions)) {
      if (!key.startsWith(prefix)) next.actions[key] = action;
      else if (destination) next.actions[destination + key.slice(prefix.length)] = action;
    }
    for (const [key, value] of Object.entries(draft.values)) {
      if (!key.startsWith(prefix)) next.values[key] = value;
      else if (destination) next.values[destination + key.slice(prefix.length)] = value;
    }
    p.onSecrets?.(next);
  };
  return <details className="rounded border p-3" open={unfolded || undefined}><summary>{name}</summary>{!p.managed ? <>
    <label>Rename<Input aria-label={`Rename ${name}`} value={nameDraft} disabled={disabled} onChange={(e) => setName(e.target.value)} /></label>
    {renaming && (stored.length > 0 || maskedURL) ? <div className="grid gap-2"><p>Renaming needs its credentials again</p>{credentials.map((s) => <label key={s.path}>{s.label}{draft.actions[s.path] === "clear" ? <span> — will be removed</span> : <Input aria-label={`Rename ${s.label}`} type="password" required value={draft.values[s.path] ?? ""} disabled={disabled} onChange={(e) => p.onSecrets?.({ actions: { ...draft.actions, [s.path]: "replace" }, values: { ...draft.values, [s.path]: e.target.value } })} />}</label>)}{maskedURL ? <label>Full URL<Input aria-label="Rename URL" required value={url} disabled={disabled} onChange={(e) => setURL(e.target.value)} /></label> : null}</div> : null}
    {invalid ? <p role="alert">{invalid}</p> : null}
    <Button type="button" variant="outline" disabled={disabled || !renaming || !!invalid || !ready} onClick={() => { moveDraft(`${path}.${nextName}.`); p.onChange({ ...changeField(records, name, undefined), [nextName]: maskedURL ? { ...record, url } : record }); }}>Rename {name}</Button>
  </> : null}{p.children}{path === "notify.targets" && !renaming ? <NotifyTestButton name={name} disabled={disabled} /> : null}{!p.managed ? <Button type="button" variant="outline" disabled={disabled} onClick={() => { moveDraft(); p.onChange(changeField(records, name, undefined)); }}>Remove {name}</Button> : null}</details>;
}
