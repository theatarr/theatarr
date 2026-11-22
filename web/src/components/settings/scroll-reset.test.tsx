// @vitest-environment jsdom
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useNavigate } from "react-router";
import { expect, it, vi } from "vitest";
import { SettingsScrollReset } from "./scroll-reset";

function Go() {
  const navigate = useNavigate();
  return (
    <>
      {["/admin/settings", "/admin/settings/import-lists", "/admin/settings/anime-mappings", "/admin/advanced", "/admin/advanced/history", "/admin/settings/formats?edit=x", "/admin/settings/plex#plex", "/activity"].map((to) => (
        <button key={to} type="button" onClick={() => navigate(to)}>{to}</button>
      ))}
      <button type="button" onClick={() => navigate(-1)}>back</button>
    </>
  );
}

it("opens every Settings and Advanced page at its top, but not for a #hash, Back, the same page or another page (F22 round 2)", () => {
  const scroll = vi.fn();
  vi.stubGlobal("scrollTo", scroll);
  render(<MemoryRouter initialEntries={["/admin/settings/formats"]}><SettingsScrollReset /><Go /></MemoryRouter>);
  const go = (to: string) => { scroll.mockClear(); fireEvent.click(screen.getByRole("button", { name: to })); return scroll.mock.calls.length; };
  for (const to of ["/admin/settings", "/admin/settings/import-lists", "/admin/settings/anime-mappings", "/admin/advanced", "/admin/advanced/history"]) {
    expect(go(to), to).toBe(1);
  }
  expect(go("/admin/settings/formats?edit=x")).toBe(1);
  expect(go("/admin/settings/formats?edit=x")).toBe(0); // the same page
  expect(go("/admin/settings/plex#plex")).toBe(0);
  expect(go("back")).toBe(0);
  expect(go("/activity")).toBe(0);
  vi.unstubAllGlobals();
});
