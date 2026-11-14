// A pager for a list read a page at a time (docs/design/ui.md "Library views"): "Previous · 1–50 of 312 · Next".
// Nothing when the list is empty; a list that fits one page gets its count alone, without the buttons. A page change
// returns to the top, so the new page is read from its first row (fetched or cached alike).
import { Button } from "@/components/ui/button";
import { fmtNumber } from "@/lib/format";

/** Back to the top of the page, where the list starts. */
function toTop() {
  if (window.scrollY > 0) window.scrollTo({ top: 0 });
}

export function Pager({ offset, limit, total, onPage }: { offset: number; limit: number; total: number; onPage: (offset: number) => void }) {
  if (total <= 0) return null;
  const go = (o: number) => {
    onPage(o);
    toTop();
  };
  if (offset <= 0 && total <= limit) {
    return <p className="text-center text-sm tabular-nums text-muted-foreground" data-testid="pager-count">All {fmtNumber(total)} shown</p>;
  }
  const last = Math.min(offset + limit, total);
  return (
    <nav aria-label="Pages" className="flex items-center justify-center gap-3 text-sm" data-testid="pager">
      <Button variant="outline" size="sm" disabled={offset <= 0} onClick={() => go(Math.max(0, offset - limit))}>
        Previous
      </Button>
      <span className="tabular-nums text-muted-foreground" data-testid="pager-range">
        {fmtNumber(offset + 1)}–{fmtNumber(last)} of {fmtNumber(total)}
      </span>
      <Button variant="outline" size="sm" disabled={last >= total} onClick={() => go(offset + limit)}>
        Next
      </Button>
    </nav>
  );
}
