// @vitest-environment jsdom
// The risky-change confirmation: the server's effects word for word, grouped by library, the other
// libraries a change touches named apart, Cancel focused. Invented keys.
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { EffectsConfirmDialog, groupEffects } from "./confirm-write";

afterEach(cleanup);

const scope = { title: "Confirm library change", does: [], keeps: ["Existing links stay."], confirm: "Confirm this change" };
const effects = [
  { version: "movie-web", effect: "Grab for movie-web: grab false → true." },
  { version: "", effect: "Enable router.enabled: new downloads may be added." },
  { version: "movie-4k", effect: "movie-4k can now grab from provider rd." },
  { version: "movie-web", effect: "movie-web follows movie-4k." },
];

it("groups the server's effects by library, global ones first, in the server's words", () => {
  expect(groupEffects(effects)).toEqual([
    { version: "", effects: ["Enable router.enabled: new downloads may be added."] },
    { version: "movie-web", effects: ["Grab for movie-web: grab false → true.", "movie-web follows movie-4k."] },
    { version: "movie-4k", effects: ["movie-4k can now grab from provider rd."] },
  ]);
});

it("names the libraries affected and the other libraries apart, with Cancel focused", async () => {
  const onConfirm = vi.fn();
  const onClose = vi.fn();
  render(<EffectsConfirmDialog effects={effects} library="movie-web" scope={scope} onConfirm={onConfirm} onClose={onClose} />);
  const dialog = screen.getByRole("dialog", { name: "Confirm library change" });
  for (const e of effects) expect(within(dialog).getByText(e.effect)).toBeTruthy();
  expect(screen.getByTestId("confirm-libraries").textContent).toBe("Libraries affected: movie-web, movie-4k");
  expect(screen.getByTestId("confirm-others").textContent).toBe("Also affects other libraries: movie-4k");
  const groups = screen.getAllByTestId("confirm-group");
  expect(groups.map((g) => g.querySelector("h3")?.textContent)).toEqual(["This change", "movie-web (this library)", "movie-4k"]);
  const cancel = within(dialog).getByRole("button", { name: "Cancel" });
  await waitFor(() => expect(document.activeElement).toBe(cancel));
  await userEvent.click(within(dialog).getByRole("button", { name: "Confirm this change" }));
  expect(onConfirm).toHaveBeenCalledOnce();
});

it("says nothing about other libraries when the change is about none or only its own", () => {
  render(<EffectsConfirmDialog effects={[effects[0]]} library="movie-web" scope={scope} onConfirm={() => {}} onClose={() => {}} />);
  expect(screen.queryByTestId("confirm-others")).toBeNull();
  cleanup();
  render(<EffectsConfirmDialog effects={[effects[1]]} scope={scope} onConfirm={() => {}} onClose={() => {}} />);
  expect(screen.queryByTestId("confirm-libraries")).toBeNull();
  expect(screen.queryByTestId("confirm-others")).toBeNull();
});
