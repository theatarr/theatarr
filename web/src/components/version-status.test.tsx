// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router";
import { DownloadsOffNotice, VersionStatus } from "./version-status";

afterEach(cleanup);

describe("VersionStatus", () => {
  const on = { mode: "active", grab: true, router_enabled: true, router_forced_off: false };

  it("shows Downloads on only with every gate open, and no Why?", () => {
    render(<VersionStatus state={on} showMode />);
    expect(screen.getByText("Live")).toBeTruthy();
    expect(screen.getByText("Downloads on").dataset.tone).toBe("ok");
    expect(screen.queryByRole("button", { name: "Why?" })).toBeNull();
    expect(screen.queryByTestId("links-off")).toBeNull();
  });

  const cases: [Partial<typeof on>, string, string[]][] = [
    [{ grab: false }, "Downloads off", ["Downloads are off for this library"]],
    [{ router_enabled: false }, "Paused globally", ["Allow new downloads is off for every library"]],
    [{ router_enabled: false, router_forced_off: true }, "Locked off", ["New downloads are locked off by the environment (THEATARR_FORCE_OFF)"]],
    [{ mode: "shadow" }, "Preview only", ["Watch only: it records what it would download, it downloads nothing"]],
    [{ mode: "off", grab: false, router_enabled: false }, "Downloads off", ["This library is not in the saved settings", "Downloads are off for this library", "Allow new downloads is off for every library"]],
  ];
  it.each(cases)("shows the verdict and every closed gate behind Why? for %j", async (gates, word, closed) => {
    render(<VersionStatus state={{ ...on, ...gates }} showMode />);
    expect(screen.getByText(word)).toBeTruthy();
    expect(screen.queryByText("Downloads on")).toBeNull();
    const why = screen.getByRole("button", { name: "Why?" });
    expect(why.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("state-why")).toBeNull();
    // Keyboard reachable: focus it and press Enter.
    why.focus();
    await userEvent.keyboard("{Enter}");
    expect(why.getAttribute("aria-expanded")).toBe("true");
    const list = screen.getByTestId("state-why");
    expect(list.id).toBe(why.getAttribute("aria-controls"));
    expect([...list.querySelectorAll("li")].map((li) => li.textContent)).toEqual(closed);
  });
});

it("links closed gates to their switches for admins", async () => {
  render(<MemoryRouter><VersionStatus state={{ mode: "shadow", grab: false, router_enabled: false, router_forced_off: false }} showMode canManage settingsKey="movie-web" /></MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  const links = screen.getAllByRole("link");
  expect(links.map((l) => l.getAttribute("href"))).toEqual(["/admin/settings/libraries?edit=movie-web", "/admin/settings/libraries?edit=movie-web", "/admin/settings/downloads"]);
});

it("marks links off separately while downloads stay on", async () => {
  render(<MemoryRouter><VersionStatus canManage showMode state={{ mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: true, library_enabled: false }} /></MemoryRouter>);
  expect(screen.getByText("Downloads on")).toBeTruthy();
  expect(screen.getByTestId("links-off").textContent).toBe("New files won't be linked");
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.getByRole("link", { name: "Settings › Libraries" }).getAttribute("href")).toBe("/admin/settings/libraries");
});

it("links library settings to Libraries without a key, and shows no links to others", async () => {
  render(<MemoryRouter><VersionStatus canManage state={{ mode: "shadow", grab: false, router_enabled: true, router_forced_off: false }} /></MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.getAllByRole("link")[0].getAttribute("href")).toBe("/admin/settings/libraries");
  cleanup();
  render(<VersionStatus state={{ mode: "shadow", grab: false, router_enabled: true, router_forced_off: false }} />);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.queryAllByRole("link")).toHaveLength(0);
});

it("keeps the gates reachable by keyboard in dense rows, with admin links, and Escape returns focus", async () => {
  render(<MemoryRouter><VersionStatus why={false} canManage settingsKey="movie-web" state={{ mode: "active", grab: false, router_enabled: true, router_forced_off: false }} /></MemoryRouter>);
  expect(screen.queryByRole("button", { name: "Why?" })).toBeNull();
  expect(screen.getByText("Downloads off").title).toBe("Downloads are off for this library.");
  const why = screen.getByRole("button", { name: "Why Downloads off?" });
  await userEvent.tab();
  expect(document.activeElement).toBe(why);
  await userEvent.keyboard("{Enter}");
  expect(why.getAttribute("aria-expanded")).toBe("true");
  const list = await screen.findByTestId("state-why");
  expect(document.getElementById(why.getAttribute("aria-controls") ?? "")?.contains(list)).toBe(true);
  const link = screen.getByRole("link", { name: "Settings › Libraries" });
  expect(link.getAttribute("href")).toBe("/admin/settings/libraries?edit=movie-web");
  // Escape from inside the list (a settings link) closes it and puts focus back on the button.
  link.focus();
  await userEvent.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByTestId("state-why")).toBeNull());
  expect(document.activeElement).toBe(why);
});

it("shows no dense Why? button when downloads are on", () => {
  render(<VersionStatus why={false} state={{ mode: "active", grab: true, router_enabled: true, router_forced_off: false }} />);
  expect(screen.queryByRole("button")).toBeNull();
});

it("says global downloads off once, with the settings link for admins", () => {
  const { rerender } = render(<MemoryRouter><DownloadsOffNotice off="off" canManage /></MemoryRouter>);
  expect(screen.getByTestId("downloads-off-notice").textContent).toContain("Allow new downloads is off");
  expect(screen.getByRole("link").getAttribute("href")).toBe("/admin/settings/downloads");
  rerender(<MemoryRouter><DownloadsOffNotice off="locked" /></MemoryRouter>);
  expect(screen.getByTestId("downloads-off-notice").textContent).toContain("locked off");
  expect(screen.queryByRole("link")).toBeNull();
  rerender(<MemoryRouter><DownloadsOffNotice /></MemoryRouter>);
  expect(screen.queryByTestId("downloads-off-notice")).toBeNull();
});
