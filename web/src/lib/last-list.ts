// Where a title removal returns to (docs/design/ui.md "Recovery actions"): the list the person last opened (Movies,
// Series or Anime, with its filters and sort), else the title's own list. Kept for this tab only: a reload starts
// from the title's own list.
let last: string | undefined;

/** Remembers a list page as it is now (its path and query). */
export function rememberList(path: string) {
  last = path;
}

/** The list to go back to after removing a title of kind (anime: the Anime list). */
export function listToReturnTo(kind?: string, anime?: boolean): string {
  if (last) return last;
  return anime ? "/anime" : kind === "series" ? "/series" : "/movies";
}
