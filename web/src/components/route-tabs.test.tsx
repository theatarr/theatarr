// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
import { expect, it } from "vitest";
import { renderPage } from "@/test/page";
import { RouteTabs } from "./page";
const tabs = [{ id: "queue", label: "Queue", to: "/activity" }, { id: "history", label: "History", to: "/activity/history?title=4#events" }];
function View() {
 const location = useLocation();
 return <><RouteTabs tabs={tabs} active={location.pathname.endsWith("history") ? "history" : "queue"} /><output>{location.pathname + location.search + location.hash}</output></>;
}
it("uses a labelled select below sm with the same route, query and hash as desktop tabs", async () => {
 const user = userEvent.setup(); renderPage(<View />, "/activity");
 const select = screen.getByRole("combobox", { name: "Section" });
 expect(select.parentElement?.className).toContain("sm:hidden");
 expect(screen.getByRole("tablist").className).toMatch(/(^| )hidden( |$)/);
  expect(screen.getByRole("tablist").className).toContain("sm:flex");
 await user.selectOptions(select, "history");
 expect(screen.getByRole("status").textContent).toBe("/activity/history?title=4#events");
 expect(screen.getByRole("tab", { name: "History" }).getAttribute("aria-selected")).toBe("true");
 await user.click(screen.getByRole("tab", { name: "Queue" }));
 expect(screen.getByRole("status").textContent).toBe("/activity");
 expect((select as HTMLSelectElement).value).toBe("queue");
});
