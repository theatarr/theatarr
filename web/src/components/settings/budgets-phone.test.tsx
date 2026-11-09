// @vitest-environment jsdom
// Request budgets and back-offs on a phone: each row stacks as labelled values, so no value cell is hidden below sm
// (a bare `hidden` with only an sm: or md: way back would drop it at 400 px).
import { render, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { BudgetsView } from "./sources";
import { RequestLimitsStatus } from "@/pages/admin/settings/network";
import { mockFetch, renderPage } from "@/test/page";
import type { SettingsRecord } from "@/api/settings";

const shownOnPhones = (row: HTMLElement) =>
  [...row.querySelectorAll("td")].every((td) => !td.className.split(/\s+/).includes("hidden"));
// Used by shows at every width: no breakpoint hides its cell or its column header.
const usedByEverywhere = (row: HTMLElement) => {
  const cell = within(row).getByText("Used by").closest("td") as HTMLElement;
  const head = [...document.querySelectorAll("th")].find((th) => th.textContent === "Used by") as HTMLElement;
  return [cell, head].every((el) => !el.className.split(/\s+/).some((c) => /^((sm|md|lg):)?hidden$/.test(c)));
};

it("shows every budget value on a phone, each with its label", () => {
  const rec = {
    key: "ad-api", config: { requests: 1, per: "second", burst: 5, daily: 5000 },
    usage: { used_hour: 30, used_day: 300, used_by: ["torrentio", "zilean"] },
  } as unknown as SettingsRecord;
  render(<BudgetsView records={[rec]} onEdit={() => {}} />);
  const row = document.querySelector<HTMLElement>('[data-record="ad-api"]')!;
  expect(shownOnPhones(row)).toBe(true);
  for (const text of ["Rate", "Caps", "This hour", "Today", "Used by"]) expect(within(row).getByText(text)).toBeTruthy();
  expect(row.textContent).toContain("5,000/day");
  expect(row.textContent).toContain("torrentio, zilean");
  expect(within(row).getByRole("button", { name: "Edit ad-api" })).toBeTruthy();
  expect(usedByEverywhere(row)).toBe(true);
});

it("shows every back-off and gateway budget value on a phone, the reason included", async () => {
  mockFetch([{
    method: "GET", path: "/gateway", reply: {
      egress: { vpn_ok: true },
      backoffs: [{ subject: "budget:seadex", active: true, until: "2026-11-08T08:00:00Z", failures: 3, reason: "503 from upstream", updated_at: "2026-11-07T08:00:00Z" }],
      budgets: [{ key: "ad-api", requests: 1, per: "second", daily: 5000, used_hour: 30, used_day: 300, used_by: ["torrentio"] }],
    },
  }]);
  renderPage(<RequestLimitsStatus />, "/admin/advanced/limits");
  const reason = await screen.findByText("503 from upstream", { exact: false });
  const backoff = reason.closest("tr") as HTMLElement;
  expect(shownOnPhones(backoff)).toBe(true);
  expect(within(backoff).getByText("Reason")).toBeTruthy();
  const budget = document.querySelector<HTMLElement>('[data-budget="ad-api"]')!;
  expect(shownOnPhones(budget)).toBe(true);
  expect(budget.textContent).toContain("torrentio");
  expect(budget.textContent).toContain("5,000/day");
  expect(usedByEverywhere(budget)).toBe(true);
  // The cap has the same visible label as the other stacked values, in the cell that holds the bar.
  expect(within(budget).getByText("Caps").closest("td")?.textContent).toContain("5,000/day");
});

it("shows both caps of a budget that has an hourly and a daily one", async () => {
  mockFetch([{ method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true }, backoffs: [],
    budgets: [{ key: "tmdb", requests: 40, per: "10s", hourly: 1000, daily: 20000, used_hour: 500, used_day: 900, used_by: ["metadata"] }] } }]);
  renderPage(<RequestLimitsStatus />, "/admin/advanced/limits");
  await screen.findByText("metadata");
  const budget = document.querySelector<HTMLElement>('[data-budget="tmdb"]')!;
  expect([...budget.querySelectorAll("[data-cap]")].map((c) => c.textContent)).toEqual(["1,000/hour", "20,000/day"]);
});
