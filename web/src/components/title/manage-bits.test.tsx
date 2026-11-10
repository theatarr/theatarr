// @vitest-environment jsdom
import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { ControlNote, MonitorSwitch } from "./manage-bits";
import { restrictions } from "@/lib/seasons";

it("says a library no longer in the policy is not in the policy, not shadow, and gives non-admin managers no settings link", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } }]);
  renderPage(<ControlNote c={{ mode: "shadow", can_edit: true, live_releases: false, can_search: false, search_reason: "not in the policy", grab_state: { mode: "off", grab: false, router_enabled: false, router_forced_off: false, will_grab: false, library_enabled: true } }} kind="search" />);
  const note = await screen.findByTestId("control-note");
  expect(note.textContent?.split(" — ")[0]).toBe("not in the policy");
  expect(within(note).queryByRole("link")).toBeNull();
});

it("states a library no longer in the policy once with the server's reason", () => {
  expect(restrictions([{ label: "Anime", control: { mode: "shadow", can_edit: true, live_releases: false, can_search: false, search_reason: "not in the policy", grab_state: { mode: "off", grab: false, router_enabled: true, router_forced_off: false, will_grab: false, library_enabled: true } } }])).toEqual(["Anime: not in the policy — Settings › Libraries"]);
});

it("states forced-off once", () => {
  expect(restrictions([{ label: "Anime", control: { mode: "active", can_edit: true, live_releases: false, can_search: false, search_reason: "router.enabled is false: nothing is searched or grabbed", grab_state: { mode: "active", grab: true, router_enabled: true, router_forced_off: true, will_grab: false, library_enabled: true } } }])).toEqual(["Anime: Locked off (new downloads are forced off by the environment) — Settings › Download clients"]);
});


it.each([
  [{ mode: "active", router_enabled: false }, "router.enabled is false: nothing is searched or grabbed", "Paused globally (Allow new downloads is off)"],
  [{ mode: "active", router_forced_off: true }, "router.enabled is false: nothing is searched or grabbed", "Locked off (new downloads are forced off by the environment)"],
  [{ mode: "shadow", router_enabled: false }, "shadow mode: nothing is searched or grabbed for this version", "Preview only (this library is Watch only: it records what it would download)"],
  [{ mode: "active", grab: false }, "version grab is off: nothing is searched or grabbed", "Downloads off (downloads are off for this library)"],
] as const)("uses grab state for download notes %j", async (state, search_reason, expected) => {
  mockFetch([]);
  renderPage(<ControlNote kind="search" c={{ mode: state.mode, can_edit: true, live_releases: false, can_search: false, search_reason,
    grab_state: { grab: true, router_enabled: true, router_forced_off: false, library_enabled: true, will_grab: false, ...state } }} />);
  const note = await screen.findByTestId("control-note");
  expect(note.textContent).toContain(expected);
  expect(note.textContent?.split(expected)).toHaveLength(2);
  expect(note.textContent).not.toContain("router.enabled");
  expect(note.textContent).not.toContain("library.enabled");
});

it.each([
  ["shadow", "shadow mode: nothing is searched or grabbed for this version", "Preview only (this library is Watch only: it records what it would download)"],
  ["off", "not in the policy", "not in the policy"],
] as const)("links an admin to Libraries for a library in mode %s while downloads are off globally", async (mode, search_reason, text) => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage"] } }]);
  renderPage(<ControlNote kind="search" c={{ mode: "shadow", can_edit: true, live_releases: false, can_search: false, search_reason,
    grab_state: { mode, grab: false, router_enabled: false, router_forced_off: false, will_grab: false, library_enabled: true } }} />);
  const link = await screen.findByRole("link", { name: "Settings › Libraries" });
  expect(link.getAttribute("href")).toBe("/admin/settings/libraries");
  expect(screen.getByTestId("control-note").textContent).toBe(`${text} — Settings › Libraries`);
});

it("toggles a monitor switch without the click reaching the row, and keeps a locked one focusable with its reason", () => {
  const onChange = vi.fn();
  const onRow = vi.fn();
  const { rerender } = render(<div onClick={onRow}><MonitorSwitch on={false} onChange={onChange} label="Monitor Alpha 4K" size="sm" /></div>);
  fireEvent.click(screen.getByRole("switch", { name: "Monitor Alpha 4K" }));
  expect(onChange).toHaveBeenCalledWith(true);
  expect(onRow).not.toHaveBeenCalled();
  rerender(<div onClick={onRow}><MonitorSwitch on onChange={onChange} label="Monitor Alpha 4K" disabled reason="managed by the arr" /></div>);
  const locked = screen.getByRole("switch", { name: "Monitor Alpha 4K" }) as HTMLButtonElement;
  expect(locked.getAttribute("aria-disabled")).toBe("true");
  expect(locked.disabled).toBe(false);
  expect(locked.title).toBe("managed by the arr");
  fireEvent.click(locked);
  expect(onChange).toHaveBeenCalledTimes(1);
});
