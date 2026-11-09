// @vitest-environment jsdom
// The settings form pattern (docs/design/ui.md "Settings forms"): changed marks with Undo back to the saved value, Reset
// to the default, help and errors tied to their control, the unsaved-changes bar with the count, Discard and Save.
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import schema from "./testdata/schema.json";
import { SectionPanel } from "./section-panel";
import { RecordEditor } from "./record-form";
import { changedPaths } from "./section-draft";
import { Field } from "./parts";
import { FieldInput } from "./field-input";
import { providerSchema, rd } from "./fixtures";

it("counts changed leaves, treating unset and null alike", () => {
  expect(changedPaths({ a: 1, b: { c: "x", d: [1] } }, { a: 1, b: { c: "x", d: [1] } })).toEqual([]);
  expect(changedPaths({ a: 2, b: { c: "y", d: [1, 2] } }, { a: 1, b: { c: "x", d: [1] } })).toEqual(["a", "b.c", "b.d"]);
  expect(changedPaths({ a: null }, {})).toEqual([]);
  expect(changedPaths({}, { a: "1m" })).toEqual(["a"]);
});

function panel(fail = false) {
  const fetch = mockFetch([
    { method: "GET", path: "/settings/schema", reply: schema },
    { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "router", config: { cached_only: false, poll_interval: "5m", max_candidates: 3 }, secrets: [] }] } },
    { method: "PUT", path: "/settings/sections/router", reply: fail ? { status: 400, body: { error: "invalid settings", fields: [{ path: "router.max_candidates", message: "must be at most 50" }] } } : {} },
  ]);
  renderPage(<SectionPanel id="grabbing" title="What to grab" section="router" keys={["cached_only", "poll_interval", "max_candidates"]} />);
  return fetch;
}

it("marks each edited field, undoes it to the saved value and counts the changes in the bar", async () => {
  const fetch = panel();
  const toggle = await screen.findByRole("switch", { name: "Only add cached releases" });
  const bar = screen.getByTestId("unsaved-bar");
  expect(bar.dataset.dirty).toBeUndefined();
  expect((within(bar).getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.queryAllByTestId("field-changed")).toHaveLength(0);

  fireEvent.click(toggle);
  const max = screen.getByRole("spinbutton", { name: "Maximum candidate releases" });
  fireEvent.change(max, { target: { value: "8" } });
  expect(screen.getAllByTestId("field-changed")).toHaveLength(2);
  expect(screen.getByRole("region", { name: "Unsaved changes: What to grab" })).toBe(bar);
  expect(screen.getByTestId("unsaved-count").textContent).toBe("2 unsaved changes");

  // Undo puts the saved value back, not the default.
  fireEvent.click(screen.getByRole("button", { name: "Undo change to Maximum candidate releases" }));
  expect((max as HTMLInputElement).value).toBe("3");
  expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");

  // Reset goes to the default: the key is left out of the saved section.
  fireEvent.click(screen.getByRole("button", { name: "Reset Maximum candidate releases" }));
  expect(screen.getByTestId("unsaved-count").textContent).toBe("2 unsaved changes");
  fireEvent.click(within(bar).getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/router")).toHaveLength(1));
  expect(fetch.calls("PUT", "/settings/sections/router")[0].body).toEqual({ generation: 4, config: { cached_only: true, poll_interval: "5m" } });
});

it("discards every change from the bar", async () => {
  const fetch = panel();
  fireEvent.click(await screen.findByRole("switch", { name: "Only add cached releases" }));
  fireEvent.click(within(screen.getByTestId("unsaved-bar")).getByRole("button", { name: "Discard" }));
  expect(screen.queryByTestId("unsaved-count")).toBeNull();
  expect(screen.queryAllByTestId("field-changed")).toHaveLength(0);
  expect(fetch.calls("PUT", "/settings/sections/router")).toHaveLength(0);
});

it("ties help and the server's validation message to the control", async () => {
  panel(true);
  const max = await screen.findByRole("spinbutton", { name: "Maximum candidate releases" });
  const ids = () => (max.getAttribute("aria-describedby") ?? "").split(" ").map((id) => document.getElementById(id)?.textContent);
  expect(ids()).toEqual([expect.stringContaining("caps how many accepted releases")]);
  fireEvent.change(max, { target: { value: "80" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(max.getAttribute("aria-invalid")).toBe("true"));
  expect(ids()).toEqual(["must be at most 50", expect.stringContaining("caps how many accepted releases")]);
});

it("marks record edits, counts them in the footer and discards without closing", async () => {
  mockFetch([]);
  const user = userEvent.setup();
  const onDone = vi.fn();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", record: rd, generation: 4 }} nextOrder={2} onDone={onDone} />);
  const sheet = await screen.findByTestId("record-form");
  expect(within(sheet).queryByTestId("unsaved-count")).toBeNull();
  await user.click(within(sheet).getByTestId("record-enabled"));
  const url = within(sheet).getByRole("textbox", { name: /^Service URL/ });
  await user.clear(url);
  await user.type(url, "https://rd.example/api");
  expect(within(sheet).getAllByTestId("field-changed")).toHaveLength(2);
  expect(within(sheet).getByTestId("unsaved-count").textContent).toBe("2 unsaved changes");
  // The help stays tied to the URL input.
  expect(url.getAttribute("aria-describedby")).toBeTruthy();
  await user.click(within(sheet).getByRole("button", { name: "Undo change to Enabled" }));
  expect(within(sheet).getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
  await user.click(within(sheet).getByTestId("record-discard"));
  expect((url as HTMLInputElement).value).toBe(rd.config.url);
  expect(within(sheet).queryByTestId("unsaved-count")).toBeNull();
  expect(onDone).not.toHaveBeenCalled();
});

it("keeps a control's own description when the field adds help and an error", () => {
  render(<><span id="own">Own note</span><Field label="Name" help="Field help" error="Too long"><input aria-describedby="own" /></Field></>);
  const box = screen.getByRole("textbox", { name: "Name" });
  expect((box.getAttribute("aria-describedby") ?? "").split(" ").map((id) => document.getElementById(id)?.textContent)).toEqual(["Own note", "Too long", "Field help"]);
  expect(box.getAttribute("aria-invalid")).toBe("true");
});

it("ties each list row to its own indexed error and the list's help", () => {
  const field = { name: "versions", label: "Libraries", type: "list", help: "Which libraries notify." } as Parameters<typeof FieldInput>[0]["field"];
  render(<FieldInput field={field} path="notify.targets.home.versions" value={["movie-web", "nope"]} onChange={() => {}} errors={{ "notify.targets.home.versions.1": "unknown library nope" }} />);
  const words = (el: HTMLElement) => (el.getAttribute("aria-describedby") ?? "").split(" ").map((id) => document.getElementById(id)?.textContent);
  const first = screen.getByRole("textbox", { name: "Libraries 1" });
  const second = screen.getByRole("textbox", { name: "Libraries 2" });
  expect(words(first)).toEqual(["Which libraries notify."]);
  expect(first.getAttribute("aria-invalid")).toBeNull();
  expect(words(second)).toEqual(["unknown library nope", "Which libraries notify."]);
  expect(second.getAttribute("aria-invalid")).toBe("true");
});

it("undoes a nested edit under an absent parent back to the saved value", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/settings/schema", reply: schema },
    { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "metadata", config: {}, secrets: [] }] } },
    { method: "PUT", path: "/settings/sections/metadata", reply: {} },
  ]);
  renderPage(<SectionPanel id="art" title="Artwork" section="metadata" keys={["art"]} />);
  await screen.findAllByRole("spinbutton");
  const field = document.querySelector<HTMLElement>('div[data-field="metadata.art.per_run"]')!;
  const box = within(field).getByRole("spinbutton");
  fireEvent.change(box, { target: { value: "7" } });
  expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
  fireEvent.click(within(field).getByRole("button", { name: /^Undo change to / }));
  expect(screen.queryByTestId("unsaved-count")).toBeNull();
  expect((within(screen.getByTestId("unsaved-bar")).getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  expect(fetch.calls("PUT", "/settings/sections/metadata")).toHaveLength(0);
});

it("clears the discarded draft's validation on Discard", async () => {
  mockFetch([
    { method: "PUT", path: "/settings/providers/7", reply: { status: 400, body: { error: "invalid settings", fields: [{ path: "providers.rd.config.url", message: "must be https" }] } } },
  ]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", record: rd, generation: 4 }} nextOrder={2} onDone={() => {}} />);
  const sheet = await screen.findByTestId("record-form");
  const url = within(sheet).getByRole("textbox", { name: /^Service URL/ });
  await user.clear(url);
  await user.type(url, "http://rd.example");
  await user.click(within(sheet).getByTestId("record-save"));
  await waitFor(() => expect(url.getAttribute("aria-invalid")).toBe("true"));
  expect(within(sheet).getByText("must be https")).toBeTruthy();
  await user.click(within(sheet).getByTestId("record-discard"));
  expect((url as HTMLInputElement).value).toBe(rd.config.url);
  expect(url.getAttribute("aria-invalid")).toBeNull();
  expect(within(sheet).queryByText("must be https")).toBeNull();
  expect(within(sheet).queryByTestId("settings-error")?.textContent ?? null).toBeNull();
});

it("tracks a new record against its first draft: marks, count and Discard", async () => {
  mockFetch([]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", generation: 4, kind: "realdebrid" }} nextOrder={2} onDone={() => {}} />);
  const sheet = await screen.findByTestId("record-form");
  expect(within(sheet).queryByTestId("unsaved-count")).toBeNull();
  const url = within(sheet).getByRole("textbox", { name: /^Service URL/ });
  const first = (url as HTMLInputElement).value;
  await user.clear(url);
  await user.type(url, "https://rd.example/api");
  expect(within(sheet).getAllByTestId("field-changed")).toHaveLength(1);
  expect(within(sheet).getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
  await user.click(within(sheet).getByTestId("record-discard"));
  expect((url as HTMLInputElement).value).toBe(first);
  expect(within(sheet).queryByTestId("unsaved-count")).toBeNull();
});

it("discards a new provider back to its first draft, so a new service choice fills its own URL and name", async () => {
  const fetch = mockFetch([{ method: "POST", path: "/settings/providers", reply: { id: 9 } }]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", generation: 4, kind: "realdebrid" }} nextOrder={2} onDone={() => {}} />);
  const sheet = await screen.findByTestId("record-form");
  const service = within(sheet).getByRole("combobox", { name: /^Service/ });
  const url = within(sheet).getByRole("textbox", { name: /^Service URL/ });
  await user.clear(url);
  await user.type(url, "https://proxy.example/rd");
  await user.selectOptions(service, "torbox");
  // The service drives the name and URL: it is marked, but has no Undo.
  expect(within(sheet).queryByRole("button", { name: "Undo change to Service" })).toBeNull();
  expect((url as HTMLInputElement).value).toBe("https://proxy.example/rd");
  await user.click(within(sheet).getByTestId("record-discard"));
  expect(within(sheet).queryByTestId("unsaved-count")).toBeNull();
  await user.selectOptions(service, "alldebrid");
  expect((url as HTMLInputElement).value).toBe("https://api.alldebrid.com/v4");
  await user.click(within(sheet).getByTestId("record-save"));
  await waitFor(() => expect(fetch.calls("POST", "/settings/providers")).toHaveLength(1));
  expect(fetch.calls("POST", "/settings/providers")[0].body).toEqual({ generation: 4, key: "alldebrid", enabled: false, config: { url: "https://api.alldebrid.com/v4", kind: "alldebrid", order: 2 } });
});

it("counts and discards a new record's name alone", async () => {
  mockFetch([]);
  const user = userEvent.setup();
  renderPage(<RecordEditor entity="providers" ent={providerSchema} session={{ entity: "providers", generation: 4, kind: "realdebrid" }} nextOrder={2} onDone={() => {}} />);
  const sheet = await screen.findByTestId("record-form");
  const name = sheet.querySelector<HTMLInputElement>('input[data-field="key"]')!;
  const first = name.value;
  await user.clear(name);
  await user.type(name, "rd-backup");
  expect(within(sheet).getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
  expect(within(sheet).getByRole("button", { name: "Undo change to Name" })).toBeTruthy();
  await user.click(within(sheet).getByTestId("record-discard"));
  expect(name.value).toBe(first);
  expect(within(sheet).queryByTestId("unsaved-count")).toBeNull();
});
