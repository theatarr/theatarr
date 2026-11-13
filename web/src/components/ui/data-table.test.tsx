// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { DataTable, type DataColumn } from "./data-table";
import { Pager } from "./pager";

type Row = { id: number; name: string; size: string };
const rows: Row[] = [
  { id: 1, name: "Alpha", size: "1 GB" },
  { id: 2, name: "Beta", size: "2 GB" },
];
const columns: DataColumn<Row>[] = [
  { id: "name", label: "Title", cell: (r) => r.name, sortable: true, card: "none" },
  { id: "size", label: "Size", cell: (r) => r.size, sortable: true },
];

const phone = () => vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("sorts from a header and marks the sorted column", async () => {
  const onSort = vi.fn();
  render(<DataTable rows={rows} rowKey={(r) => r.id} columns={columns} sort={{ column: "size", dir: "desc" }} onSort={onSort} />);
  await userEvent.setup().click(screen.getByRole("button", { name: "Sort by title" }));
  expect(onSort).toHaveBeenCalledWith("name");
  const heads = screen.getAllByRole("columnheader");
  expect(heads[1].getAttribute("aria-sort")).toBe("descending");
  expect(heads[0].getAttribute("aria-sort")).toBeNull();
});

it("selects every row from the select-all box and shows an open row's panel under it", async () => {
  function Harness() {
    const [chosen, setChosen] = useState<ReadonlySet<number>>(new Set());
    const select = {
      chosen,
      toggle: (id: number, on: boolean) => setChosen((s) => { const n = new Set(s); if (on) n.add(id); else n.delete(id); return n; }),
      all: (on: boolean) => setChosen(new Set(on ? rows.map((r) => r.id) : [])),
    };
    return (
      <DataTable rows={rows} rowKey={(r) => r.id} columns={columns} select={select} selectAllLabel="Select all 2 shown" rowLabel={(r) => r.name}
        actions={(r) => <button type="button">Remove {r.name}</button>} expanded={(r) => (r.id === 2 ? <p>Details of Beta</p> : null)} />
    );
  }
  render(<Harness />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: "Select all 2 shown" }));
  expect((screen.getByRole("checkbox", { name: "Select Alpha" }) as HTMLInputElement).checked).toBe(true);
  expect((screen.getByRole("checkbox", { name: "Select Beta" }) as HTMLInputElement).checked).toBe(true);
  await user.click(screen.getByRole("checkbox", { name: "Select all 2 shown" }));
  expect((screen.getByRole("checkbox", { name: "Select Alpha" }) as HTMLInputElement).checked).toBe(false);
  const panel = screen.getByText("Details of Beta").closest("td")!;
  expect(panel.getAttribute("colspan")).toBe("4");
  expect(screen.getByRole("button", { name: "Remove Alpha" })).toBeTruthy();
});

it("draws cards with each column as a labelled line on a phone", () => {
  phone();
  render(<DataTable rows={rows} rowKey={(r) => r.id} columns={columns} cardHead={(r) => <strong>{r.name}</strong>} testid="things" />);
  expect(screen.queryByRole("table")).toBeNull();
  const cards = within(screen.getByTestId("things")).getAllByRole("listitem");
  expect(cards).toHaveLength(2);
  expect(within(cards[0]).getByText("Alpha")).toBeTruthy();
  const line = cards[1].querySelector('[data-column="size"]')!;
  expect(line.textContent).toBe("Size2 GB");
  expect(cards[0].querySelector('[data-column="name"]')).toBeNull();
});

it("pages: Next asks for the next offset and the range follows", async () => {
  const onPage = vi.fn();
  const { rerender } = render(<Pager offset={0} limit={50} total={312} onPage={onPage} />);
  expect(screen.getByRole("button", { name: "Previous" })).toHaveProperty("disabled", true);
  await userEvent.setup().click(screen.getByRole("button", { name: "Next" }));
  expect(onPage).toHaveBeenCalledWith(50);
  rerender(<Pager offset={300} limit={50} total={312} onPage={onPage} />);
  expect(screen.getByTestId("pager-range").textContent).toBe("301–312 of 312");
  expect(screen.getByRole("button", { name: "Next" })).toHaveProperty("disabled", true);
});
