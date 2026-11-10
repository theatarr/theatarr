// The title page's two per-title choices (docs/design/ui.md, Title page): a movie's minimum availability in each
// library (on its library card) and a series' type (among the About facts). Each shows the value in force; managers
// change it, and a change is stored as an explicit choice.
import { useMinimumAvailability, useSeriesType, type MinimumAvailability, type SeriesType } from "@/api/manage";
import { usePerms, type Version } from "@/api/library";
import { Select } from "@/components/ui/select";

export const MINIMUM_AVAILABILITY: readonly (readonly [MinimumAvailability, string])[] = [["announced", "Announced"], ["inCinemas", "In cinemas"], ["released", "Released"]];
export const SERIES_TYPES: readonly (readonly [SeriesType, string])[] = [["standard", "Standard"], ["daily", "Daily"], ["anime", "Anime"]];

const labelOf = (options: readonly (readonly [string, string])[], value: string) => options.find(([v]) => v === value)?.[1] ?? value;

/** A movie's minimum availability in the library of the card. An imported value outside the list (tba) is shown,
 *  and can be changed, but not picked. */
export function MinimumAvailabilityControl({ v, label }: { v: Version; label: string }) {
  const perms = usePerms();
  const save = useMinimumAvailability(v.title_id ?? 0);
  const value = v.minimum_availability ?? "";
  if (!perms.manage) return <>{labelOf(MINIMUM_AVAILABILITY, value)}</>;
  return (
    <>
      <Select className="w-auto" aria-label={`Minimum availability for ${label}`} value={value} disabled={save.isPending}
        onChange={(e) => save.mutate({ version: v.key ?? "", value: e.target.value as MinimumAvailability })} data-testid={`version-minimum-availability-${v.key}`}>
        {MINIMUM_AVAILABILITY.some(([o]) => o === value) ? null : <option value={value}>{value}</option>}
        {MINIMUM_AVAILABILITY.map(([o, l]) => <option key={o} value={o}>{l}</option>)}
      </Select>
      {save.error ? <p className="mt-1 text-xs text-[var(--bad-ink)]" role="alert">{(save.error as Error).message}</p> : null}
      {/* A copy taken out of the library still has its card; it takes no choice. */}
      {save.data?.changed === 0 ? <p className="mt-1 text-xs text-[var(--ink-3)]" role="status">Not changed: the movie is not in this library now.</p> : null}
    </>
  );
}

/** A series' type: the one value of the series, for every library. */
export function SeriesTypeControl({ titleId, value }: { titleId: number; value: string }) {
  const perms = usePerms();
  const save = useSeriesType(titleId);
  if (!perms.manage) return <>{labelOf(SERIES_TYPES, value)}</>;
  return (
    <>
      <Select className="w-auto" aria-label="Series type" value={value} disabled={save.isPending}
        onChange={(e) => save.mutate(e.target.value as SeriesType)} data-testid="title-series-type">
        {SERIES_TYPES.map(([o, l]) => <option key={o} value={o}>{l}</option>)}
      </Select>
      {save.error ? <p className="mt-1 text-xs text-[var(--bad-ink)]" role="alert">{(save.error as Error).message}</p> : null}
    </>
  );
}
