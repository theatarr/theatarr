// @vitest-environment jsdom
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { toast, Toaster } from "./toaster";

afterEach(() => vi.useRealTimers());

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
