import { useEffect, useRef, useState, type ComponentProps, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, SELECT_CLASS } from "@/components/settings/parts";

export type Config = Record<string, unknown>;
export type Quality = string | { group: string; qualities: string[] };
export type Specification = { name: string; implementation: string; negate: boolean; required: boolean; fields: Record<string, unknown> | { name: string; value: unknown }[] };
export type FormatJSON = { name: string; includeCustomFormatWhenRenaming: boolean; specifications: Specification[]; trash_id?: string; [key: string]: unknown };
export type Implementation = { implementation: string; flavours: string[]; fields: { name: string; type: string; options?: Record<string, { name: string; value: number }[]> }[] };
export const text = (v: unknown) => typeof v === "string" ? v : "";
export const object = (v: unknown): Config => v !== null && typeof v === "object" && !Array.isArray(v) ? v as Config : {};
export const qualityName = (q: Quality) => typeof q === "string" ? q : q.group;
export const pretty = (v: unknown) => JSON.stringify(v, null, 2);

export function TextField({ label, value, onChange, type = "text" }: { label: string; value: unknown; onChange: (v: string) => void; type?: string }) {
  const shown = value === undefined || value === null ? "" : String(value);
  return <Field label={label}>{type === "number" ? <NumberInput value={shown} onChange={onChange} /> : <Input type={type} value={shown} onChange={(e) => onChange(e.target.value)} />}</Field>;
}

/** A number input that keeps what is typed while it is not a number yet ("" or a lone "-"), so a negative value can be
 * typed; onChange hears every value that is a number, and a field left blank becomes 0 on leaving it. */
export function NumberInput({ value, onChange, ...props }: Omit<ComponentProps<typeof Input>, "value" | "onChange" | "type"> & { value: string; onChange: (v: string) => void }) {
  const [draft, setDraft] = useState(value);
  const last = useRef(value);
  useEffect(() => { if (value !== last.current) { last.current = value; setDraft(value); } }, [value]);
  return <Input {...props} type="number" value={draft}
    onChange={(e) => { const v = e.target.value; setDraft(v); if (v.trim() !== "" && Number.isFinite(Number(v))) { last.current = String(Number(v)); onChange(v); } }}
    onBlur={(e) => {
      // Left blank (or a lone "-"): 0, as a plain number input gave.
      if (draft.trim() === "" || !Number.isFinite(Number(draft))) { last.current = "0"; onChange("0"); }
      setDraft(last.current); props.onBlur?.(e);
    }} />;
}
export function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />{label}</label>;
}
export function Select({ label, value, values, onChange, labels }: { label: string; value: string; values: string[]; onChange: (v: string) => void; labels?: Record<string, string> }) {
  return <Field label={label}><select className={SELECT_CLASS} value={value} onChange={(e) => onChange(e.target.value)}>{values.map((v) => <option key={v} value={v}>{labels?.[v] ?? (v || "Default")}</option>)}</select></Field>;
}
/** The arrs' Propers and Repacks choices, in their words; the stored values stay the keys. */
export const PROPERS = ["", "prefer_and_upgrade", "do_not_upgrade", "do_not_prefer"];
export const PROPERS_LABELS: Record<string, string> = { "": "Default (prefer and upgrade)", prefer_and_upgrade: "Prefer and upgrade", do_not_upgrade: "Do not upgrade automatically", do_not_prefer: "Do not prefer" };
export function JSONField({ label, value, onChange, validate }: { label: string; value: unknown; onChange: (v: unknown) => void; validate?: (v: unknown) => boolean }) {
  const serialized = pretty(value);
  const last = useRef(serialized);
  const input = useRef<HTMLTextAreaElement>(null);
  const [draft, setDraft] = useState(serialized);
  useEffect(() => { if (serialized !== last.current) { last.current = serialized; setDraft(serialized); input.current?.setCustomValidity(""); } }, [serialized]);
  return <Field label={label}><textarea ref={input} className="min-h-32 rounded border p-2 font-mono text-xs" value={draft} onChange={(e) => { setDraft(e.target.value); try { const next: unknown = JSON.parse(e.target.value); if (validate && !validate(next)) { e.target.setCustomValidity("Enter a custom format with a name and specifications containing an implementation and fields"); return; } last.current = pretty(next); onChange(next); e.target.setCustomValidity(""); } catch { e.target.setCustomValidity("Enter valid JSON"); } }} /></Field>;
}
export function FormGrid({ children }: { children: ReactNode }) { return <div className="grid gap-3 sm:grid-cols-2">{children}</div>; }

export function ProfileFields({ config: c, onChange }: { config: Config; onChange: (c: Config) => void }) {
  const [newQuality, setNewQuality] = useState("");
  const [members, setMembers] = useState("");
  const [newScore, setNewScore] = useState("");
  const set = (key: string, value: unknown) => onChange({ ...c, [key]: value });
  const allowed = (c.qualities ?? []) as Quality[];
  const order = (c.quality_order ?? c.qualities ?? []) as Quality[];
  const scores = object(c.formats);
  const reorder = (i: number, step: number) => { const next = [...order]; [next[i], next[i + step]] = [next[i + step], next[i]]; onChange({ ...c, quality_order: next, qualities: next.filter((q) => allowed.some((a) => qualityName(a) === qualityName(q))) }); };
  return <div className="space-y-4">
    <p className="text-sm text-muted-foreground">Qualities are ordered best first. A group treats its members as equal. Disabled qualities retain their position.</p>
    {order.map((q, i) => <div className="flex flex-wrap items-center gap-2 rounded border p-2" key={i}>
      <Toggle label={`Allow ${qualityName(q)}`} checked={allowed.some((a) => qualityName(a) === qualityName(q))} onChange={(on) => set("qualities", order.filter((x) => qualityName(x) === qualityName(q) ? on : allowed.some((a) => qualityName(a) === qualityName(x))))} />
      <span className="flex-1 text-sm">{typeof q === "string" ? q : `${q.group}: ${q.qualities.join(", ")}`}</span>
      {typeof q !== "string" ? <TextField label={`${q.group} members (comma separated)`} value={q.qualities.join(", ")} onChange={(v) => { const replacement = { ...q, qualities: v.split(",").map((s) => s.trim()).filter(Boolean) }; onChange({ ...c, quality_order: order.map((x, n) => n === i ? replacement : x), qualities: allowed.map((x) => qualityName(x) === q.group ? replacement : x) }); }} /> : null}
      <Button type="button" size="xs" disabled={i === 0} aria-label={`Move ${qualityName(q)} up`} onClick={() => reorder(i, -1)}>Up</Button>
      <Button type="button" size="xs" disabled={i === order.length - 1} aria-label={`Move ${qualityName(q)} down`} onClick={() => reorder(i, 1)}>Down</Button>
      <Button type="button" size="xs" onClick={() => { const next = order.filter((_, n) => n !== i); onChange({ ...c, quality_order: next, qualities: allowed.filter((a) => qualityName(a) !== qualityName(q)) }); }}>Remove</Button>
    </div>)}
    <FormGrid><TextField label="New quality or group name" value={newQuality} onChange={setNewQuality} /><TextField label="Group members (comma separated; empty for one quality)" value={members} onChange={setMembers} /></FormGrid>
    <Button type="button" variant="outline" disabled={!newQuality.trim()} onClick={() => { const q: Quality = members.trim() ? { group: newQuality.trim(), qualities: members.split(",").map((v) => v.trim()).filter(Boolean) } : newQuality.trim(); onChange({ ...c, quality_order: [...order, q], qualities: [...allowed, q] }); setNewQuality(""); setMembers(""); }}>Add quality or group</Button>
    <FormGrid>
      <Select label="Quality cutoff" value={text(c.cutoff)} values={["", ...allowed.map(qualityName)]} onChange={(v) => set("cutoff", v)} />
      <Toggle label="Upgrades allowed" checked={c.upgrades !== false} onChange={(v) => set("upgrades", v)} />
      <TextField label="Minimum format score" type="number" value={c.min_score ?? 0} onChange={(v) => set("min_score", Number(v))} />
      <TextField label="Cutoff format score (upgrade until)" type="number" value={c.cutoff_score ?? 0} onChange={(v) => set("cutoff_score", Number(v))} />
      <TextField label="Minimum score gain for an upgrade" type="number" value={c.min_upgrade_step ?? 1} onChange={(v) => set("min_upgrade_step", Number(v))} />
      <TextField label="Format set key" value={c.format_set} onChange={(v) => set("format_set", v)} />
      <TextField label="Language" value={c.language} onChange={(v) => set("language", v)} />
      <Select label="Propers and repacks" value={text(c.propers)} values={PROPERS} labels={PROPERS_LABELS} onChange={(v) => set("propers", v)} />
      <TextField label="Profile from arr (instance/profile; exclusive)" value={c.from_arr} onChange={(v) => set("from_arr", v)} />
    </FormGrid>
    <fieldset className="space-y-2"><legend className="font-medium">Custom format scores</legend>
      {Object.entries(scores).map(([key, score]) => <div className="flex items-end gap-2" key={key}><TextField label={key} type="number" value={score} onChange={(v) => set("formats", { ...scores, [key]: Number(v) })} /><Button type="button" onClick={() => { const next = { ...scores }; delete next[key]; set("formats", next); }}>Remove</Button></div>)}
      <TextField label="New score format key or TRaSH ID" value={newScore} onChange={setNewScore} /><Button type="button" disabled={!newScore.trim()} onClick={() => { set("formats", { ...scores, [newScore.trim()]: 0 }); setNewScore(""); }}>Add score</Button>
    </fieldset>
    <JSONField label="Size limits (MB/minute by quality)" value={c.sizes ?? {}} onChange={(v) => set("sizes", v)} />
  </div>;
}

export function FormatFields({ spec, onChange, schema, flavour }: { spec: FormatJSON; onChange: (v: FormatJSON) => void; schema: Implementation[]; flavour: string }) {
  const update = (i: number, patch: Partial<Specification>) => onChange({ ...spec, specifications: spec.specifications.map((s, n) => n === i ? { ...s, ...patch } : s) });
  return <div className="space-y-4">
    <TextField label="Format name" value={spec.name} onChange={(name) => onChange({ ...spec, name })} />
    <TextField label="TRaSH ID" value={spec.trash_id} onChange={(trash_id) => onChange({ ...spec, trash_id })} />
    <Toggle label="Include when renaming" checked={spec.includeCustomFormatWhenRenaming} onChange={(v) => onChange({ ...spec, includeCustomFormatWhenRenaming: v })} />
    {spec.specifications.map((s, i) => { const fields = Array.isArray(s.fields) ? Object.fromEntries(s.fields.map((f) => [f.name, f.value])) : s.fields; return <fieldset className="space-y-3 rounded border p-3" key={i}><legend>Specification {i + 1}</legend>
      <TextField label={`Specification ${i + 1} name`} value={s.name} onChange={(name) => update(i, { name })} />
      <Select label="Implementation" value={s.implementation} values={schema.filter((x) => !flavour || x.flavours.includes(flavour)).map((x) => x.implementation)} onChange={(implementation) => update(i, { implementation, fields: {} })} />
      <Toggle label="Negate" checked={s.negate} onChange={(negate) => update(i, { negate })} /><Toggle label="Required" checked={s.required} onChange={(required) => update(i, { required })} />
      {schema.find((x) => x.implementation === s.implementation)?.fields.map((f) => f.type === "boolean" ? <Toggle key={f.name} label={f.name} checked={fields[f.name] === true} onChange={(v) => update(i, { fields: { ...fields, [f.name]: v } })} /> : f.options?.[flavour] ? <Field key={f.name} label={f.name}><select className={SELECT_CLASS} value={String(fields[f.name] ?? "")} onChange={(e) => update(i, { fields: { ...fields, [f.name]: Number(e.target.value) } })}><option value="" disabled>Select a value</option>{f.options[flavour].map((o) => <option key={o.value} value={o.value}>{o.name}</option>)}</select></Field> : <TextField key={f.name} label={f.name} type={f.type === "string" ? "text" : "number"} value={fields[f.name]} onChange={(v) => update(i, { fields: { ...fields, [f.name]: f.type === "string" ? v : Number(v) } })} />)}
      <Button type="button" onClick={() => onChange({ ...spec, specifications: spec.specifications.filter((_, n) => n !== i) })}>Remove specification</Button>
    </fieldset>; })}
    <Button type="button" onClick={() => onChange({ ...spec, specifications: [...spec.specifications, { name: "Condition", implementation: "ReleaseTitleSpecification", negate: false, required: false, fields: { value: "" } }] })}>Add specification</Button>
    <JSONField label="Import arr custom format JSON (replaces this draft)" value={spec} validate={(v) => {
      const candidate = object(v);
      if (typeof candidate.name !== "string" || !Array.isArray(candidate.specifications)) return false;
      return candidate.specifications.every((value: unknown) => {
        const s = object(value);
        return typeof s.name === "string" && typeof s.implementation === "string" && s.fields !== null && typeof s.fields === "object" && (!Array.isArray(s.fields) || s.fields.every((field: unknown) => typeof object(field).name === "string"));
      });
    }} onChange={(v) => onChange(v as FormatJSON)} />
  </div>;
}
