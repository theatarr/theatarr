// The title page's two per-title choices (docs/design/ui.md, Title page): a movie's minimum availability in each
// library (on its library card) and a series' type (among the About facts). Each shows the value in force; managers
// change it, and a change is stored as an explicit choice.
import { useMinimumAvailability, useSeriesType, type MinimumAvailability, type SeriesType } from "@/api/manage";
import { usePerms, type Version } from "@/api/library";
import { Select } from "@/components/ui/select";

export const MINIMUM_AVAILABILITY: readonly (readonly [MinimumAvailability, string])[] = [["announced", "Announced"], ["inCinemas", "In cinemas"], ["released", "Released"]];
export const SERIES_TYPES: readonly (readonly [SeriesType, string])[] = [["standard", "Standard"], ["daily", "Daily"], ["anime", "Anime"]];

/** Under the Series type select (managers): it saves at once, with no Save button. */
export const SERIES_TYPE_HINT = "Saves at once for every library. Affects release matching and which libraries search this series.";

const labelOf = (options: readonly (readonly [string, string])[], value: string) => options.find(([v]) => v === value)?.[1] ?? value;

/** A movie's minimum availability in the library of the card. An imported value outside the list (tba) is shown,
 *  and can be changed, but not picked. Library default returns the copy to the library's own setting, and names the
 *  value it gives while it is in force. */
export function MinimumAvailabilityControl({ v, label }: { v: Version; label: string }) {
  const perms = usePerms();
  const save = useMinimumAvailability(v.title_id ?? 0);
  const value = v.minimum_availability ?? "";
  const isDefault = !!v.minimum_availability_default;
  if (!perms.manage) return <>{labelOf(MINIMUM_AVAILABILITY, value)}</>;
  return (
    <>
      <Select className="w-auto" aria-label={`Minimum availability for ${label}`} value={isDefault ? "default" : value} disabled={save.isPending}
        onChange={(e) => save.mutate({ version: v.key ?? "", value: e.target.value as MinimumAvailability })} data-testid={`version-minimum-availability-${v.key}`}>
        <option value="default">{isDefault ? `Library default (${labelOf(MINIMUM_AVAILABILITY, value)})` : "Library default"}</option>
        {isDefault || MINIMUM_AVAILABILITY.some(([o]) => o === value) ? null : <option value={value}>{value}</option>}
        {MINIMUM_AVAILABILITY.map(([o, l]) => <option key={o} value={o}>{l}</option>)}
      </Select>
      {save.error ? <p className="mt-1 text-xs text-[var(--bad-ink)]" role="alert">{(save.error as Error).message}</p> : null}
      {/* A copy taken out of the library still has its card; it takes no choice. */}
      {save.data?.changed === 0 ? <p className="mt-1 text-xs text-[var(--ink-3)]" role="status">Not changed: the movie is not in this library now.</p> : null}
    </>
  );
}

/** A series' type: the one value of the series, for every library. Acquisition reads a series as anime by its
 *  Anime mark or this type and leaves out a library whose anime rule does not fit; the copies the title page marks
 *  (anime_skip) are named under the control, so a change that stops a library's searches shows once it is saved. */
export function SeriesTypeControl({ titleId, value, anime = false, versions = [] }: { titleId: number; value: string; anime?: boolean; versions?: Version[] }) {
  const perms = usePerms();
  const save = useSeriesType(titleId);
  const skipped = versions.filter((v) => v.anime_skip).map((v) => v.label || v.key);
  const one = skipped.length === 1;
  const note = skipped.length === 0 ? null : (
    <p className="mt-1 text-xs text-[var(--ink-3)]" role="note" data-testid="series-type-skipped">
      Not searched in {skipped.join(", ")}:{" "}
      {anime || value === "anime"
        ? `the series counts as anime, and ${one ? "that library does" : "those libraries do"} not take anime.`
        : `${one ? "that library takes" : "those libraries take"} anime only, and the series does not count as anime.`}
    </p>
  );
  if (!perms.manage) return <>{labelOf(SERIES_TYPES, value)}{note}</>;
  return (
    <>
      <Select className="w-auto" aria-label="Series type" value={value} disabled={save.isPending}
        onChange={(e) => save.mutate(e.target.value as SeriesType)} data-testid="title-series-type">
        {SERIES_TYPES.map(([o, l]) => <option key={o} value={o}>{l}</option>)}
      </Select>
      <p className="mt-1 text-xs text-[var(--ink-3)]" data-testid="series-type-hint">{SERIES_TYPE_HINT}</p>
      {save.error ? <p className="mt-1 text-xs text-[var(--bad-ink)]" role="alert">{(save.error as Error).message}</p> : null}
      {note}
    </>
  );
}
