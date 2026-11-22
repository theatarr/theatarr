// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SettingsTabView } from "@/pages/admin/settings";
import { budget, budgetSchema, indexerSchema, overview, rd, schema } from "./fixtures";
import { RecordEditor } from "./record-form";
import { expectNamedControls, json, mockFetch, renderPage } from "@/test/page";

describe("source editors", () => {
  it("saves an indexer generation and focuses and announces field errors from 422", async () => {
    const fetch = mockFetch([
      { method: "POST", path: "/settings/indexers", reply: json({
        error: "settings validation failed",
        fields: [{ path: "priority", message: "Priority must be at least 1" }, { path: "config.url", message: "URL is not accepted" }],
      }, 422) },
    ]);
    const user = userEvent.setup();
    const onDone = vi.fn();
    // Opening the sheet and choosing a source are covered in add-indexer.test.tsx and sheet.test.tsx.
    renderPage(<RecordEditor entity="indexers" ent={indexerSchema} session={{ entity: "indexers", generation: 4, kind: "torznab" }} nextOrder={1} onDone={onDone} />);
    const form = within(screen.getByTestId("record-form"));
    // Paste complete values: per-character typing repeatedly renders the whole editor.
    await user.click(form.getByText("Advanced options"));
    await user.clear(form.getByRole("textbox", { name: /^Name/ }));
    await user.click(form.getByRole("textbox", { name: /^Name/ }));
    await user.paste("new-indexer");
    await user.click(form.getByRole("textbox", { name: /^Service URL/i }));
    await user.paste("https://indexer.example/api");
    await user.clear(form.getByRole("textbox", { name: /^Search priority/i }));
    await user.paste("2");
    await user.click(form.getByRole("button", { name: "Save" }));

    const error = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(error));
    expect(screen.getByText("Priority must be at least 1")).toBeTruthy();
    expect(screen.getByText("URL is not accepted")).toBeTruthy();
    expect(screen.getByTestId("candidate-announce").textContent).toContain("Settings were not saved.");
    expect(fetch.calls("POST", "/settings/indexers")[0].body).toMatchObject({ generation: 4, key: "new-indexer", priority: 2, config: { url: "https://indexer.example/api" } });
    expect(fetch.calls("POST", "/settings/indexers")[0].headers["x-csrf-token"]).toBe("ui-test-csrf");
    expect(onDone).not.toHaveBeenCalled();
  });

  it("renders and saves a provider with the current settings generation", async () => {
    const fetch = mockFetch([
      { method: "POST", path: "/settings/providers", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<SettingsTabView tab="providers" overview={overview()} schema={schema} />, "/admin/settings/downloads");
    await user.click(screen.getByRole("button", { name: "Add provider" }));
    expectNamedControls();
    await user.click(screen.getByText("Advanced options"));
    await user.clear(screen.getByRole("textbox", { name: /^Name/ }));
    await user.type(screen.getByRole("textbox", { name: /^Name/ }), "new-provider");
    await user.clear(screen.getByRole("textbox", { name: /^Service URL/i }));
    await user.type(screen.getByRole("textbox", { name: /^Service URL/i }), "https://provider.example/api");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/providers")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/providers")[0].body).toMatchObject({ generation: 4, key: "new-provider", enabled: false, config: { kind: "realdebrid", url: "https://provider.example/api" } });
    expect(fetch.calls("POST", "/settings/providers")[0].headers["x-csrf-token"]).toBe("ui-test-csrf");
  });

  it("lists the write-enabling effects of a 422 and saves again with its token only after confirmation", async () => {
    const effect = "Provider new-provider becomes usable (enabled with a credential): active version movie-web may add torrents to it when router.enabled is on (currently false).";
    let posts = 0;
    const fetch = mockFetch([
      { method: "POST", path: "/settings/providers", reply: () => ++posts === 1
        ? json({ error: "explicit confirmation required", effects: [{ version: "movie-web", effect }], confirmation: { generation: 5, token: "provider-confirmation", effects: [{ version: "movie-web", effect }] } }, 422)
        : json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<SettingsTabView tab="providers" overview={overview()} schema={schema} />, "/admin/settings/downloads");
    await user.click(screen.getByRole("button", { name: "Add provider" }));
    await user.click(screen.getByText("Advanced options"));
    await user.clear(screen.getByRole("textbox", { name: /^Name/ }));
    await user.type(screen.getByRole("textbox", { name: /^Name/ }), "new-provider");
    await user.clear(screen.getByRole("textbox", { name: /^Service URL/i }));
    await user.type(screen.getByRole("textbox", { name: /^Service URL/i }), "https://provider.example/api");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effect)).toBeTruthy();
    expect(fetch.calls("POST", "/settings/providers")).toHaveLength(1);
    expect(fetch.calls("POST", "/settings/providers")[0].headers["x-confirmation-token"]).toBeUndefined();
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/providers")).toHaveLength(2));
    const confirmed = fetch.calls("POST", "/settings/providers")[1];
    expect(confirmed.headers["x-confirmation-token"]).toBe("provider-confirmation");
    expect(confirmed.body).toEqual(fetch.calls("POST", "/settings/providers")[0].body);
  });
});


describe("record conflict reload", () => {
  it.each([true, false])("reload keep=%s never submits, and the next explicit save uses the chosen draft", async (keep) => {
    let writes = 0;
    const fresh = { ...budget, config: { ...budget.config, requests: 20 } };
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview({ generation: 5, budgets: [fresh] }) },
      { method: "PUT", path: "/settings/budgets/1", reply: () => ++writes === 1
        ? json({ error: "settings changed", generation: 5 }, 409)
        : json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    const onDone = vi.fn();
    renderPage(<RecordEditor entity="budgets" ent={budgetSchema} session={{ entity: "budgets", record: budget, generation: 4 }} nextOrder={1} onDone={onDone} />);
    const requests = screen.getByRole("textbox", { name: /^Requests/ });
    await user.clear(requests);
    await user.paste("30");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await user.click(await screen.findByRole("button", { name: keep ? "Reload, keep my changes" : "Reload, discard my changes" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(fetch.calls("PUT", "/settings/budgets/1")).toHaveLength(1);
    expect(onDone).not.toHaveBeenCalled();
    expect((requests as HTMLInputElement).value).toBe(keep ? "30" : "20");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(fetch.calls("PUT", "/settings/budgets/1")).toHaveLength(2);
    expect(fetch.calls("PUT", "/settings/budgets/1")[1].body).toMatchObject({ generation: 5, config: { requests: keep ? 30 : 20 } });
  });

  it("moves a provider earlier although libraries list both: the save reorders their lists, and the help says so (F30)", async () => {
    const ad = { ...rd, id: 8, key: "ad", kind: "alldebrid", order: 2, config: { ...rd.config, kind: "alldebrid", order: 2 } };
    const fetch = mockFetch([
      { method: "PUT", path: "/settings/providers/order", reply: json({ generation: 5, revision: "r5", records: [] }) },
    ]);
    const user = userEvent.setup();
    renderPage(<SettingsTabView tab="providers" overview={overview({ providers: [rd, ad] })} schema={schema} />, "/admin/settings/downloads");
    expect(screen.getByTestId("providers-list").textContent).toContain("each library's providers are put in the same order");
    await user.click(screen.getByRole("button", { name: /^Move ad earlier/ }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/providers/order")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/providers/order")[0].body).toEqual({ generation: 4, order: [8, 7] });
    expect(screen.queryByTestId("order-note")).toBeNull();
  });
});
