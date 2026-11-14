import { useState, type ReactNode } from "react";
import type { SchemaField } from "@/api/settings";
import type { FieldValues, SecretAction } from "@/lib/settings";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ChangedMark, Field, SecretField, SELECT_CLASS } from "./parts";
import { NotifyTestButton } from "./notify-test";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { plural } from "@/lib/format";
import { useLibraryName } from "@/lib/library-names";
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
  /** The configured libraries' keys, for a per-library record (library.versions): a stored row for a key not among
   *  them is marked "library removed" and can be removed. Undefined while they load. */
  libraryKeys?: string[];
};
const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
/** The record's key beside its label only when it says something the label does not ("Requests", not "Requests requests"). */
const keyHint = (f: SchemaField) => recordLabel(f).toLowerCase().replace(/\s+/g, "_") !== f.name;

/** Shared recursive inputs use YAML-shaped values; untouched keys never pass through a coercion. */
export function FieldInput(p: FieldInputProps) {
  const { field: f, value, onChange, path, disabled } = p;
  const [newName, setNewName] = useState("");
  // Reset and Undo start the duration and size controls afresh from the value they set.
  const [resets, setResets] = useState(0);
  if (p.record) {
    const v = typeof value === "string" ? value : "";
    const changed = p.tracked && v !== (typeof p.saved === "string" ? p.saved : "");
    return (
          <Field changed={changed} onUndo={() => { setResets((n) => n + 1); onChange(p.saved ?? ""); }} disabled={disabled} name={recordLabel(f)} label={<>{recordLabel(f)}{keyHint(f) ? <> <small className="font-normal text-muted-foreground">{f.name}</small></> : null}{f.required ? <span className="text-[var(--bad-ink)]"> *</span> : null}</>} help={recordHelp(f)} error={p.errors?.[path]} wide={f.name === "url" || f.type === "list" || f.type === "map" || f.type === "string_map"}>
            {f.type === "duration" ? (
              <DurationControl key={resets} f={f} label={recordLabel(f)} path={path} disabled={disabled} value={v} onChange={(x) => onChange(typeof x === "string" ? x : "")} raw={() => <Input className="h-9" aria-label={`${recordLabel(f)} text`} value={v} disabled={disabled} onChange={(e) => onChange(e.target.value)} data-field={path} />} />
            ) : f.type === "enum" ? (
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
  const clear = () => { setResets((n) => n + 1); onChange(undefined); };
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
    // A switch with its state in words; "(default)" while nothing is stored, so an untouched field stays unset.
    case "boolean": control = <div className="flex items-center gap-2"><Switch aria-label={label} {...describe} checked={checked === true} disabled={disabled} onCheckedChange={(next) => onChange(next)} /><span className="text-sm" data-testid="switch-state">{checked === true ? "On" : "Off"}{value === undefined || value === null ? " (default)" : ""}</span></div>; break;
    case "integer": case "number": control = input("number"); break;
    case "enum": control = <select aria-label={label} {...describe} className={SELECT_CLASS} disabled={disabled} value={String(value ?? "")} onChange={(e) => onChange(e.target.value || undefined)}><option value="">Default{f.default ? ` (${f.default})` : ""}</option>{f.enum?.map((v) => <option key={v}>{v}</option>)}</select>; break;
    case "duration": control = <DurationControl key={resets} f={f} label={label} describe={describe} path={path} disabled={disabled} value={value} onChange={onChange} raw={input} />; break;
    case "bytes": control = <BytesControl key={resets} f={f} label={label} describe={describe} disabled={disabled} value={value} onChange={onChange} />; break;
    // An object the saved settings did not have goes back to absent once its last key is undone, so Undo returns the
    // draft to the saved value (no stray {} to count or send).
    case "object": control = <fieldset className="grid gap-4 rounded border p-3">{children(objectValue(value), path, (v) => onChange(p.tracked && p.saved == null && !Object.keys(objectValue(v)).length ? undefined : v), p.saved)}</fieldset>; break;
    case "keyed": {
      const records = objectValue(value);
      const managed = path === "library.versions";
      control = <div className="grid gap-3">{Object.entries(records).map(([key, record]) => <KeyedRecord key={key} {...p} name={key} records={records} managed={managed}>{children(objectValue(record), `${path}.${key}`, (v) => onChange({ ...records, [key]: v }), objectValue(p.saved)[key])}</KeyedRecord>)}{!managed ? <div className="flex gap-2"><Input aria-label={`New ${label} name`} placeholder="Name" value={newName} disabled={disabled} onChange={(e) => setNewName(e.target.value)} /><Button type="button" disabled={disabled || !newName.trim() || Object.hasOwn(records, newName.trim())} onClick={() => { onChange({ ...records, [newName.trim()]: {} }); setNewName(""); }}>{addLabel(f)}</Button>{newName && Object.hasOwn(records, newName.trim()) ? <p role="alert">That name already exists.</p> : null}</div> : null}</div>; break;
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
      control = <div className="grid gap-2">{rows.map((row, i) => <div key={i} className="rounded border p-2">{f.type === "object_list" ? children(objectValue(row), `${path}.${i}`, (v) => update(i, v), Array.isArray(p.saved) ? p.saved[i] : undefined) : f.enum ? <select aria-label={`${label} ${i + 1}`} {...rowDescribe(i)} className={SELECT_CLASS} disabled={disabled} value={String(row)} onChange={(e) => update(i, e.target.value)}><option value="">Choose…</option>{f.enum.map((v) => <option key={v}>{v}</option>)}</select> : <Input aria-label={`${label} ${i + 1}`} {...rowDescribe(i)} value={String(row)} disabled={disabled} onChange={(e) => update(i, e.target.value)} />}{p.errors?.[`${path}.${i}`] ? <p id={`${id}-${i}-error`} role="alert">{p.errors[`${path}.${i}`]}</p> : null}<Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(rows.filter((_, n) => n !== i))}>Remove {i + 1}</Button>{f.type === "object_list" ? <><Button type="button" variant="ghost" disabled={disabled || i === 0} onClick={() => move(i, -1)}>Move up</Button><Button type="button" variant="ghost" disabled={disabled || i === rows.length - 1} onClick={() => move(i, 1)}>Move down</Button></> : null}</div>)}<Button type="button" variant="outline" disabled={disabled} onClick={() => onChange([...rows, f.type === "object_list" ? {} : ""])}>{addLabel(f)}</Button></div>; break;
    }
    case "map": case "string_map": {
      const entries = objectValue(value);
      control = <div className="grid gap-2">{Object.entries(entries).map(([key, v]) => <div key={key} className="flex gap-2"><Input aria-label={`${label} key ${key}`} defaultValue={key} disabled={disabled} onBlur={(e) => { const name = e.target.value; if (name !== key && !Object.hasOwn(entries, name)) onChange({ ...changeField(entries, key, undefined), [name]: v }); }} />{f.type === "map" ? <NumberInput aria-label={`${label} value ${key}`} value={String(v)} disabled={disabled} onChange={(n) => onChange({ ...entries, [key]: Number(n) })} /> : <Input aria-label={`${label} value ${key}`} value={String(v)} disabled={disabled} onChange={(e) => onChange({ ...entries, [key]: e.target.value })} />}<Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(changeField(entries, key, undefined))}>Remove {key}</Button></div>)}<Button type="button" variant="outline" disabled={disabled || Object.hasOwn(entries, "")} onClick={() => onChange({ ...entries, "": f.type === "map" ? 0 : "" })}>{addLabel(f)}</Button></div>; break;
    }
    default: control = input();
  }
  const leaf = !["object", "keyed"].includes(f.type);
  const changed = p.tracked && leaf && !same(value, p.saved);
  return <div data-field={path} data-changed={changed || undefined} className="grid min-w-0 gap-1"><div className="flex items-start justify-between gap-x-2">{f.type === "object" && p.panelTitle === label ? null : <span className="min-w-0 text-sm font-medium [overflow-wrap:anywhere]">{label}</span>}{leaf && value !== undefined && value !== null ? <Button type="button" size="xs" variant="ghost" className="shrink-0" disabled={disabled} onClick={clear} aria-label={`Reset ${label}`} title={f.default ? `Back to the default (${f.default})` : "Back to the default"}>Reset</Button> : null}</div>{control}{f.unit && f.type !== "bytes" ? <span className="text-xs">{f.unit}</span> : null}{error ? <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{error}</p> : null}{f.help ? <p id={`${id}-help`} className="text-xs text-muted-foreground">{f.help}</p> : null}{changed ? <ChangedMark name={label} disabled={disabled} onUndo={() => { setResets((n) => n + 1); onChange(structuredClone(p.saved)); }} /> : null}</div>;
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
  const libraryName = useLibraryName();
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
  const removed = p.managed && !!p.libraryKeys && !p.libraryKeys.includes(name);
  return <details className="rounded border p-3" open={unfolded || undefined} data-removed={removed || undefined}><summary>{p.managed ? <><span>{libraryName(name)}</span>{libraryName(name) !== name ? <> <small className="font-mono text-muted-foreground">{name}</small></> : null}{removed ? <> <Badge tone="warning">library removed</Badge></> : null}</> : name}</summary>{removed ? <div className="my-2 flex flex-wrap items-center gap-2 text-sm"><span>No library has this key any more. Remove the row; Save writes the change.</span><Button type="button" variant="outline" size="sm" disabled={disabled} onClick={() => p.onChange(changeField(records, name, undefined))}>Remove {name}</Button></div> : null}{!p.managed ? <>
    <label>Rename<Input aria-label={`Rename ${name}`} value={nameDraft} disabled={disabled} onChange={(e) => setName(e.target.value)} /></label>
    {renaming && (stored.length > 0 || maskedURL) ? <div className="grid gap-2"><p>Renaming needs its credentials again</p>{credentials.map((s) => <label key={s.path}>{s.label}{draft.actions[s.path] === "clear" ? <span> — will be removed</span> : <Input aria-label={`Rename ${s.label}`} type="password" required value={draft.values[s.path] ?? ""} disabled={disabled} onChange={(e) => p.onSecrets?.({ actions: { ...draft.actions, [s.path]: "replace" }, values: { ...draft.values, [s.path]: e.target.value } })} />}</label>)}{maskedURL ? <label>Full URL<Input aria-label="Rename URL" required value={url} disabled={disabled} onChange={(e) => setURL(e.target.value)} /></label> : null}</div> : null}
    {invalid ? <p role="alert">{invalid}</p> : null}
    <Button type="button" variant="outline" disabled={disabled || !renaming || !!invalid || !ready} onClick={() => { moveDraft(`${path}.${nextName}.`); p.onChange({ ...changeField(records, name, undefined), [nextName]: maskedURL ? { ...record, url } : record }); }}>Rename {name}</Button>
  </> : null}{p.children}{path === "notify.targets" && !renaming ? <NotifyTestButton name={name} disabled={disabled} /> : null}{!p.managed ? <Button type="button" variant="outline" disabled={disabled} onClick={() => { moveDraft(); p.onChange(changeField(records, name, undefined)); }}>Remove {name}</Button> : null}</details>;
}

/** "Add folder", "Add destination": the add button names one of the things the list holds. */
const ITEM: Record<string, string> = {
  "": "download client", vpn_ips: "VPN exit IP", vpn_cidrs: "address range", require_vpn_for: "traffic type", roots: "folder",
  scan_roots: "folder", sources: "metadata service", allow_hosts: "host", targets: "destination", views: "view", events: "event",
  versions: "library", orders: "watch order", franchises: "franchise", trackers: "tracker",
};
export const addLabel = (f: SchemaField) => ITEM[f.name] ? `Add ${ITEM[f.name]}` : `Add ${f.label || f.name}`;

// Durations in words: the select and the custom amount show "10 minutes"; what is written stays a Go duration ("10m";
// days as hours, which Go reads). A stored value is never rewritten until it is edited.
type DurationUnit = "seconds" | "minutes" | "hours" | "days";
const DURATION_UNITS: Record<DurationUnit, number> = { seconds: 1, minutes: 60, hours: 3600, days: 86400 };
// Days from two days up: 24h reads "24 hours", 168h "7 days".
const bestDurationUnit = (seconds: number): DurationUnit => (["days", "hours", "minutes"] as const).find((u) => seconds % DURATION_UNITS[u] === 0 && (u !== "days" || seconds >= 172800)) ?? "seconds";
/** The exact Go duration of an amount in a unit: 1.25 hours is "1h15m", 2 days "48h", 1.5 seconds "1.5s". */
export function goDuration(n: number, unit: DurationUnit): string {
  let rest = Math.round(n * DURATION_UNITS[unit] * 1000) / 1000;
  const h = Math.floor(rest / 3600); rest -= h * 3600;
  const m = Math.floor(rest / 60); rest = Math.round((rest - m * 60) * 1000) / 1000;
  return `${h ? `${h}h` : ""}${m ? `${m}m` : ""}${rest || (!h && !m) ? `${rest}s` : ""}`;
}
/** A typed amount: a number of at least 0, else undefined (nothing is written while it is empty or half typed). */
const amountOf = (text: string): number | undefined => {
  const t = text.trim();
  if (!/^\d*\.?\d+$|^\d+\.$/.test(t)) return undefined;
  const n = Number(t);
  return Number.isFinite(n) ? n : undefined;
};
const shown = (n: number) => String(Number(n.toFixed(3)));

type Describe = { "aria-describedby"?: string; "aria-invalid"?: boolean };
/** A duration: Default, Off (when allowed), presets in words, or Custom as an amount and a unit. While the amount is
 * edited, Custom and the unit stay as shown; the amount is the typed text, and only a valid amount writes (the exact Go
 * duration of amount × unit). The unit changes only through its selector (the amount is rescaled, nothing written), and
 * Custom is left only through the preset selector. */
function DurationControl({ f, label, describe, path, disabled, value, onChange, raw }: { f: SchemaField; label: string; describe?: Describe; path: string; disabled?: boolean; value: unknown; onChange: (v: unknown) => void; raw: () => ReactNode }) {
  const v = typeof value === "string" ? value : "";
  const seconds = v ? parseDuration(v) : undefined;
  const allowed = durationPresets(f, seconds).filter(([, n]) => n >= (f.min ?? 0) && n <= (f.max ?? Infinity));
  const unsetIsOff = f.zero_allowed && (!f.default || parseDuration(f.default) === 0);
  const preset = allowed.find(([, n]) => n === seconds)?.[0];
  // A stored 0 on a field without an Off means the default to the server: it shows as Default (and stays stored).
  const zeroIsDefault = seconds === 0 && !f.zero_allowed;
  const [custom, setCustom] = useState(false);
  const [unit, setUnit] = useState<DurationUnit>(() => bestDurationUnit(seconds || parseDuration(f.default ?? "") || f.min || 60));
  const [text, setText] = useState(() => seconds === undefined ? "" : shown(seconds / DURATION_UNITS[unit]));
  const selected = custom ? "custom" : seconds === 0 && f.zero_allowed ? "0s" : zeroIsDefault ? "" : preset ?? (v ? "custom" : unsetIsOff ? "0s" : "");
  const editor = selected !== "custom" ? null : v && seconds === undefined ? raw() : <div className="flex gap-2">
    <Input aria-label={`${label} amount`} {...describe} data-field={path} inputMode="decimal" disabled={disabled} value={text} onChange={(e) => { setText(e.target.value); setCustom(true); const n = amountOf(e.target.value); if (n !== undefined) onChange(goDuration(n, unit)); }} />
    <select aria-label={`${label} unit`} className={SELECT_CLASS} disabled={disabled} value={unit} onChange={(e) => { const next = e.target.value as DurationUnit; setUnit(next); setCustom(true); if (seconds !== undefined) setText(shown(seconds / DURATION_UNITS[next])); }}>{Object.keys(DURATION_UNITS).map((u) => <option key={u} value={u}>{u}</option>)}</select>
  </div>;
  return <><select aria-label={label} {...describe} className={SELECT_CLASS} disabled={disabled} value={selected} onChange={(e) => {
    const choice = e.target.value;
    setCustom(choice === "custom");
    if (choice === "custom") { setText(seconds === undefined ? "" : shown(seconds / DURATION_UNITS[unit])); return; }
    onChange(choice === "0s" && unsetIsOff ? undefined : choice || undefined);
  }}><option value="">{`Default${f.default ? ` (${durationDefault(f)})` : ""}`}</option>{f.zero_allowed ? <option value="0s">Off</option> : null}{allowed.map(([s, n]) => <option key={s} value={s}>{durationWords(n)}</option>)}<option value="custom">Custom</option></select>{editor}</>;
}

/** A byte size as an amount and a unit. Stored values show without being rewritten; while the amount is edited the unit
 * stays as shown and only a valid amount writes (amount × unit, in bytes). A stored 0 is the default to the server, so
 * it shows the default's placeholder (and stays stored). */
function BytesControl({ f, label, describe, disabled, value, onChange }: { f: SchemaField; label: string; describe?: Describe; disabled?: boolean; value: unknown; onChange: (v: unknown) => void }) {
  const match = typeof value === "string" ? value.match(/^([\d.]+)\s*(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$/) : null;
  const bytes = typeof value === "number" ? value : match ? Number(match[1]) * BYTE_UNITS[match[2] || "B"] : undefined;
  const defaultMatch = f.default?.match(/^([\d.]+)\s*(KB|MB|GB|TB|KiB|MiB|GiB|TiB)$/);
  const [unit, setUnit] = useState(() => match?.[2] && match[2] !== "B" ? match[2] : bytes ? bestByteUnit(bytes) : defaultMatch?.[2] ?? "MB");
  const [text, setText] = useState(() => bytes === undefined || bytes === 0 ? "" : shown(bytes / BYTE_UNITS[unit]));
  const per = BYTE_UNITS[unit];
  const rate = f.unit === "bytes/s" ? "/s" : "";
  return <div className="flex gap-2">
    <Input aria-label={label} {...describe} inputMode="decimal" placeholder={defaultMatch ? `${defaultMatch[1]} ${defaultMatch[2]}${rate}` : f.default} disabled={disabled} value={text} onChange={(e) => { setText(e.target.value); const n = amountOf(e.target.value); if (n !== undefined) onChange(Math.round(n * per)); }} />
    <select aria-label={`${label} unit`} className={SELECT_CLASS} disabled={disabled} value={unit} onChange={(e) => { setUnit(e.target.value); if (bytes) setText(shown(bytes / BYTE_UNITS[e.target.value])); }}>{Object.keys(BYTE_UNITS).map((u) => <option key={u} value={u}>{u}{rate}</option>)}</select>
  </div>;
}
/** 600 → "10 minutes", 5400 → "1 hour 30 minutes", 86400 → "24 hours", 604800 → "7 days". */
export function durationWords(seconds: number): string {
  if (seconds === 0) return "0 seconds";
  const parts: string[] = [];
  let rest = seconds;
  for (const [unit, per] of [["day", 86400], ["hour", 3600], ["minute", 60]] as const) {
    const n = unit === "day" && seconds < 172800 ? 0 : Math.floor(rest / per);
    if (n) { parts.push(plural(n, unit)); rest -= n * per; }
  }
  if (rest) parts.push(plural(Number(rest.toFixed(3)), "second"));
  return parts.join(" ");
}
/** The default in words: "6 minutes"; a zero default of a field with Off is "off"; a non-duration ("3 × interval") as written. */
function durationDefault(f: SchemaField): string {
  const n = parseDuration(f.default ?? "");
  return n === undefined ? f.default ?? "" : n === 0 && f.zero_allowed ? "off" : durationWords(n);
}

// Byte sizes: decimal and binary units, as policy.ByteSize reads them. A stored count shows in the unit that gives the
// smallest whole number (2147483648 is 2 GiB, 25000000 is 25 MB).
const BYTE_UNITS: Record<string, number> = { B: 1, KB: 1e3, MB: 1e6, GB: 1e9, TB: 1e12, KiB: 1024, MiB: 1048576, GiB: 1073741824, TiB: 1099511627776 };
const bestByteUnit = (bytes: number) => Object.entries(BYTE_UNITS).filter(([, per]) => bytes % per === 0).sort((a, b) => b[1] - a[1])[0]?.[0] ?? "B";
