// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { providerSchema, indexerSchema, dmm, rd, zilean } from "./fixtures";
import { RecordEditor } from "./record-form";

it("starts with service, write-only API key and Enabled; advanced values preserve the settings payload", async () => {
  const fetch = mockFetch([{ method: "POST", path: "/settings/providers", reply: { generation: 5 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", generation: 4 }} nextOrder={1} onDone={vi.fn()} />);
  expect(screen.getByRole("combobox", { name: "Service" })).toBeTruthy();
  expect(screen.getByRole("checkbox", { name: /Enabled/ })).toBeTruthy();
  expect(screen.getByRole("textbox", { name: /^Name/ }).closest("details")?.open).toBe(false);
  expect(screen.getByRole("textbox", { name: /Service URL/ }).closest("details")?.open).toBe(false);
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "torbox");
  await user.click(screen.getByRole("button", { name: "Set" }));
  await user.type(screen.getByLabelText("New credential"), "fixture-token");
  await user.click(screen.getByText("Show advanced"));
  expect((screen.getByRole("textbox", { name: /^Name/ }) as HTMLInputElement).value).toBe("torbox");
  expect((screen.getByRole("textbox", { name: /Service URL/ }) as HTMLInputElement).value).toBe("https://api.torbox.app/v1");
  await user.type(screen.getByRole("textbox", { name: /Adds per minute/ }), "3");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("POST", "/settings/providers")).toHaveLength(1));
  expect(fetch.calls("POST", "/settings/providers")[0].body).toEqual({
    generation: 4, key: "torbox", enabled: false, secret: "fixture-token",
    config: { kind: "torbox", order: 1, url: "https://api.torbox.app/v1", pacing: { adds_per_minute: 3 } },
  });
});


it.each(["providers", "indexers"] as const)("keeps a typed URL when changing %s service", async (entity) => {
  mockFetch([]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity={entity} ent={entity === "providers" ? providerSchema : indexerSchema} session={{ entity, generation: 4 }} nextOrder={1} onDone={vi.fn()} />);
  await user.click(screen.getByText("Show advanced"));
  const url = screen.getByRole("textbox", { name: /Service URL/ });
  await user.clear(url);
  await user.type(url, "http://127.0.0.1/custom");
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), entity === "providers" ? "torbox" : "dmm");
  expect((url as HTMLInputElement).value).toBe("http://127.0.0.1/custom");
});

it("prefills the public address of a built-in indexer, follows the service, and hints at DMM's feed", async () => {
  const fetch = mockFetch([{ method: "POST", path: "/settings/indexers", reply: { generation: 5 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="indexers" ent={indexerSchema} session={{ entity: "indexers", generation: 4, kind: "torrentio" }} nextOrder={1} onDone={vi.fn()} />);
  const url = screen.getByRole("textbox", { name: /Service URL/ }) as HTMLInputElement;
  expect(url.value).toBe("https://torrentio.strem.fun");
  expect(screen.queryByTestId("dmm-url-hint")).toBeNull();
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "dmm");
  expect(url.value).toBe("");
  expect(screen.getByTestId("dmm-url-hint").textContent).toContain("/api/torznab/cached");
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "zilean");
  expect(url.value).toBe("");
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "seadex");
  expect(url.value).toBe("https://seadex.moe");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("POST", "/settings/indexers")).toHaveLength(1));
  expect(fetch.calls("POST", "/settings/indexers")[0].body).toMatchObject({ key: "seadex", enabled: false, config: { kind: "seadex", url: "https://seadex.moe" } });
});

it("suggests the next free key for a new indexer and service changes", async () => {
  mockFetch([]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="indexers" ent={indexerSchema} session={{ entity: "indexers", generation: 4 }} records={[{ ...dmm, key: "torznab" }, { ...dmm, key: "torznab-2" }, dmm]} nextOrder={1} onDone={vi.fn()} />);
  await user.click(screen.getByText("Show advanced"));
  expect((screen.getByRole("textbox", { name: /^Name/ }) as HTMLInputElement).value).toBe("torznab-3");
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "dmm");
  expect((screen.getByRole("textbox", { name: /^Name/ }) as HTMLInputElement).value).toBe("dmm-2");
});


it("preserves an edited key when changing a new indexer's service and saving", async () => {
  const fetch = mockFetch([{ method: "POST", path: "/settings/indexers", reply: { generation: 5 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="indexers" ent={indexerSchema} session={{ entity: "indexers", generation: 4 }} nextOrder={1} onDone={vi.fn()} />);
  await user.click(screen.getByText("Show advanced"));
  const key = screen.getByRole("textbox", { name: /^Name/ });
  await user.clear(key);
  await user.type(key, "my-custom-key");
  await user.type(screen.getByRole("textbox", { name: /Service URL/ }), "http://127.0.0.1/torznab");
  await user.click(screen.getByText("Show advanced"));
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "dmm");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("POST", "/settings/indexers")).toHaveLength(1));
  expect(fetch.calls("POST", "/settings/indexers")[0].body).toMatchObject({ key: "my-custom-key", config: { kind: "dmm" } });
});

it("adds a provider from the three first fields: service, API key and Enable", async () => {
  const fetch = mockFetch([{ method: "POST", path: "/settings/providers", reply: { generation: 5 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", generation: 4 }} nextOrder={2} onDone={vi.fn()} />);
  await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "alldebrid");
  await user.click(screen.getByRole("button", { name: "Set" }));
  await user.type(screen.getByLabelText("New credential"), "fixture-token");
  await user.click(screen.getByRole("checkbox", { name: /Enabled/ }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("POST", "/settings/providers")).toHaveLength(1));
  // The key and URL come from the service; nothing advanced was opened.
  expect(fetch.calls("POST", "/settings/providers")[0].body).toEqual({
    generation: 4, key: "alldebrid", enabled: true, secret: "fixture-token",
    config: { kind: "alldebrid", order: 2, url: "https://api.alldebrid.com/v4" },
  });
});

it.each([
  ["closed for a provider at its defaults", "providers", { ...rd, config: { kind: "realdebrid", order: 1, url: "https://api.real-debrid.com/rest/1.0" } }, false],
  ["open for a provider with a budget and pacing", "providers", rd, true],
  ["open for a provider at another URL", "providers", { ...rd, config: { kind: "realdebrid", order: 1, url: "http://127.0.0.1/rd" } }, true],
  ["closed for an indexer with only its URL", "indexers", { ...zilean, priority: 0, config: { kind: "zilean", url: "http://zilean:8181" } }, false],
  ["open for an indexer with a priority", "indexers", { ...zilean, config: { kind: "zilean", url: "http://zilean:8181" } }, true],
  ["open for an indexer with a page cap", "indexers", dmm, true],
] as const)("edits with Show advanced %s", (_, entity, record, open) => {
  mockFetch([]);
  renderPage(<RecordEditor entity={entity} ent={entity === "providers" ? providerSchema : indexerSchema} session={{ entity, record, generation: 4 }} nextOrder={1} onDone={vi.fn()} />);
  expect(screen.getByRole("combobox", { name: "Service" })).toBeTruthy();
  expect((screen.getByText("Show advanced").closest("details") as HTMLDetailsElement).open).toBe(open);
});

it("shows a new indexer's name and required URL before Show advanced", async () => {
  mockFetch([]);
  renderPage(<RecordEditor entity="indexers" ent={indexerSchema} session={{ entity: "indexers", generation: 4, kind: "torznab" }} nextOrder={1} onDone={vi.fn()} />);
  expect(screen.getByRole("textbox", { name: /^Name/ }).closest("details")).toBeNull();
  expect(screen.getByRole("textbox", { name: /Service URL/ }).closest("details")).toBeNull();
  expect(screen.getByRole("textbox", { name: /Page cap/ }).closest("details")?.open).toBe(false);
});

const allowlistSchema = { ...indexerSchema, config: [...indexerSchema.config, { name: "providers", type: "list" as const, help: "Where its releases may go." }] };

it("edits an indexer's provider allowlist as Any or named providers, saving the same list", async () => {
  const saved = { ...dmm, config: { ...dmm.config, providers: ["rd"] } };
  const fetch = mockFetch([{ method: "PUT", path: "/settings/indexers/3", reply: { generation: 5 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="indexers" ent={allowlistSchema} session={{ entity: "indexers", generation: 4, record: saved }} providerKeys={["rd", "ad", "tb"]} nextOrder={1} onDone={vi.fn()} />);
  const group = screen.getByRole("group", { name: "Providers" });
  expect((screen.getByRole("radio", { name: "Only these providers" }) as HTMLInputElement).checked).toBe(true);
  expect((screen.getByRole("checkbox", { name: "rd" }) as HTMLInputElement).checked).toBe(true);
  await user.click(screen.getByRole("checkbox", { name: "tb" }));
  expect(group.textContent).toContain("Changed");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("PUT", "/settings/indexers/3")).toHaveLength(1));
  expect(fetch.calls("PUT", "/settings/indexers/3")[0].body).toMatchObject({ config: { providers: ["rd", "tb"] } });
  await user.click(screen.getByRole("radio", { name: "Any provider chosen by the library" }));
  expect(screen.queryByRole("checkbox", { name: "rd" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("PUT", "/settings/indexers/3")).toHaveLength(2));
  expect((fetch.calls("PUT", "/settings/indexers/3")[1].body as { config: Record<string, unknown> }).config.providers ?? []).toEqual([]);
});

it("refuses to save Only these providers with none ticked, with an inline error", async () => {
  const saved = { ...dmm, config: { ...dmm.config, providers: ["rd"] } };
  const fetch = mockFetch([{ method: "PUT", path: "/settings/indexers/3", reply: { generation: 5 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="indexers" ent={allowlistSchema} session={{ entity: "indexers", generation: 4, record: saved }} providerKeys={["rd", "ad"]} nextOrder={1} onDone={vi.fn()} />);
  await user.click(screen.getByRole("checkbox", { name: "rd" }));
  // Unticking the last provider keeps Only these chosen rather than flipping to Any.
  expect((screen.getByRole("radio", { name: "Only these providers" }) as HTMLInputElement).checked).toBe(true);
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect((await screen.findByRole("alert")).textContent).toBe("Tick at least one provider, or choose Any.");
  expect(fetch.calls("PUT", "/settings/indexers/3")).toHaveLength(0);
  await user.click(screen.getByRole("radio", { name: "Any provider chosen by the library" }));
  expect(screen.queryByRole("alert")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("PUT", "/settings/indexers/3")).toHaveLength(1));
  expect((fetch.calls("PUT", "/settings/indexers/3")[0].body as { config: Record<string, unknown> }).config.providers ?? []).toEqual([]);
});
