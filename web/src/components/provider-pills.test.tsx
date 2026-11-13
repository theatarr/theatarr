// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { ProviderPills, providerLabel } from "./provider-pills";

afterEach(cleanup);

it("labels the provider codes", () => {
  expect(["rd", "ad", "tb", "pm"].map(providerLabel)).toEqual(["RD", "AD", "TB", "PM"]);
  render(<ProviderPills codes={["rd", "tb"]} testid="pill" />);
  expect(screen.getAllByTestId("pill").map((p) => p.textContent)).toEqual(["RD", "TB"]);
});

it("shows a dash, or the given node, with no provider", () => {
  const { rerender } = render(<ProviderPills codes={[]} />);
  expect(screen.getByText("—")).toBeTruthy();
  rerender(<ProviderPills codes={undefined} empty="Nowhere" />);
  expect(screen.getByText("Nowhere")).toBeTruthy();
});
