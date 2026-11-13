// A pager for a list read a page at a time (docs/design/ui.md "Library views"): "Previous · 1–50 of 312 · Next".
// Nothing when the list is empty.
import { Button } from "@/components/ui/button";
import { fmtNumber } from "@/lib/format";

export function Pager({ offset, limit, total, onPage }: { offset: number; limit: number; total: number; onPage: (offset: number) => void }) {
  if (total <= 0) return null;
  const last = Math.min(offset + limit, total);
  return (
    <nav aria-label="Pages" className="flex items-center justify-center gap-3 text-sm" data-testid="pager">
      <Button variant="outline" size="sm" disabled={offset <= 0} onClick={() => onPage(Math.max(0, offset - limit))}>
        Previous
      </Button>
      <span className="tabular-nums text-muted-foreground" data-testid="pager-range">
        {fmtNumber(offset + 1)}–{fmtNumber(last)} of {fmtNumber(total)}
      </span>
      <Button variant="outline" size="sm" disabled={last >= total} onClick={() => onPage(offset + limit)}>
        Next
      </Button>
    </nav>
  );
}
