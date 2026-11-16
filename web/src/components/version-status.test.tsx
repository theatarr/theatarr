// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router";
import { DownloadsOffNotice, VersionStatus } from "./version-status";

afterEach(cleanup);

describe("VersionStatus", () => {
  const on = { paused: false, pause_forced: false };

  it("shows Downloads on only with every gate open, and no Why?", () => {
    render(<VersionStatus state={on} />);
    expect(screen.getByText("Downloads on").dataset.tone).toBe("ok");
    expect(screen.queryByRole("button", { name: "Why?" })).toBeNull();
    expect(screen.queryByTestId("links-off")).toBeNull();
  });

  it("quiet: says nothing while all is well, and never Downloads on beside a closed gate", () => {
    const { container } = render(<VersionStatus state={on} quiet />);
    expect(container.textContent).toBe("");
    cleanup();
    render(<MemoryRouter><VersionStatus state={{ ...on, will_grab: true, library_writes: false }} quiet /></MemoryRouter>);
    expect(screen.getByTestId("links-off")).toBeTruthy();
    expect(screen.queryByText("Downloads on")).toBeNull();
    cleanup();
    render(<MemoryRouter><VersionStatus state={{ ...on, paused: true }} quiet /></MemoryRouter>);
    expect(screen.getByText("Paused")).toBeTruthy();
  });

  const cases: [Partial<typeof on & { will_grab: boolean }>, string, string[]][] = [
    [{ paused: true }, "Paused", ["Paused: turn Pause off at the top of the page"]],
    [{ paused: true, pause_forced: true }, "Paused", ["Paused by the environment (THEATARR_FORCE_OFF)"]],
    [{ will_grab: false }, "Downloads off", ["Automatic downloads are paused by the server"]],
  ];
  it.each(cases)("shows the verdict and every closed gate behind Why? for %j", async (gates, word, closed) => {
    render(<VersionStatus state={{ ...on, ...gates }} />);
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

it("links closed gates to where they are changed for admins; Pause has no link", async () => {
  render(<MemoryRouter><VersionStatus state={{ paused: false, pause_forced: false, will_grab: false }} canManage settingsKey="movie-web" /></MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  const links = screen.getAllByRole("link");
  expect(links.map((l) => l.getAttribute("href"))).toEqual(["/admin/settings/libraries?edit=movie-web"]);
  cleanup();
  render(<MemoryRouter><VersionStatus state={{ paused: true, pause_forced: false }} canManage settingsKey="movie-web" /></MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.queryAllByRole("link")).toHaveLength(0);
});

it("marks links off separately while downloads stay on", async () => {
  render(<MemoryRouter><VersionStatus canManage state={{ paused: false, pause_forced: false, will_grab: true, library_writes: false }} /></MemoryRouter>);
  expect(screen.getByText("Downloads on")).toBeTruthy();
  expect(screen.getByTestId("links-off").textContent).toBe("New files won't be linked");
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.getByRole("link", { name: "Settings › Libraries" }).getAttribute("href")).toBe("/admin/settings/libraries");
});

it("links library settings to Libraries without a key, and shows no links to others", async () => {
  render(<MemoryRouter><VersionStatus canManage state={{ paused: false, pause_forced: false, will_grab: false }} /></MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.getAllByRole("link")[0].getAttribute("href")).toBe("/admin/settings/libraries");
  cleanup();
  render(<VersionStatus state={{ paused: false, pause_forced: false, will_grab: false }} />);
  await userEvent.click(screen.getByRole("button", { name: "Why?" }));
  expect(screen.queryAllByRole("link")).toHaveLength(0);
});

it("keeps the gates reachable by keyboard in dense rows, with admin links, and Escape returns focus", async () => {
  render(<MemoryRouter><VersionStatus why={false} canManage settingsKey="movie-web" state={{ paused: false, pause_forced: false, will_grab: false }} /></MemoryRouter>);
  expect(screen.queryByRole("button", { name: "Why?" })).toBeNull();
  expect(screen.getByText("Downloads off").title).toBe("Automatic downloads are paused by the server.");
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
  render(<VersionStatus why={false} state={{ paused: false, pause_forced: false }} />);
  expect(screen.queryByRole("button")).toBeNull();
});

it("says Pause once, pointing admins at the header", () => {
  const { rerender } = render(<MemoryRouter><DownloadsOffNotice off="off" canManage /></MemoryRouter>);
  expect(screen.getByTestId("downloads-off-notice").textContent).toBe("Paused: nothing is searched, downloaded or linked for any library. Turn Pause off at the top of the page to resume.");
  rerender(<MemoryRouter><DownloadsOffNotice off="locked" canManage /></MemoryRouter>);
  expect(screen.getByTestId("downloads-off-notice").textContent).toBe("Paused by the environment (THEATARR_FORCE_OFF): nothing is searched, downloaded or linked for any library.");
  expect(screen.queryByRole("link")).toBeNull();
  rerender(<MemoryRouter><DownloadsOffNotice /></MemoryRouter>);
  expect(screen.queryByTestId("downloads-off-notice")).toBeNull();
});
