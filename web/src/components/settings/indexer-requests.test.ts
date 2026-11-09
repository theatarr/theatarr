// An indexer's requests by purpose on the Indexers page (docs/design/gateway.md "Indexer requests by purpose"): the
// summary line with its budget's daily use, the per-purpose table, and the row of the indexers list carrying it.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement as h, type ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { SettingsRecord } from "@/api/settings";
import { initialTests } from "@/lib/settings";
import { budget, dmm, zilean } from "./fixtures";
import { IndexerRequests, SourcesView } from "./sources";

function render(el: ReactElement): string {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(h(MemoryRouter, null, h(QueryClientProvider, { client: qc }, el)));
}

const noop = () => {};

const used: SettingsRecord = {
  ...dmm,
  requests: {
    today: 42,
    week: 1147,
    purposes: [
      { purpose: "interactive", today: 2, week: 2 },
      { purpose: "upgrade", today: 0, week: 5 },
      { purpose: "rss", today: 40, week: 1140 },
    ],
  },
};
const capped: SettingsRecord = { ...budget, usage: { ...budget.usage!, daily: 500, used_day: 140 } };

/** The text of each row of the requests table: purpose, today, 7 days. */
function rows(html: string): string[][] {
  const body = /<tbody>(.*?)<\/tbody>/.exec(html)?.[1] ?? "";
  return [...body.matchAll(/<tr[^>]*>(.*?)<\/tr>/g)].map((m) => [...m[1].matchAll(/<td[^>]*>(.*?)<\/td>/g)].map((c) => c[1]));
}

describe("indexer requests by purpose", () => {
  it("sums today and 7 days, with the budget's daily use when it has a daily cap", () => {
    const html = render(h(IndexerRequests, { r: used, budgets: [capped] }));
    expect(html).toContain("42 requests today · 1,147 in 7 days · budget 140/500 today");
    expect(rows(html)).toEqual([
      ["Interactive", "2", "2"],
      ["Upgrade", "0", "5"],
      ["RSS", "40", "1,140"],
    ]);
    expect(html).toContain('data-purpose="rss"');
    // Without a daily cap the budget counts nothing per day: no budget part.
    expect(render(h(IndexerRequests, { r: used, budgets: [budget] }))).toContain("1,147 in 7 days</summary>");
  });

  it("says when nothing was sent, and shows nothing without counts", () => {
    const quiet = render(h(IndexerRequests, { r: { ...dmm, requests: { today: 0, week: 0, purposes: [] } } }));
    expect(quiet).toContain("0 requests today · 0 in 7 days");
    expect(quiet).toContain("No requests in the last 7 days.");
    expect(quiet).not.toContain('data-testid="requests-table"');
    expect(render(h(IndexerRequests, { r: zilean }))).toBe("");
  });

  it("is in each indexer's row of the list", () => {
    const html = render(
      h(SourcesView, {
        entity: "indexers",
        records: [used, zilean],
        budgets: [capped],
        tests: initialTests,
        includeDisabled: false,
        onIncludeDisabled: noop,
        onTest: noop,
        onTestAll: noop,
        onEdit: noop,
        onMove: noop,
      }),
    );
    const row = /<tr[^>]*data-record="dmm".*?<\/tr>(?=<tr[^>]*data-record=)/.exec(html)?.[0] ?? "";
    expect(row).toContain('data-testid="indexer-requests"');
    expect(row).toContain("budget 140/500 today");
    expect(html).not.toMatch(/data-record="zilean".*data-testid="indexer-requests"/);
  });
});
