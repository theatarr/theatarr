// @vitest-environment jsdom
import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { ControlNote, MonitorSwitch } from "./manage-bits";
import { restrictions } from "@/lib/seasons";

it("says a library no longer in the policy is not in the policy, and gives non-admin managers no settings link", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } }]);
  renderPage(<ControlNote c={{ can_edit: true, live_releases: false, can_search: false, search_reason: "not in the policy", grab_state: { paused: false, pause_forced: false, will_grab: false } }} kind="search" />);
  const note = await screen.findByTestId("control-note");
  expect(note.textContent?.split(" — ")[0]).toBe("Downloads off (not in the policy)");
  expect(within(note).queryByRole("link")).toBeNull();
});

it("states a library no longer in the policy once with the server's reason", () => {
  expect(restrictions([{ label: "Anime", control: { can_edit: true, live_releases: false, can_search: false, search_reason: "not in the policy", grab_state: { paused: false, pause_forced: false, will_grab: false } } }])).toEqual(["Anime: Downloads off (not in the policy) — Settings › Libraries"]);
});

it("states forced Pause once", () => {
  expect(restrictions([{ label: "Anime", control: { can_edit: true, live_releases: false, can_search: false, search_reason: "Paused: nothing is searched or downloaded", grab_state: { paused: true, pause_forced: true, will_grab: false } } }])).toEqual(["Anime: Paused by the environment (THEATARR_FORCE_OFF)"]);
});

it("says forced Pause once in a library's note", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage"] } }]);
  renderPage(<ControlNote kind="search" c={{ can_edit: true, live_releases: false, can_search: false, search_reason: "Paused: nothing is searched or downloaded", grab_state: { paused: true, pause_forced: true, will_grab: false } }} />);
  expect((await screen.findByTestId("control-note")).textContent).toBe("Paused by the environment (THEATARR_FORCE_OFF)");
});


it.each([
  [{ paused: true }, "Paused: nothing is searched or downloaded", "Paused: nothing is searched, downloaded or linked"],
  [{ paused: true, pause_forced: true }, "Paused: nothing is searched or downloaded", "Paused by the environment (THEATARR_FORCE_OFF)"],
] as const)("uses grab state for download notes %j", async (state, search_reason, expected) => {
  mockFetch([]);
  renderPage(<ControlNote kind="search" c={{ can_edit: true, live_releases: false, can_search: false, search_reason,
    grab_state: { pause_forced: false, will_grab: false, ...state } }} />);
  const note = await screen.findByTestId("control-note");
  expect(note.textContent).toContain(expected);
  expect(note.textContent?.split(expected)).toHaveLength(2);
  expect(note.textContent).not.toContain("router.enabled");
  expect(note.textContent).not.toContain("library.enabled");
});

it.each([
  ["not in the policy", "Downloads off (not in the policy)"],
] as const)("links an admin to Libraries for a library the server holds: %s", async (search_reason, text) => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage"] } }]);
  renderPage(<ControlNote kind="search" c={{ can_edit: true, live_releases: false, can_search: false, search_reason,
    grab_state: { paused: false, pause_forced: false, will_grab: false } }} />);
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
