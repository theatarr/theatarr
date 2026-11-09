// @vitest-environment jsdom
import { focusManager } from "@tanstack/react-query";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ViewsCard } from "./views";
import { useSettings } from "@/api/settings";
function GeneralSettingsPage() { const q = useSettings(); return q.data ? <ViewsCard readOnly={!q.data.writable} /> : null; }
import { json, mockFetch, renderPage } from "@/test/page";

const overview = {
  source: "db", writable: true, generation: 7, active_generation: 7, provider_epoch: 0, policy_dir: "/policy", yaml_owned: [], indexers: [], providers: [], budgets: [],
  switches: { router_enabled: false, library_enabled: false, notify_enabled: false, versions: [], sources: { router: "db", library: "db", notify: "db" } },
};
const radarr = { id: 1, key: "radarr", versions: ["movie-web"], profiles: { "WEB only": ["movie-web"] }, identity: { flavour: "radarr", path: "/radarr", alias_from: ["radarr_4k"] }, secret_set: true };
const library = (key: string) => ({ id: 1, key, roots: [], config: { mode: key === "movie-web" ? "active" : "shadow", kind: "movie" } });
const versionsReply = { source: "db", generation: 7, versions: ["movie-web", "movie-remux", "tv-web"].map(library), profiles: {}, format_sets: [], clients: [], indexers: [], providers: [],
  switches: { router_enabled: false, library_enabled: false, notify_enabled: false, versions: [{ key: "movie-web", mode: "shadow", grab_state: { mode: "shadow", grab: false, router_enabled: false, router_forced_off: false } }] } };
const librariesRoute = { method: "GET", path: "/settings/versions", reply: versionsReply };
const routes = (list: unknown[] = [radarr]) => [
  librariesRoute,
  { method: "GET", path: "/settings", reply: overview },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 7, sections: [] } },
  { method: "GET", path: "/settings/view-bindings", reply: { source: "db", generation: 7, views: list } },
];

describe("View editor", () => {
  it("creates a view with its key after confirming what it exposes", async () => {
    const effects = [{ version: "", effect: "Create view radarr2 at /radarr2 (radarr): arr clients with its API key can use it." }];
    const fetch = mockFetch([
      ...routes([]),
      { method: "POST", path: "/settings/view-bindings", reply: json({ error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 8, effects } }, 422) },
      { method: "POST", path: "/settings/view-bindings", reply: { generation: 8, revision: 1 } },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("button", { name: "Add view" }));
    const sheet = await screen.findByTestId("view-editor");
    await user.click(within(sheet).getByRole("textbox", { name: /^Key/ }));
    await user.paste("radarr2");
    await user.click(within(sheet).getByRole("textbox", { name: /^Path/ }));
    await user.paste("/radarr2");
    const libraries = within(sheet).getByRole("group", { name: "Libraries" });
    await user.click(within(libraries).getByRole("checkbox", { name: "movie-web" }));
    await user.click(within(libraries).getByRole("checkbox", { name: "movie-remux" }));
    await user.click(within(sheet).getByRole("textbox", { name: /^New profile name/ }));
    await user.paste("Rules default");
    await user.click(within(sheet).getByRole("button", { name: "Add profile" }));
    await user.click(within(within(sheet).getByRole("group", { name: "Profile: Rules default" })).getByRole("checkbox", { name: "movie-web" }));
    await user.click(within(sheet).getByTestId("secret-input"));
    await user.paste("view-key-value");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(screen.getByText(effects[0].effect)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/view-bindings")).toHaveLength(2));
    const [first, second] = fetch.calls("POST", "/settings/view-bindings");
    expect(first.body).toEqual({ generation: 7, key: "radarr2", flavour: "radarr", path: "/radarr2", alias_from: [],
      versions: ["movie-web", "movie-remux"], profiles: { "Rules default": ["movie-web"] }, api_key: "view-key-value" });
    expect(second.headers["x-confirmation-token"]).toBe("proof");
  });

  it("refuses to save a new view without a key", async () => {
    const fetch = mockFetch(routes([]));
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("button", { name: "Add view" }));
    const sheet = await screen.findByTestId("view-editor");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    expect((await within(sheet).findByTestId("settings-error")).textContent).toContain("needs its API key");
    expect(fetch.calls("POST", "/settings/view-bindings")).toHaveLength(0);
  });

  it("edits a view keeping its stored key, and never shows the key", async () => {
    const fetch = mockFetch([...routes(), { method: "PUT", path: "/settings/view-bindings/radarr", reply: { generation: 8, revision: 1 } }]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    const row = (await screen.findByText("API key set")).closest("li") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    expect(within(sheet).getByTestId("secret-state").textContent).toBe("set");
    const path = within(sheet).getByRole("textbox", { name: /^Path/ });
    await user.clear(path);
    await user.click(path);
    await user.paste("/movies");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/view-bindings/radarr")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[0].body).toEqual({ generation: 7, flavour: "radarr", path: "/movies", alias_from: ["radarr_4k"],
      versions: ["movie-web"], profiles: { "WEB only": ["movie-web"] } });
  });

  it("keeps the generation and view it opened with; a conflict offers an explicit reload", async () => {
    let gen = 7;
    const fetch = mockFetch([
      librariesRoute,
      { method: "GET", path: "/settings", reply: overview },
      { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 7, sections: [] } },
      { method: "GET", path: "/settings/view-bindings", reply: () => ({ source: "db", generation: gen,
        views: gen === 7 ? [radarr] : [{ ...radarr, versions: ["movie-web", "movie-remux"] }] }) },
      { method: "PUT", path: "/settings/view-bindings/radarr", reply: json({ error: "settings changed", generation: 8 }, 409) },
      { method: "PUT", path: "/settings/view-bindings/radarr", reply: { generation: 9, revision: 2 } },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    const row = (await screen.findByText("API key set")).closest("li") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    // Another admin adds a version; this tab refetches on focus. The draft and its generation stay.
    gen = 8;
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    await waitFor(() => expect(fetch.calls("GET", "/settings/view-bindings").length).toBeGreaterThan(1));
    const remux = within(within(sheet).getByRole("group", { name: "Libraries" })).getByRole("checkbox", { name: "movie-remux" }) as HTMLInputElement;
    expect(remux.checked).toBe(false);
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    expect((await within(sheet).findByTestId("settings-error")).getAttribute("data-kind")).toBe("stale");
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[0].body).toMatchObject({ generation: 7, versions: ["movie-web"] });
    await user.click(within(sheet).getByTestId("settings-reload"));
    await waitFor(() => expect(remux.checked).toBe(true));
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/view-bindings/radarr")).toHaveLength(2));
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[1].body).toMatchObject({ generation: 8, versions: ["movie-web", "movie-remux"] });
  });

  it("stays an edit of the view it opened when the view is deleted elsewhere", async () => {
    let views: unknown[] = [radarr];
    const fetch = mockFetch([
      librariesRoute,
      { method: "GET", path: "/settings", reply: overview },
      { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 7, sections: [] } },
      { method: "GET", path: "/settings/view-bindings", reply: () => ({ source: "db", generation: views.length ? 7 : 8, views }) },
      { method: "PUT", path: "/settings/view-bindings/radarr", reply: json({ error: "settings changed", generation: 8 }, 409) },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    const row = (await screen.findByText("API key set")).closest("li") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    views = [];
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    await waitFor(() => expect(fetch.calls("GET", "/settings/view-bindings").length).toBeGreaterThan(1));
    expect(within(sheet).getByText("View radarr")).toBeTruthy();
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await within(sheet).findByTestId("settings-error");
    expect(fetch.calls("POST", "/settings/view-bindings")).toHaveLength(0);
    await user.click(within(sheet).getByTestId("settings-reload"));
    expect((await within(sheet).findByTestId("settings-error")).textContent).toContain("View deleted elsewhere");
  });

  it("deletes a view after a confirmation naming it", async () => {
    const fetch = mockFetch([...routes(), { method: "DELETE", path: "/settings/view-bindings/radarr", reply: { generation: 8, revision: 1 } }]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    const row = (await screen.findByText("API key set")).closest("li") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /Edit/ }));
    await user.click(within(await screen.findByTestId("view-editor")).getByRole("button", { name: "Delete view" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete view radarr" });
    await user.click(within(dialog).getByRole("button", { name: "Delete view" }));
    await waitFor(() => expect(fetch.calls("DELETE", "/settings/view-bindings/radarr")).toHaveLength(1));
    expect(fetch.calls("DELETE", "/settings/view-bindings/radarr")[0].search).toBe("?generation=7");
  });

  it("locks the identity of a view not seeded yet", async () => {
    mockFetch(routes([{ ...radarr, identity: null, secret_set: false }]));
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    expect((within(sheet).getByRole("textbox", { name: /^Path/ }) as HTMLInputElement).disabled).toBe(true);
    expect(within(sheet).queryByTestId("secret-field")).toBeNull();
  });
});

it("retries deleting a view binding with the server confirmation token", async () => {
  const effects = [{ version: "", effect: "A startup fallback left a different view running." }];
  const fetch = mockFetch([...routes().map((route) => route.path === "/settings" ? { ...route, reply: { ...overview, active_generation: 6 } } : route), { method: "DELETE", path: "/settings/view-bindings/radarr", reply: (r: { headers: Record<string, string> }) =>
    r.headers["x-confirmation-token"] === "delete-proof" ? json({ generation: 8 }) : json({ error: "explicit confirmation required", effects, confirmation: { token: "delete-proof" } }, 422) }]);
  const user = userEvent.setup();
  renderPage(<GeneralSettingsPage />);
  await user.click(await screen.findByRole("button", { name: /Edit/ }));
  await user.click(within(await screen.findByTestId("view-editor")).getByRole("button", { name: "Delete view" }));
  await user.click(within(await screen.findByRole("dialog", { name: "Delete view radarr" })).getByRole("button", { name: "Delete view" }));
  const confirmation = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
  expect(confirmation.textContent).toContain("After a startup fallback");
  await user.click(within(confirmation).getByRole("button", { name: "Confirm and save" }));
  await waitFor(() => expect(fetch.calls("DELETE", "/settings/view-bindings/radarr")).toHaveLength(2));
  const [first, retry] = fetch.calls("DELETE", "/settings/view-bindings/radarr");
  expect(retry.headers["x-confirmation-token"]).toBe("delete-proof");
  expect(retry.search).toBe(first.search);
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Confirm what this change enables" })).toBeNull());
});

describe("Connected apps bindings (moved from Settings › Libraries)", () => {
  it("lists each app's libraries with their state, and links key help to Users & access", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    const row = (await screen.findByText("API key set")).closest("li") as HTMLElement;
    expect(await within(row).findByText("Shadow")).toBeTruthy();
    await user.click(within(row).getByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    expect(within(sheet).getByRole("link", { name: "Users & access" }).getAttribute("href")).toBe("/admin/users");
    expect((within(within(sheet).getByRole("group", { name: "Profile: WEB only" })).getByRole("checkbox", { name: "movie-web" }) as HTMLInputElement).checked).toBe(true);
  });

  it("keeps a saved library no longer in the settings, ticked and marked", async () => {
    const fetch = mockFetch([...routes([{ ...radarr, versions: ["movie-web", "gone-lib"] }]), { method: "PUT", path: "/settings/view-bindings/radarr", reply: { generation: 8 } }]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    const libraries = within(sheet).getByRole("group", { name: "Libraries" });
    expect((within(libraries).getByRole("checkbox", { name: /gone-lib \(not found\)/ }) as HTMLInputElement).checked).toBe(true);
    await user.click(within(libraries).getByRole("checkbox", { name: "tv-web" }));
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/view-bindings/radarr")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[0].body).toMatchObject({ versions: ["movie-web", "gone-lib", "tv-web"] });
  });

  it("saves bindings with their generation; a stale save reloads keeping the ticked libraries", async () => {
    let reads = 0;
    let writes = 0;
    const fetch = mockFetch([
      librariesRoute,
      { method: "GET", path: "/settings", reply: overview },
      { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 7, sections: [] } },
      { method: "GET", path: "/settings/view-bindings", reply: () => ({ source: "db", generation: ++reads === 1 ? 4 : 5, views: [radarr] }) },
      { method: "PUT", path: "/settings/view-bindings/radarr", reply: () => ++writes === 1
        ? json({ error: "settings changed since this generation was read; reload and retry", generation: 5 }, 409)
        : json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("button", { name: /Edit/ }));
    const sheet = await screen.findByTestId("view-editor");
    await user.click(within(within(sheet).getByRole("group", { name: "Libraries" })).getByRole("checkbox", { name: "tv-web" }));
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await within(sheet).findByTestId("settings-error");
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[0].body).toMatchObject({ generation: 4, versions: ["movie-web", "tv-web"] });
    await user.click(within(sheet).getByRole("button", { name: "Reload, keep my changes" }));
    await within(sheet).findByTestId("draft-kept");
    expect((within(within(sheet).getByRole("group", { name: "Libraries" })).getByRole("checkbox", { name: "tv-web" }) as HTMLInputElement).checked).toBe(true);
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/view-bindings/radarr")).toHaveLength(2));
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[1].body).toMatchObject({ generation: 5, versions: ["movie-web", "tv-web"] });
  });

  it("confirms a binding that serves an active library and saves it again with the token", async () => {
    const effect = "Active library movie-web is exposed through view radarr: its arr clients can add titles to movie-web and search for them.";
    let writes = 0;
    const fetch = mockFetch([
      ...routes(),
      { method: "PUT", path: "/settings/view-bindings/radarr", reply: () => ++writes === 1
        ? json({ error: "explicit confirmation required", effects: [{ version: "movie-web", effect }], confirmation: { generation: 7, token: "view-confirmation", effects: [{ version: "movie-web", effect }] } }, 422)
        : json({ generation: 8 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("button", { name: /Edit/ }));
    await user.click(within(await screen.findByTestId("view-editor")).getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effect)).toBeTruthy();
    expect(fetch.calls("PUT", "/settings/view-bindings/radarr")[0].headers["x-confirmation-token"]).toBeUndefined();
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/view-bindings/radarr")).toHaveLength(2));
    const confirmed = fetch.calls("PUT", "/settings/view-bindings/radarr")[1];
    expect(confirmed.headers["x-confirmation-token"]).toBe("view-confirmation");
    expect(confirmed.body).toEqual(fetch.calls("PUT", "/settings/view-bindings/radarr")[0].body);
  });
});
