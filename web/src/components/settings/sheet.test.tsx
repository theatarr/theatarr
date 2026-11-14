// @vitest-environment jsdom
// The side sheet every settings editor opens in: focus on open and close, Escape, the marked row, ?edit=<key>, and
// confirmations stacked on top of it.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { SettingsTabView } from "@/pages/admin/settings";
import { dmm, overview, rd, schema, zilean } from "./fixtures";
import { json, mockFetch, renderPage } from "@/test/page";

const indexers = () => <SettingsTabView tab="indexers" overview={overview({ indexers: [dmm, zilean] })} schema={schema} versions={[]} />;
const row = (key: string) => document.querySelector(`[data-record="${key}"]`);

describe("settings side sheet", () => {
  it("focuses its heading, marks the row, and on Escape closes and gives focus back to the row's Edit", async () => {
    mockFetch([]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    const edit = screen.getByRole("button", { name: "Edit dmm" });
    await user.click(edit);

    const sheet = screen.getByRole("dialog", { name: "Edit indexer dmm" });
    await waitFor(() => expect(document.activeElement).toBe(within(sheet).getByTestId("record-form-heading")));
    expect(row("dmm")?.getAttribute("data-selected")).toBe("true");
    expect(row("zilean")?.getAttribute("data-selected")).not.toBe("true");
    expect(within(sheet).getByTestId("sheet-footer").textContent).toContain("Save");

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(edit);
    expect(row("dmm")?.getAttribute("data-selected")).not.toBe("true");
  });

  it("opens the sheet named by ?edit=<key>", async () => {
    mockFetch([]);
    renderPage(indexers(), "/admin/settings/indexers?edit=zilean");
    const sheet = await screen.findByRole("dialog", { name: "Edit indexer zilean" });
    expect(within(sheet).getByTestId("record-form").getAttribute("data-mode")).toBeTruthy();
    expect(row("zilean")?.getAttribute("data-selected")).toBe("true");
  });

  it("stacks the delete confirmation over the sheet: Escape closes only the dialog, a delete moves focus to the next row", async () => {
    const fetch = mockFetch([{ method: "DELETE", path: "/settings/indexers/3", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Edit dmm" }));
    const sheet = screen.getByRole("dialog", { name: "Edit indexer dmm" });
    const del = within(sheet).getByTestId("record-delete");
    await user.click(del);

    const confirm = await screen.findByTestId("delete-confirm");
    expect(within(confirm).getByRole("dialog", { name: "Delete indexer dmm?" })).toBeTruthy();
    expect(screen.getAllByRole("dialog")).toHaveLength(2);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByTestId("delete-confirm")).toBeNull());
    expect(screen.getByRole("dialog", { name: "Edit indexer dmm" })).toBe(sheet);
    await waitFor(() => expect(document.activeElement).toBe(del));
    expect(fetch.calls("DELETE", "/settings/indexers/3")).toHaveLength(0);

    await user.click(del);
    await user.click(within(await screen.findByTestId("delete-confirm")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(fetch.calls("DELETE", "/settings/indexers/3")[0].search).toBe("?generation=4");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Edit zilean" })));
  });

  it("asks a provider delete's orphan choice in the dialog, off unless ticked", async () => {
    const fetch = mockFetch([{ method: "DELETE", path: "/settings/providers/7", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<SettingsTabView tab="providers" overview={overview({ providers: [rd] })} schema={schema} versions={[]} />, "/admin/settings/downloads");
    await user.click(screen.getByRole("button", { name: "Edit rd" }));
    await user.click(screen.getByTestId("record-delete"));
    const confirm = await screen.findByTestId("delete-confirm");
    expect((within(confirm).getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(fetch.calls("DELETE", "/settings/providers/7")).toHaveLength(1));
    expect(fetch.calls("DELETE", "/settings/providers/7")[0].search).toBe("?generation=4&orphan_active_downloads=false");
  });
});

describe("unsaved edits in a sheet", () => {
  it("asks before Escape, Close or Cancel drops typed changes; Cancel in the question keeps the sheet and the draft", async () => {
    mockFetch([]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Edit dmm" }));
    const sheet = screen.getByRole("dialog", { name: "Edit indexer dmm" });
    const url = within(sheet).getByRole("textbox", { name: /Service URL/ });
    await user.type(url, "x");
    for (const close of [() => user.keyboard("{Escape}"), () => user.click(within(sheet).getByTestId("sheet-close")), () => user.click(within(sheet).getByTestId("record-cancel"))]) {
      await close();
      const ask = await screen.findByTestId("discard-confirm");
      expect(ask.textContent).toContain("Drop 1 unsaved change.");
      await user.click(within(ask).getByTestId("confirm-cancel"));
      await waitFor(() => expect(screen.queryByTestId("discard-confirm")).toBeNull());
      expect(screen.getByRole("dialog", { name: "Edit indexer dmm" })).toBeTruthy();
      expect((within(sheet).getByRole("textbox", { name: /Service URL/ }) as HTMLInputElement).value).toMatch(/x$/);
    }
    await user.keyboard("{Escape}");
    await user.click(within(await screen.findByTestId("discard-confirm")).getByRole("button", { name: "Close without saving" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("closes at once without changes", async () => {
    mockFetch([]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Edit dmm" }));
    await user.click(within(screen.getByRole("dialog", { name: "Edit indexer dmm" })).getByTestId("record-cancel"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByTestId("discard-confirm")).toBeNull();
  });
});

describe("indexer order", () => {
  it("shows the search order as 1..n and says Saved after an arrow moves an indexer", async () => {
    const fetch = mockFetch([{ method: "PUT", path: "/settings/indexers/priority", reply: { generation: 5 } }]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    expect(screen.getAllByTestId("search-order").map((n) => n.textContent)).toEqual(["1", "2"]);
    expect(screen.getByTestId("move-saved").textContent).toBe("");
    await user.click(screen.getAllByRole("button", { name: "Move zilean up" })[0]);
    await waitFor(() => expect(fetch.calls("PUT", "/settings/indexers/priority")).toHaveLength(1));
    expect((await screen.findByText("Saved: zilean moved up.")).getAttribute("role")).toBe("status");
  });
});
