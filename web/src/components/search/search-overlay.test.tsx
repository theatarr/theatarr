// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { useLocation } from "react-router";
import { expect, it } from "vitest";
import { SearchProvider, useSearchUI } from "./search-context";
import { mockFetch, renderPage } from "@/test/page";

function Opener() {
  const { openOverlay } = useSearchUI();
  useEffect(() => openOverlay("Signal"), [openOverlay]);
  return null;
}

function Where() {
  return <p data-testid="where">{useLocation().pathname + useLocation().hash}</p>;
}

it("closes the overlay when an admin follows the TMDb key link", async () => {
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["browse", "add", "admin"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library") ? { library: [], library_total: 0 } : { tmdb_state: "off" }) },
    { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  ]);
  renderPage(<SearchProvider><Opener /><Where /></SearchProvider>, "/");
  const link = await screen.findByRole("link", { name: "Set the TMDb key" });
  await user.click(link);
  await waitFor(() => expect(screen.queryByTestId("search-overlay")).toBeNull());
  expect(screen.getByTestId("where").textContent).toBe("/admin/settings/metadata#sources");
});
