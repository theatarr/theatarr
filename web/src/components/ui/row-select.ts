// A table's row selection (docs/design/ui.md "Library views"): the chosen row ids and how to change them. The page keeps
// the state; DataTable draws the select column and the select-all box from it.

export type RowSelection<K extends string | number = number> = {
  chosen: ReadonlySet<K>;
  toggle: (id: K, on: boolean) => void;
  all: (on: boolean) => void;
};
