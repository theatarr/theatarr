// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { GlobalSwitchesCard } from "./global-switches";
import { useSettings } from "@/api/settings";
function GeneralSettingsPage() { const q = useSettings(); return q.data ? <GlobalSwitchesCard overview={q.data} /> : null; }
import { json, mockFetch, renderPage } from "@/test/page";

const overview = (source = "db", router = false, forced = false) => ({
  source, writable: source === "db", generation: 7, active_generation: 7, provider_epoch: 0, policy_dir: "/policy", yaml_owned: [], indexers: [], providers: [], budgets: [],
  switches: { router_enabled: router, library_enabled: false, notify_enabled: false, versions: [], sources: { router: forced ? "forced off by environment" : source, library: source, notify: source } },
});

describe("Global switches", () => {
  it("shows DB state and retries the exact enable with its confirmation token", async () => {
    const effects = [{ version: "", effect: "Turn on Allow new downloads (router.enabled): movie-web (downloads on): providers rd." }];
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "PUT", path: "/settings/switches", reply: json({ error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 8, effects } }, 422) },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("switch", { name: "Allow new downloads" }));
    await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(screen.getByText(effects[0].effect)).toBeTruthy();
    expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/switches");
    expect(first.body).toEqual({ generation: 7, router_enabled: true });
    expect(second.body).toEqual(first.body);
    expect(second.headers["x-confirmation-token"]).toBe("proof");
  });

  it("turns off immediately without a confirmation dialog", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview("db", true) },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    renderPage(<GeneralSettingsPage />);
    await userEvent.setup().click(await screen.findByRole("switch", { name: "Allow new downloads" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/switches")[0].body).toEqual({ generation: 7, router_enabled: false });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("disables forced-off and YAML controls and names their sources", async () => {
    mockFetch([{ method: "GET", path: "/settings", reply: overview("db", false, true) }]);
    renderPage(<GeneralSettingsPage />);
    const router = await screen.findByRole("switch", { name: "Allow new downloads" });
    expect((router as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText("Forced off by environment")).toBeTruthy();
    expect((screen.getByRole("switch", { name: "Write library links" }) as HTMLInputElement).disabled).toBe(false);
  });

  it("keeps the switches read-only while settings cannot be written", async () => {
    mockFetch([{ method: "GET", path: "/settings", reply: { ...overview(), writable: false } }]);
    renderPage(<GeneralSettingsPage />);
    await screen.findByRole("switch", { name: "Allow new downloads" });
    for (const input of screen.getAllByRole("switch")) expect((input as HTMLInputElement).disabled).toBe(true);
    expect(screen.queryByText(/policy files/)).toBeNull();
  });
});

it("explains that disabling adds still allows indexer reads", async () => {
 mockFetch([{ method: "GET", path: "/settings", reply: overview() }]);
 renderPage(<GeneralSettingsPage />);
 expect(await screen.findByText("Off: Theatarr adds no torrents to your debrid accounts. RSS checks and comparison searches still read indexers. Each library also has its own switch, Allow downloads for this library.")).toBeTruthy();
});
