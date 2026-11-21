// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { dismiss, toast, Toaster, toastTop } from "./toaster";
import { Modal } from "./ui/modal";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

/** A Toaster alone on the page, with no toast left from an earlier test (the store lives outside React). */
function fresh() {
  cleanup();
  for (let id = 1; id < 1000; id++) dismiss(id);
  render(
    <MemoryRouter>
      <Toaster />
    </MemoryRouter>,
  );
}

it("keeps a toast 6 s, or as long as its duration says (the removal notice: 10 s, so its link can be followed)", () => {
  vi.useFakeTimers();
  render(
    <MemoryRouter>
      <Toaster />
    </MemoryRouter>,
  );
  act(() => {
    toast({ tone: "ok", title: "short" });
    toast({ tone: "ok", title: "notice", to: "/title/7?tab=history", duration: 10_000 });
  });
  expect(screen.getAllByTestId("toast")).toHaveLength(2);
  act(() => vi.advanceTimersByTime(7_000));
  expect(screen.getAllByTestId("toast").map((t) => t.textContent)).toEqual(["notice"]);
  act(() => vi.advanceTimersByTime(3_500));
  expect(screen.queryAllByTestId("toast")).toHaveLength(0);
});

it("stacks the toasts above the dialogs and sheets (z-[70] to z-[85]) without taking the clicks around them", () => {
  render(
    <MemoryRouter>
      <Toaster />
    </MemoryRouter>,
  );
  act(() => {
    toast({ tone: "bad", title: "Not sent", body: "Already downloading" });
  });
  const card = screen.getAllByTestId("toast").at(-1)!;
  expect(card.parentElement!.className).toContain("z-[95]");
  expect(card.parentElement!.className).toContain("pointer-events-none");
  expect(card.className).toContain("pointer-events-auto");
});

it("holds a toast while it is hovered or focused, and gives it its full time again after", () => {
  vi.useFakeTimers();
  fresh();
  act(() => {
    toast({ tone: "info", title: "held", to: "/activity" });
  });
  const card = screen.getByTestId("toast");
  act(() => vi.advanceTimersByTime(4_000));
  fireEvent.mouseEnter(card);
  act(() => vi.advanceTimersByTime(20_000));
  expect(screen.getAllByTestId("toast")).toHaveLength(1);
  fireEvent.mouseLeave(card);
  fireEvent.focus(screen.getByRole("link"));
  act(() => vi.advanceTimersByTime(20_000));
  expect(screen.getAllByTestId("toast")).toHaveLength(1);
  fireEvent.blur(screen.getByRole("link"));
  act(() => vi.advanceTimersByTime(5_000));
  expect(screen.getAllByTestId("toast")).toHaveLength(1);
  act(() => vi.advanceTimersByTime(1_500));
  expect(screen.queryAllByTestId("toast")).toHaveLength(0);
});

it("counts a repeat of the same words, tone and link on the toast already shown; anything different is its own toast", () => {
  vi.useFakeTimers();
  fresh();
  let first = 0, again = 0;
  act(() => {
    first = toast({ tone: "bad", title: "Not sent", body: "Already downloading", to: "/activity" });
    again = toast({ tone: "bad", title: "Not sent", body: "Already downloading", to: "/activity" });
    toast({ tone: "bad", title: "Not sent", body: "Already downloading", to: "/title/2" });
    toast({ tone: "warn", title: "Not sent", body: "Already downloading", to: "/activity" });
  });
  expect(again).toBe(first);
  const cards = screen.getAllByTestId("toast");
  expect(cards).toHaveLength(3);
  expect(within(cards[0]).getByTestId("toast-count").textContent).toBe("×2");
  expect(within(cards[1]).queryByTestId("toast-count")).toBeNull();
  // A repeat starts the time again.
  act(() => vi.advanceTimersByTime(4_000));
  act(() => { toast({ tone: "bad", title: "Not sent", body: "Already downloading", to: "/activity" }); });
  act(() => vi.advanceTimersByTime(4_000));
  expect(within(screen.getAllByTestId("toast")[0]).getByTestId("toast-count").textContent).toBe("×3");
  act(() => vi.advanceTimersByTime(2_500));
  expect(screen.queryAllByTestId("toast")).toHaveLength(0);
});

it("moves the toasts to the top of the screen while a modal is open, off a dialog's buttons, and back after", () => {
  fresh();
  act(() => {
    toast({ tone: "ok", title: "one" });
    toast({ tone: "ok", title: "two" });
  });
  expect(screen.getByTestId("toasts").dataset.placement).toBe("bottom");
  const dialog = render(<Modal label="Confirm" onClose={() => {}}><button type="button">Confirm</button></Modal>);
  expect(screen.getByTestId("toasts").dataset.placement).toBe("top");
  expect(screen.getByTestId("toasts").className).toContain("top-");
  expect(screen.getByTestId("toasts").className).not.toContain("bottom-");
  dialog.unmount();
  expect(screen.getByTestId("toasts").dataset.placement).toBe("bottom");
});

it("keeps a toast tappable while a modal is open: the stack sits on <body>, not inert, and Dismiss works without closing the dialog", () => {
  fresh();
  const onClose = vi.fn();
  const dialog = render(<Modal label="Confirm" onClose={onClose}><button type="button">Confirm</button></Modal>);
  act(() => {
    toast({ tone: "ok", title: "marked fixed" });
  });
  const stack = screen.getByTestId("toasts");
  expect(stack.parentElement).toBe(document.body);
  expect(stack.closest("[inert]")).toBeNull();
  fireEvent.click(within(stack).getByRole("button", { name: "Dismiss" }));
  expect(screen.queryAllByTestId("toast")).toHaveLength(0);
  expect(onClose).not.toHaveBeenCalled();
  dialog.unmount();
  // One live region: the toast is announced once.
  act(() => {
    toast({ tone: "ok", title: "again" });
  });
  expect(document.querySelectorAll("[aria-live]")).toHaveLength(1);
});

const rect = (top: number, bottom: number) => ({ top, bottom, left: 0, right: 400, width: 400, height: bottom - top, x: 0, y: top }) as DOMRect;
const overlaps = (a: { top: number; bottom: number }, b: { top: number; bottom: number }) => a.top < b.bottom && b.top < a.bottom;

/** A phone-width modal laid out by hand (jsdom has no layout): its panel, header row with Close, and buttons. */
function phoneModal(panel: [number, number], header: [number, number], close: [number, number], buttons: [number, number]) {
  const view = render(
    <Modal label="Confirm" onClose={() => {}} panelTestid="panel">
      <div data-testid="head"><h2>Remove</h2><button type="button" aria-label="Close">x</button></div>
      <div data-testid="buttons"><button type="button">Cancel</button><button type="button">Remove</button></div>
    </Modal>,
  );
  vi.spyOn(screen.getByTestId("panel"), "getBoundingClientRect").mockReturnValue(rect(...panel));
  vi.spyOn(screen.getByTestId("head"), "getBoundingClientRect").mockReturnValue(rect(...header));
  return { view, close: rect(...close), buttons: rect(...buttons) };
}

/** The stack's box as placed: its --toast-top and its height. */
function stackBox(height: number) {
  const top = Number.parseFloat(screen.getByTestId("toasts").style.getPropertyValue("--toast-top"));
  return { top, bottom: top + height };
}

it("puts the toasts above a short confirmation on a phone, over the backdrop, clear of the whole dialog", () => {
  vi.useFakeTimers();
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.dataset.testid === "toasts" ? 80 : 0;
  });
  fresh();
  act(() => {
    toast({ tone: "ok", title: "saved" });
  });
  // A bottom-aligned confirmation with no body (Cancel request): header 560-620, buttons right under it.
  const m = phoneModal([560, 860], [560, 620], [572, 604], [620, 680]);
  act(() => {
    vi.advanceTimersByTime(400);
  });
  const box = stackBox(80);
  expect(screen.getByTestId("toasts").className).toContain("top-[var(--toast-top,1rem)]");
  expect(box.top).toBe(16);
  expect(overlaps(box, { top: 560, bottom: 860 })).toBe(false);
  expect(overlaps(box, m.close) || overlaps(box, m.buttons)).toBe(false);
  m.view.unmount();
});

it("puts the toasts just below a full-height sheet's header row on a phone, clear of its Close and its buttons", () => {
  vi.useFakeTimers();
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.dataset.testid === "toasts" ? 80 : 0;
  });
  fresh();
  act(() => {
    toast({ tone: "ok", title: "saved" });
  });
  // The add sheet at 400 px: from 40 down, header to 180, buttons at the bottom.
  const m = phoneModal([40, 860], [40, 180], [52, 84], [800, 852]);
  act(() => {
    vi.advanceTimersByTime(400);
  });
  const box = stackBox(80);
  expect(box.top).toBe(188);
  expect(overlaps(box, m.close) || overlaps(box, m.buttons)).toBe(false);
  expect(toastTop(40, 180, 80)).toBe(188);
  m.view.unmount();
});

it("places the toasts again when a confirmation over a sheet closes, clear of the sheet's Close", () => {
  vi.useFakeTimers();
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.dataset.testid === "toasts" ? 68 : 0;
  });
  fresh();
  // A full-height sheet (Close 13-45), and over it a short confirmation ("Close without saving?").
  const sheet = render(
    <Modal label="Edit" onClose={() => {}} panelTestid="sheet">
      <div data-testid="sheet-head"><h2>Edit</h2><button type="button" aria-label="Close">x</button></div>
      <div><button type="button">Save</button></div>
    </Modal>,
  );
  vi.spyOn(screen.getByTestId("sheet"), "getBoundingClientRect").mockReturnValue(rect(0, 860));
  vi.spyOn(screen.getByTestId("sheet-head"), "getBoundingClientRect").mockReturnValue(rect(0, 58));
  const confirm = render(
    <Modal label="Close without saving?" onClose={() => {}} panelTestid="confirm">
      <div data-testid="confirm-head"><h2>Close without saving?</h2><button type="button" aria-label="Close">x</button></div>
      <div><button type="button">Cancel</button><button type="button">Close</button></div>
    </Modal>,
  );
  vi.spyOn(screen.getByTestId("confirm"), "getBoundingClientRect").mockReturnValue(rect(600, 860));
  vi.spyOn(screen.getByTestId("confirm-head"), "getBoundingClientRect").mockReturnValue(rect(600, 660));
  act(() => {
    toast({ tone: "ok", title: "saved elsewhere" });
  });
  act(() => {
    vi.advanceTimersByTime(400);
  });
  // Above the confirmation, over the sheet's header: fine while the confirmation is the top modal.
  expect(stackBox(68).top).toBe(16);
  confirm.unmount();
  act(() => {
    vi.advanceTimersByTime(400);
  });
  // The sheet is on top again: the toast moves below its header row, off its Close.
  const box = stackBox(68);
  expect(overlaps(box, rect(13, 45))).toBe(false);
  expect(box.top).toBe(66);
  sheet.unmount();
});

// On a phone (400 px) a long title wraps whole instead of being cut with an ellipsis: its point is often at the end
// ("… is in the library"). Dismiss stays beside it, and a very long toast scrolls inside the screen instead of growing
// past it.
it("wraps a long toast title in full, keeps Dismiss reachable, and keeps the toast inside the screen", () => {
  fresh();
  const title = "Saltmarsh Rangers and the Very Long Subtitle of the Northern Coast: The Complete Collection is in the library";
  act(() => {
    toast({ tone: "ok", title, to: "/title/7" });
  });
  const card = screen.getByTestId("toast");
  const line = within(card).getByText(title);
  const cut = (el: Element) => el.className.split(/\s+/).some((c) => c === "truncate" || /^line-clamp-\d/.test(c));
  expect(cut(line)).toBe(false);
  expect(line.className).toContain("break-words");
  const close = within(card).getByRole("button", { name: "Dismiss" });
  expect(close.className).toContain("shrink-0");
  expect(card.className).toMatch(/max-h-\[/);
  expect(card.className).toContain("overflow-y-auto");
  fireEvent.click(close);
  expect(screen.queryAllByTestId("toast")).toHaveLength(0);
});
