// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PauseSwitch } from "./global-switches";
import { json, mockFetch, renderPage } from "@/test/page";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";

/** Reads what Wanted and the status read, under their keys. */
function StatusAndWanted() {
  useQuery({ queryKey: qk.status, queryFn: () => api("/status") });
  useQuery({ queryKey: ["wanted", {}], queryFn: () => api("/wanted") });
  return null;
}

const overview = (paused: boolean, extra: Record<string, unknown> = {}, pause_forced = false) => ({
  source: "db", writable: true, generation: 7, active_generation: 7, provider_epoch: 0, policy_dir: "/policy", yaml_owned: [], indexers: [], providers: [], budgets: [],
  switches: { paused, pause_forced, versions: [] }, ...extra,
});

describe("Pause in the admin header", () => {
  it("on: sends paused in one save, without a dialog", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/settings", reply: overview(false) }, { method: "PUT", path: "/settings/switches", reply: { generation: 8 } }]);
    renderPage(<PauseSwitch />);
    const pause = await screen.findByRole("switch", { name: "Pause" });
    expect((pause as HTMLInputElement).checked).toBe(false);
    expect(screen.getByTestId("pause").getAttribute("title")).toBe("Pause stops searches, new downloads, list syncs and changes in your library folders, for every library. Downloads already started finish at the provider and are imported when you turn Pause off.");
    await userEvent.setup().click(pause);
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/switches")[0].body).toEqual({ generation: 7, paused: true });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a save reads the status and Wanted again, so due rows and search buttons follow Pause", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview(false) },
      { method: "GET", path: "/status", reply: {} },
      { method: "GET", path: "/wanted", reply: { items: [] } },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    renderPage(<><PauseSwitch /><StatusAndWanted /></>);
    const pause = await screen.findByRole("switch", { name: "Pause" });
    await waitFor(() => expect(fetch.calls("GET", "/wanted")).toHaveLength(1));
    expect(fetch.calls("GET", "/status")).toHaveLength(1);
    await userEvent.setup().click(pause);
    await waitFor(() => expect(fetch.calls("GET", "/wanted")).toHaveLength(2));
    expect(fetch.calls("GET", "/status")).toHaveLength(2);
  });

  it("off: goes through the confirmation showing the server's effect as given", async () => {
    const effects = [{ effect: "Turn Pause off: Live libraries search, download and link again: movie-web, tv-web. Waiting downloads are imported." }];
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview(true) },
      { method: "PUT", path: "/settings/switches", reply: json({ error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 8, effects } }, 422) },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    const user = userEvent.setup();
    renderPage(<PauseSwitch />);
    const pause = await screen.findByRole("switch", { name: "Pause" });
    expect((pause as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText("Paused")).toBeTruthy();
    expect(screen.getByTestId("pause").getAttribute("title")).toBe("Paused: nothing is searched, downloaded or linked. Turn off to resume.");
    await user.click(pause);
    await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(screen.getByText(effects[0].effect)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/switches");
    expect(first.body).toEqual({ generation: 7, paused: false });
    expect(second.body).toEqual(first.body);
    expect(second.headers["x-confirmation-token"]).toBe("proof");
  });

  it("is disabled while settings are read-only", async () => {
    mockFetch([{ method: "GET", path: "/settings", reply: overview(false, { writable: false }) }]);
    renderPage(<PauseSwitch />);
    expect((await screen.findByRole("switch", { name: "Pause" }) as HTMLInputElement).disabled).toBe(true);
  });

  it("forced by the environment: a locked pill, no switch", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/settings", reply: overview(true, {}, true) }]);
    renderPage(<PauseSwitch />);
    const pill = await screen.findByTestId("pause");
    expect(pill.textContent).toBe("Paused (locked)");
    expect(pill.getAttribute("title")).toBe("Paused by THEATARR_FORCE_OFF. Remove it and restart to resume.");
    expect(screen.queryByRole("switch")).toBeNull();
    expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(0);
  });
});
