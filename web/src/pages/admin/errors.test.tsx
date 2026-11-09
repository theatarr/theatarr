// @vitest-environment jsdom
// A row has a status field, so its replies go in { body } (mockFetch reads a top-level status as the HTTP status).
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";
import { ErrorsPage } from "./errors";

const row = {
  id: 7,
  fingerprint: "0123456789abcdef0123456789abcdef",
  source: "panic",
  component: "jobs",
  level: "ERROR",
  message: "runtime error: index out of range [<n>] with length <n>",
  sample: "runtime error: index out of range [3] with length 2",
  context: {},
  count: 4,
  first_seen: "2026-10-30T10:00:00Z",
  last_seen: "2026-10-30T11:00:00Z",
  status: "new",
  regressions: 1,
  first_version: "v1.0.0",
  last_version: "v1.0.1",
  note: "",
  updated_at: "2026-10-30T11:00:00Z",
};
const page = { items: [row], total: 1, counts: { new: 1, seen: 0, resolved: 2, ignored: 0 }, drops: 3 };

it("lists grouped errors, opens one with its stack and resolves it", async () => {
  const api = mockFetch([
    { method: "GET", path: "/errors", reply: page },
    { method: "GET", path: "/errors/7", reply: { body: { ...row, context: { kind: "meta.art", job: "12" }, stack: "goroutine 9 [running]:\ngithub.com/theatarr/theatarr/internal/jobs.(*Worker).safeRun()" } } },
    { method: "POST", path: "/errors/7", reply: { body: { ...row, status: "resolved", resolved_at: "2026-10-30T12:00:00Z" } } },
  ]);
  renderPage(<ErrorsPage />);
  expect(await screen.findByText("runtime error: index out of range [3] with length 2")).toBeTruthy();
  expect(api.calls("GET", "/errors")[0].search).toBe("?status=open&limit=100");
  expect(screen.getByTestId("errors-new").textContent).toBe("1");
  expect(screen.getByTestId("errors-drops").textContent).toBe("3");
  expect(screen.getByTestId("error-count").textContent).toBe("4×");
  expect(screen.getByText("came back 1×")).toBeTruthy();
  expectNamedControls();

  fireEvent.click(screen.getByTestId("error-open-7"));
  expect((await screen.findByTestId("error-stack")).textContent).toContain("safeRun");
  expect(screen.getByText("meta.art")).toBeTruthy();

  fireEvent.click(screen.getByTestId("error-resolve-7"));
  await waitFor(() => expect(api.calls("POST", "/errors/7")).toHaveLength(1));
  expect(api.calls("POST", "/errors/7")[0].body).toEqual({ status: "resolved" });
});

it("filters by status and source and resolves everything shown", async () => {
  const api = mockFetch([
    { method: "GET", path: "/errors", reply: page },
    { method: "POST", path: "/errors/bulk", reply: { changed: 1 } },
  ]);
  renderPage(<ErrorsPage />);
  await screen.findByTestId("errors-list");
  fireEvent.change(screen.getByRole("combobox", { name: "Source" }), { target: { value: "panic" } });
  await waitFor(() => expect(api.calls("GET", "/errors").at(-1)?.search).toBe("?status=open&source=panic&limit=100"));
  fireEvent.click(screen.getByRole("button", { name: "New" }));
  await waitFor(() => expect(api.calls("GET", "/errors").at(-1)?.search).toBe("?status=new&source=panic&limit=100"));
  fireEvent.click(await screen.findByTestId("errors-resolve-all"));
  await waitFor(() => expect(api.calls("POST", "/errors/bulk")).toHaveLength(1));
  expect(api.calls("POST", "/errors/bulk")[0].body).toEqual({ where: { status: "new", source: "panic" }, status: "resolved" });
});

it("says so when there is nothing", async () => {
  mockFetch([{ method: "GET", path: "/errors", reply: { items: [], total: 0, counts: { new: 0, seen: 0, resolved: 0, ignored: 0 }, drops: 0 } }]);
  renderPage(<ErrorsPage />);
  expect(await screen.findByText("No errors here.")).toBeTruthy();
  expect(screen.queryByTestId("errors-drops")).toBeNull();
});

it("gives the message the full row on phones, with the actions on their own line below it", async () => {
  mockFetch([{ method: "GET", path: "/errors", reply: page }]);
  renderPage(<ErrorsPage />);
  const open = await screen.findByTestId("error-open-7");
  // Below sm the message button takes the whole first line of the wrapping row; from sm it shares it with the actions.
  expect(open.className).toMatch(/(^| )basis-full( |$)/);
  expect(open.className).toContain("sm:basis-0");
  const actions = screen.getByTestId("error-actions-7");
  expect(open.nextElementSibling).toBe(actions);
  expect(actions.contains(screen.getByTestId("error-resolve-7"))).toBe(true);
});
