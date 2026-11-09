// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { mockFetch, renderPage, type RequestRecord } from "@/test/page";
import { AuditPanel } from "./audit";

const row = (id: number) => ({ id, actor: "admin", created_at: "2026-11-07T10:00:00Z", generation: id, entity: "budgets", key: `b${id}`, operation: "update" });

describe("settings history", () => {
  it("opens newest first and pages with Older and Newer", async () => {
    const { calls } = mockFetch([{
      method: "GET",
      path: "/settings/audit",
      reply: (r: RequestRecord) => new URLSearchParams(r.search).get("before") === "41" ? { records: [row(40), row(39)], next: 0 } : { records: [row(42), row(41)], next: 41 },
    }]);
    const user = userEvent.setup();
    renderPage(<AuditPanel />);
    expect(await screen.findByText("b42")).toBeTruthy();
    const first = new URLSearchParams(calls("GET", "/settings/audit")[0].search);
    expect(first.get("order")).toBe("desc");
    expect(first.get("before")).toBeNull();
    expect((screen.getByTestId("audit-prev") as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: /Older/ }));
    expect(await screen.findByText("b40")).toBeTruthy();
    expect(new URLSearchParams(calls("GET", "/settings/audit").at(-1)!.search).get("before")).toBe("41");
    expect((screen.getByTestId("audit-next") as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: /Newer/ }));
    expect(await screen.findByText("b42")).toBeTruthy();
  });
});

it("names a switch row's entity in the singular", async () => {
  mockFetch([{ method: "GET", path: "/settings/audit", reply: { records: [{ ...row(7), entity: "switches", key: "global", operation: "create" }], next: 0 } }]);
  renderPage(<AuditPanel />);
  expect(await screen.findByText("global")).toBeTruthy();
  expect(screen.getByText("switch")).toBeTruthy();
  expect(screen.queryByText("switche")).toBeNull();
});
