// Size limits in the profile editor (docs/design/settings.md, "Profiles, presets and formats"): min, preferred and max per
// quality in MB per minute of runtime, editing the profile's sizes map, the same value the raw "Size limits" JSON editor
// edits (it stays in the raw profile editor as the fallback). One row per quality the profile allows (a group's members
// each get their own row, as sizes are keyed by quality), plus any quality that already has a limit.
//
// The engine (internal/decide/build.go) reads a quality with no entry as the arr's default definition
// (size-defaults.json, kept equal to the engine's by a Go test); an entry replaces all three: min as given, max 0 as no
// limit, preferred 0 as no preferred size unless preferred_zero keeps the arr's preferred size of 0. The form shows the
// effective values: a blank field is the default (its placeholder says which), "none" is no limit.
import { useEffect, useId, useState } from "react";
import { Link } from "react-router";
import { Input } from "@/components/ui/input";
import { object, type Config, type Quality } from "../quality/fields";
import defaults from "./size-defaults.json";

type Limit = { min?: number; preferred?: number; max?: number; preferred_zero?: boolean };
type Field = "min" | "preferred" | "max";
/** Effective limits as the engine applies them; null is unlimited (no max, no preferred size). */
type Effective = { min: number; preferred: number | null; max: number | null };
const FIELDS = [["min", "Min"], ["preferred", "Preferred"], ["max", "Max"]] as const;

/** The arr's default definition for a quality ([min, max, preferred] in size-defaults.json). */
export function sizeDefault(flavour: string, quality: string): Effective {
  const row = (defaults as Record<string, Record<string, (number | null)[]>>)[flavour]?.[quality];
  return row ? { min: row[0] ?? 0, max: row[1], preferred: row[2] } : { min: 0, max: null, preferred: null };
}

function effective(limit: Limit | undefined, def: Effective): Effective {
  if (!limit) return def;
  return { min: limit.min ?? 0, max: limit.max ? limit.max : null, preferred: limit.preferred || limit.preferred_zero ? limit.preferred ?? 0 : null };
}

const same = (a: Effective, b: Effective) => a.min === b.min && a.max === b.max && a.preferred === b.preferred;

/** The qualities a profile allows, groups flattened to their members, best first. */
export function sizeRows(config: Config): string[] {
  const allowed = ((config.qualities ?? []) as Quality[]).flatMap((q) => typeof q === "string" ? [q] : q.qualities);
  const set = Object.keys(object(config.sizes)).filter((q) => !allowed.includes(q));
  return [...allowed, ...set.sort()];
}

/** The sizes map with one field set (undefined: back to the default for that field). The other fields keep their
 * effective values; an entry equal to the defaults is removed, as no entry means the defaults. */
export function setSize(config: Config, flavour: string, quality: string, field: Field, value: number | null | undefined): Config {
  const sizes = object(config.sizes) as Record<string, Limit>;
  const def = sizeDefault(flavour, quality);
  const next = { ...effective(sizes[quality], def), [field]: value === undefined ? def[field] : value } as Effective;
  const out = { ...sizes };
  if (same(next, def)) delete out[quality];
  else out[quality] = { min: next.min, preferred: next.preferred ?? 0, max: next.max ?? 0, ...(next.preferred === 0 ? { preferred_zero: true } : {}) };
  return { ...config, sizes: out };
}

const show = (v: number | null) => v === null ? "none" : String(v);

/** A typed value: "" is the default, "none" no limit (not for min), else a number (a decimal comma is a point); max 0
 * is no limit as in the engine. false: not a value. */
export function parseSize(field: Field, text: string): number | null | undefined | false {
  const t = text.trim().toLowerCase().replace(",", ".");
  if (t === "") return undefined;
  if (t === "none") return field === "min" ? false : null;
  if (!/^\d*\.?\d+$|^\d+\.$/.test(t)) return false;
  const n = Number(t);
  return field === "max" && n === 0 ? null : n;
}

/** The arr flavours whose defaults apply to a profile: one per kind of library using it, as the engine builds it once
 * per flavour (a series library is Sonarr, any other Radarr). */
export function profileFlavours(kinds: string[]): string[] {
  return [...new Set(kinds.map((k) => k === "series" ? "sonarr" : "radarr"))].sort();
}

/** Size limits for a profile used by `flavours` (none: a new or unused profile, so the user picks whose defaults to
 * edit against, not saved; two: read-only, as one sizes map cannot hold both flavours' defaults). onInvalid reports a
 * typed value that is not a size, which blocks saving. */
export function SizeLimits({ config, flavours, rawHref, onChange, onInvalid, readOnly }: {
  config: Config; flavours: string[]; rawHref?: string; onChange: (c: Config) => void; onInvalid?: (invalid: boolean) => void; readOnly: boolean;
}) {
  const sizes = object(config.sizes) as Record<string, Limit>;
  const rows = sizeRows(config);
  const [picked, setPicked] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const invalid = Object.entries(drafts).some(([k, t]) => parseSize(k.split("\n")[1] as Field, t) === false);
  useEffect(() => { onInvalid?.(invalid); }, [invalid, onInvalid]);
  const name = useId();
  if (!rows.length) return <p className="text-sm text-muted-foreground">Allow a quality to set its size limits.</p>;
  const mixed = flavours.length > 1;
  const flavour = flavours.length === 1 ? flavours[0] : mixed ? "" : picked;
  const locked = readOnly || mixed;
  return <div className="space-y-2" data-testid="size-limits">
    {mixed ? <p className="text-[13px] text-[var(--warn-ink)]" data-testid="sizes-mixed">Used by movie and series libraries, whose default sizes differ: edit sizes in the {rawHref ? <Link className="underline" to={rawHref}>raw editor</Link> : "raw editor"}, or give each its own profile.</p> : null}
    {!flavours.length ? <fieldset className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm" data-testid="sizes-flavour">
      <legend className="sr-only">Defaults for</legend><span aria-hidden className="text-muted-foreground">Defaults for:</span>
      {[["radarr", "Movies"], ["sonarr", "Series"]].map(([f, label]) => <label key={f} className="flex items-center gap-1.5">
        <input type="radio" name={name} className="size-4 accent-[var(--accent)]" checked={picked === f} disabled={readOnly} onChange={() => setPicked(f)} />{label}
      </label>)}
      {!picked ? <span className="basis-full text-xs text-muted-foreground">No library uses this profile yet: pick whose arr defaults the sizes start from.</span> : null}
    </fieldset> : null}
    {flavours.length || picked ? <>
      <div className="hidden grid-cols-[minmax(0,1fr)_repeat(3,6.5rem)] gap-2 px-1 text-xs text-muted-foreground sm:grid" aria-hidden>
        <span>Quality</span>{FIELDS.map(([, label]) => <span key={label}>{label}</span>)}
      </div>
      <ul className="divide-y divide-[var(--line)] rounded-lg border border-[var(--line)]">
        {rows.map((q) => {
          const def = sizeDefault(flavour, q);
          const eff = effective(sizes[q], def);
          return <li key={q} className="grid grid-cols-3 items-center gap-2 px-3 py-2 sm:grid-cols-[minmax(0,1fr)_repeat(3,6.5rem)] sm:px-1" data-quality={q}>
            <span className="col-span-3 text-sm [overflow-wrap:anywhere] sm:col-span-1">{q}</span>
            {FIELDS.map(([field, label]) => {
              const k = `${q}\n${field}`;
              const draft = drafts[k];
              const bad = draft !== undefined && parseSize(field, draft) === false;
              // Without a flavour (mixed) the defaults are unknown: an entry shows its values, no entry "default".
              const shown = !flavour ? (sizes[q] ? show(eff[field]) : "") : eff[field] === def[field] ? "" : show(eff[field]);
              return <label key={field} className="flex flex-col gap-0.5 text-xs text-muted-foreground">
                <span className="sm:sr-only">{label}</span>
                <Input type="text" inputMode="decimal" className="h-8" disabled={locked} aria-label={`${label} size for ${q}`}
                  placeholder={flavour ? `${show(def[field])} default` : "default"} value={draft ?? shown} aria-invalid={bad || undefined}
                  onChange={(e) => {
                    const text = e.target.value;
                    setDrafts((d) => ({ ...d, [k]: text }));
                    const v = parseSize(field, text);
                    if (v !== false) onChange(setSize(config, flavour, q, field, v));
                  }}
                  onBlur={() => { if (!bad) setDrafts(({ [k]: _, ...rest }) => rest); }} />
              </label>;
            })}
          </li>;
        })}
      </ul>
      {invalid ? <p role="alert" className="text-xs text-destructive">A size is a number (a decimal point or comma is fine), blank for the default, or none.</p> : null}
    </> : null}
  </div>;
}
