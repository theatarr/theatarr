// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { dismiss, toast, Toaster } from "./toaster";

afterEach(() => vi.useRealTimers());

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
