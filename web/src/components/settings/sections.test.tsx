// @vitest-environment jsdom
import { focusManager } from "@tanstack/react-query";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { SectionsCard } from "./sections";
import { useSettings } from "@/api/settings";
function GeneralSettingsPage() { const q = useSettings(); return q.data ? <SectionsCard readOnly={!q.data.writable} /> : null; }
import { json, mockFetch, renderPage } from "@/test/page";

const overview = (source = "db") => ({
  source, writable: true, generation: 7, active_generation: 7, provider_epoch: 0, policy_dir: "/policy", yaml_owned: [], indexers: [], providers: [], budgets: [],
  switches: { router_enabled: false, library_enabled: false, notify_enabled: false, versions: [], sources: { router: source, library: source, notify: source } },
});

const libraryYAML = "mount:\n  path: /mnt/debrid\n  link_path: /mnt/debrid\nplex:\n  url: http://plex.example:32400\n  token_env: THEATARR_PLEX_TOKEN\n";
const sections = (source = "db") => ({
  source, generation: 7,
  sections: [
    { key: "library", live: true, config: {}, yaml: libraryYAML, secrets: [{ path: "plex.token", set: false, env: "THEATARR_PLEX_TOKEN" }] },
    { key: "egress", live: true, config: {}, yaml: "check_url: https://check.example\n", secrets: [] },
    // Every section the server lists is live; the restart-needed rendering stays for a server that says otherwise.
    { key: "ui", live: false, config: {}, yaml: "images:\n  original: false\n", secrets: [] },
  ],
});
const views = { source: "db", generation: 7, views: [] };

const routes = (source = "db") => [
  { method: "GET", path: "/settings", reply: overview(source) },
  { method: "GET", path: "/settings/sections", reply: sections(source) },
  { method: "GET", path: "/settings/view-bindings", reply: { ...views, source } },
];

const row = async (key: string) => {
  await screen.findByTestId("settings-sections");
  return document.querySelector(`li[data-section="${key}"]`) as HTMLElement;
};

async function openSection(key: string) {
  const user = userEvent.setup();
  renderPage(<GeneralSettingsPage />);
  await user.click(within(await row(key)).getByRole("button"));
  return { user, sheet: await screen.findByTestId("section-editor") };
}

describe("Section editors", () => {
  it("lists each section with live or restart needed", async () => {
    mockFetch(routes());
    renderPage(<GeneralSettingsPage />);
    const library = await row("library");
    expect(within(library).getByText("live")).toBeTruthy();
    expect(within(library).getByRole("button", { name: "Edit as YAML Library and Plex" })).toBeTruthy();
    expect(within(await row("egress")).getByText("live")).toBeTruthy();
    expect(within(await row("ui")).getByText("restart needed")).toBeTruthy();
  });

  it("saves the YAML and a write-only credential, confirms a risky change and says it applied", async () => {
    const effects = [{ version: "", effect: "Change where files are read or written: library.mount.link_path /mnt/debrid → /mnt/other." }];
    const fetch = mockFetch([
      ...routes(),
      { method: "PUT", path: "/settings/sections/library", reply: json({ error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 8, effects } }, 422) },
      { method: "PUT", path: "/settings/sections/library", reply: { generation: 8, revision: 1, restart_required: false } },
    ]);
    const { user, sheet } = await openSection("library");
    expect(within(sheet).getByText(/Applies without a restart/)).toBeTruthy();
    expect(within(sheet).getByText("THEATARR_PLEX_TOKEN")).toBeTruthy();
    const yaml = within(sheet).getByTestId("section-yaml") as HTMLTextAreaElement;
    await user.clear(yaml);
    await user.paste("mount:\n  link_path: /mnt/other\n");
    await user.click(within(sheet).getByTestId("secret-replace"));
    await user.click(within(sheet).getByTestId("secret-input"));
    await user.paste("plex-token-value");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(screen.getByText(effects[0].effect)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/library")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/sections/library");
    expect(first.body).toEqual({ generation: 7, yaml: "mount:\n  link_path: /mnt/other\n", secrets: { "plex.token": "plex-token-value" } });
    expect(second.body).toEqual(first.body);
    expect(second.headers["x-confirmation-token"]).toBe("proof");
    expect((await screen.findByTestId("sections-notice")).textContent).toContain("Library and Plex saved and applied.");
  });

  it("says a restart is needed when the server answers restart_required", async () => {
    mockFetch([
      ...routes(),
      { method: "PUT", path: "/settings/sections/ui", reply: { generation: 8, revision: 1, restart_required: true } },
    ]);
    const { user, sheet } = await openSection("ui");
    expect(within(sheet).getByText(/Restart needed/)).toBeTruthy();
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    expect((await screen.findByTestId("sections-notice")).textContent).toContain("Restart Theatarr");
  });

  it("clears a stored credential", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sections", reply: { ...sections(), sections: [{ ...sections().sections[0], secrets: [{ path: "plex.token", set: true, env: "THEATARR_PLEX_TOKEN" }] }] } },
      { method: "GET", path: "/settings/view-bindings", reply: views },
      { method: "PUT", path: "/settings/sections/library", reply: { generation: 8, revision: 1, restart_required: false } },
    ]);
    const { user, sheet } = await openSection("library");
    await user.click(within(sheet).getByTestId("secret-clear"));
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/library")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/sections/library")[0].body).toEqual({ generation: 7, yaml: libraryYAML, clear_secrets: ["plex.token"] });
  });

  it("shows a validation refusal in the sheet", async () => {
    mockFetch([
      ...routes(),
      { method: "PUT", path: "/settings/sections/egress", reply: json({ error: "egress.check_url: must be an http(s) URL" }, 422) },
    ]);
    const { user, sheet } = await openSection("egress");
    expect(within(sheet).getByText(/Applies without a restart/)).toBeTruthy();
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    expect((await within(sheet).findByTestId("settings-error")).textContent).toContain("must be an http(s) URL");
  });

  it("keeps the generation it opened with: a change elsewhere is a 409 with an explicit reload, not an overwrite", async () => {
    let gen = 7;
    const otherYAML = "check_url: https://other.example\n";
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sections", reply: () => {
        const s = sections();
        return gen === 7 ? s : { ...s, generation: gen, sections: s.sections.map((x) => x.key === "egress" ? { ...x, yaml: otherYAML } : x) };
      } },
      { method: "GET", path: "/settings/view-bindings", reply: views },
      { method: "PUT", path: "/settings/sections/egress", reply: json({ error: "settings changed: generation is 8, not 7", generation: 8 }, 409) },
      { method: "PUT", path: "/settings/sections/egress", reply: { generation: 9, revision: 2, restart_required: false } },
    ]);
    const { user, sheet } = await openSection("egress");
    // Another admin saves; this tab refetches on focus.
    gen = 8;
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    await waitFor(() => expect(fetch.calls("GET", "/settings/sections").length).toBeGreaterThan(1));
    const yaml = within(sheet).getByTestId("section-yaml") as HTMLTextAreaElement;
    expect(yaml.value).toBe("check_url: https://check.example\n");
    await user.clear(yaml);
    await user.paste("check_url: https://mine.example\n");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    const error = await within(sheet).findByTestId("settings-error");
    expect(error.getAttribute("data-kind")).toBe("stale");
    expect(fetch.calls("PUT", "/settings/sections/egress")[0].body).toEqual({ generation: 7, yaml: "check_url: https://mine.example\n" });
    // Discard: the draft becomes the current section.
    await user.click(within(sheet).getByTestId("settings-reload"));
    await waitFor(() => expect(yaml.value).toBe(otherYAML));
    await user.clear(yaml);
    await user.paste("check_url: https://mine.example\n");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/egress")).toHaveLength(2));
    expect(fetch.calls("PUT", "/settings/sections/egress")[1].body).toEqual({ generation: 8, yaml: "check_url: https://mine.example\n" });
  });

  it("reloads after a conflict keeping the draft when asked", async () => {
    const fetch = mockFetch([
      ...routes(),
      { method: "PUT", path: "/settings/sections/egress", reply: json({ error: "settings changed", generation: 8 }, 409) },
      { method: "PUT", path: "/settings/sections/egress", reply: { generation: 9, revision: 2, restart_required: false } },
    ]);
    const { user, sheet } = await openSection("egress");
    const yaml = within(sheet).getByTestId("section-yaml") as HTMLTextAreaElement;
    await user.clear(yaml);
    await user.paste("check_url: https://mine.example\n");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await user.click(await within(sheet).findByTestId("settings-reload-keep"));
    await within(sheet).findByTestId("draft-kept");
    expect(yaml.value).toBe("check_url: https://mine.example\n");
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/egress")).toHaveLength(2));
    // The fixture still answers generation 7: the save goes at the generation the reload read.
    expect(fetch.calls("PUT", "/settings/sections/egress")[1].body).toEqual({ generation: 7, yaml: "check_url: https://mine.example\n" });
  });

  it("keeps the sections read-only while settings cannot be written", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/settings", reply: { ...overview(), writable: false } }, ...routes().slice(1)]);
    const { sheet } = await openSection("library");
    expect((within(sheet).getByTestId("section-yaml") as HTMLTextAreaElement).disabled).toBe(true);
    expect(within(sheet).queryByRole("button", { name: "Save" })).toBeNull();
    expect(fetch.requests.filter((r) => r.method !== "GET")).toHaveLength(0);
  });
});
