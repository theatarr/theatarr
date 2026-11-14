// A sortable table header (docs/design/ui.md "Library views"): the header cell with a button that asks for the sort, an
// arrow on the column the table is sorted by and aria-sort for screen readers. Without onSort it is a plain header.
import { ArrowDown, ArrowUp } from "lucide-react";
import type { ReactNode, ThHTMLAttributes } from "react";
import { TableHead } from "@/components/ui/table";

export type SortDir = "asc" | "desc";

type Props = ThHTMLAttributes<HTMLTableCellElement> & {
  /** The direction when the table is sorted by this column; undefined when it is not. */
  dir?: SortDir;
  onSort?: () => void;
  /** Names the button "Sort by <sortLabel>"; without it the header's text names it. */
  sortLabel?: string;
  children: ReactNode;
};

export function SortHead({ dir, onSort, sortLabel, children, ...th }: Props) {
  if (!onSort) return <TableHead {...th}>{children}</TableHead>;
  return (
    <TableHead aria-sort={dir ? (dir === "desc" ? "descending" : "ascending") : undefined} {...th}>
      <button type="button" onClick={onSort} aria-label={sortLabel ? `Sort by ${sortLabel}` : undefined} className="inline-flex min-w-0 items-center gap-1 text-left [letter-spacing:inherit] [text-transform:inherit] hover:text-foreground">
        {children}
        {dir ? (dir === "desc" ? <ArrowDown className="size-3 shrink-0" aria-hidden /> : <ArrowUp className="size-3 shrink-0" aria-hidden />) : null}
      </button>
    </TableHead>
  );
}
