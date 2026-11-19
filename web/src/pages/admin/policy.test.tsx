// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { PolicyFiles } from "./policy";

it("says the policy files are read only by an import", async () => {
  mockFetch([{ method: "GET", path: "/policy", reply: { dir: "/config/policy", versions: [], indexers: [], outbound_hosts: [], budgets: [], files: [] } }]);
  renderPage(<PolicyFiles />);
  const intro = await screen.findByText(/^Policy directory:/);
  expect(intro.textContent).toContain("The files here are imported only when you run an import (Import & export).");
  expect(intro.textContent).not.toContain("first start");
  expect(intro.textContent).toContain("Policy directory: /config/policy. Indexers, providers, budgets, quality profiles (with their format sets), custom formats, naming and libraries are managed on their own tabs.");
  expect(intro.textContent).not.toContain("DB settings");
});

it("offers validation only when the policy directory has YAML files", async () => {
  mockFetch([{ method: "GET", path: "/policy", reply: { dir: "/config/policy", versions: [], indexers: [], outbound_hosts: [], budgets: [], files: [] } }]);
  const { unmount } = renderPage(<PolicyFiles />);
  await screen.findByText(/^Policy directory:/);
  expect(screen.queryByRole("button", { name: /validate/i })).toBeNull();
  expect(screen.getByText("No YAML files in the policy directory.")).toBeTruthy();
  unmount();
  mockFetch([{ method: "GET", path: "/policy", reply: { dir: "/config/policy", versions: [], indexers: [], outbound_hosts: [], budgets: [], files: ["versions.yaml"] } }]);
  renderPage(<PolicyFiles />);
  expect(await screen.findByRole("button", { name: "Validate YAML files" })).toBeTruthy();
});
