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
};

/** The library title the sheet adds to, if any: the target's, else the one GET /add/options found by TMDb id. */
export function libraryTitleId(target: AddTarget, opts?: Pick<AddOptions, "title_id">): number | undefined {
  return target.title_id ?? opts?.title_id ?? undefined;
}

export function addRequest(target: AddTarget, opts: Pick<AddOptions, "kind" | "title_id">, c: AddChoice): AddRequest {
  const kind = opts.kind === "series" ? "series" : "movie";
  const titleId = libraryTitleId(target, opts);
  const common = { versions: c.versions, seasons: c.seasons, search: c.search };
  if (titleId !== undefined) return { title_id: titleId, kind, ...common };
  return { kind, tmdb_id: target.tmdb_id || undefined, title: target.name, year: target.year, anime: c.anime, ...common };
}
