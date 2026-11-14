// Where a title removal returns to (docs/design/ui.md "Recovery actions"): the list the person opened the title from
// (Movies, Series or Anime, with its filters and sort), else the title's own list. Every location change is noted
// (noteLocation, from the signed-in shell): entering a title page from a list keeps that list as its origin; entering
// it from anywhere else (Search, Home, another page) clears it. Kept for this tab only.
const LISTS = /^\/(movies|series|anime)$/;
const TITLE = /^\/title\/[^/]+$/;

let prev: string | undefined;
let origin: string | undefined;

const pathOf = (p: string) => p.split(/[?#]/)[0];

/** Notes the location now (path and query). */
export function noteLocation(path: string) {
  const here = pathOf(path);
  if (TITLE.test(here) && prev !== undefined) {
    const from = pathOf(prev);
    if (LISTS.test(from)) origin = prev;
    else if (from !== here) origin = undefined; // from Search, Home, another title: no list of origin
  } else if (TITLE.test(here)) {
    origin = undefined;
  }
  prev = path;
}

/** The list to go back to after removing a title of kind (anime: the Anime list). */
export function listToReturnTo(kind?: string, anime?: boolean): string {
  if (origin) return origin;
  return anime ? "/anime" : kind === "series" ? "/series" : "/movies";
}
