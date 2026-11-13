// A table's row selection (docs/design/ui.md "Library views"): the chosen row ids and how to change them. The page keeps
// the state; DataTable draws the select column and the select-all box from it.

export type RowSelection<K extends string | number = number> = {
  chosen: ReadonlySet<K>;
  toggle: (id: K, on: boolean) => void;
  all: (on: boolean) => void;
  /** Whether a row may be chosen; without it every row may. A row that may not gets a disabled box, and the
   *  select-all box counts and takes only the rows that may. */
  selectable?: (id: K) => boolean;
};
