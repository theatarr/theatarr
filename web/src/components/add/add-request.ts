// The add sheet's POST /titles body. A title already in the library (opened from its title page, a library search
// hit, or a TMDb hit the library has) is sent by title_id, so a series stored with only a TVDB id, or a title whose
// TMDb id the title row lacks, gets its versions too. A new title is sent by kind and TMDb id, with the name, year and
// anime flag the sheet shows.
import type { AddOptions, AddRequest, AddTarget } from "@/api/add";

export type AddChoice = {
  /** Every version to have on: the new ticks and the ones already on. */
  versions: string[];
  seasons?: { season: number; monitored: boolean }[];
  search: boolean;
  anime: boolean;
  /** A new series' monitor preset; unset sends the seasons as picked (today's request). */
  monitor?: NonNullable<AddRequest["monitor"]>;
  /** A new series: false leaves seasons that appear later unmonitored; unset or true sends nothing. */
  monitorNew?: boolean;
  /** A new series' type; unset sends nothing (standard, or anime for an anime title, applies). */
  seriesType?: NonNullable<AddRequest["series_type"]>;
  /** A movie's minimum availability for the libraries added now; unset ("Library default") sends nothing. */
  minimumAvailability?: NonNullable<AddRequest["minimum_availability"]>;
};

/** The library title the sheet adds to, if any: the target's, else the one GET /add/options found by TMDb id. */
export function libraryTitleId(target: AddTarget, opts?: Pick<AddOptions, "title_id">): number | undefined {
  return target.title_id ?? opts?.title_id ?? undefined;
}

export function addRequest(target: AddTarget, opts: Pick<AddOptions, "kind" | "title_id">, c: AddChoice): AddRequest {
  const kind = opts.kind === "series" ? "series" : "movie";
  const titleId = libraryTitleId(target, opts);
  // A movie's choice goes with the libraries the request adds, in the library or not; left at "Library default" it
  // adds no key.
  const movie: Partial<AddRequest> = kind === "movie" && c.minimumAvailability ? { minimum_availability: c.minimumAvailability } : {};
  const common = { versions: c.versions, seasons: c.monitor && kind === "series" && titleId === undefined ? undefined : c.seasons, search: c.search, ...movie };
  if (titleId !== undefined) return { title_id: titleId, kind, ...common };
  // The preset, the new-seasons choice and the type are a new series' only: the request names the libraries the
  // title already has too, and they would be rewritten; the type is the one the series has. Each left at its default adds no key, so the body is the one sent before
  // these choices existed. monitor_new none alone would leave the seasons of the first refresh unmonitored as well
  // (the new series has no episodes yet, so each of its seasons appears later), so without a preset or season picks
  // it goes with monitor all: the first seasons are monitored and only the ones after them are not.
  const later = c.monitorNew === false;
  const monitor = c.monitor ?? (later && !c.seasons?.length ? "all" : undefined);
  const series: Partial<AddRequest> = kind === "series" ? {
    ...(monitor ? { monitor } : {}),
    ...(later ? { monitor_new: "none" } : {}),
    ...(c.seriesType ? { series_type: c.seriesType } : {}),
  } : {};
  return { kind, tmdb_id: target.tmdb_id || undefined, title: target.name, year: target.year, anime: c.anime, ...common, ...series };
}
