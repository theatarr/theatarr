// @vitest-environment jsdom
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { Logs } from "./logs";

it("shows a skipped-lines marker and links persistent log files", async () => {
  mockFetch([
    { method: "GET", path: "/logs", reply: { items: [], components: [], boot_id: "boot", after: 25, more: false, skipped: 17 } },
    { method: "GET", path: "/logs/files", reply: { items: [{ name: "theatarr.log", size: 1024, modified: "2026-10-30T12:00:00Z" }] } },
    { method: "GET", path: "/logs/level", reply: { level: "INFO" } },
  ]);
  renderPage(<Logs />);
  expect(await screen.findByText(/^17 log lines skipped/)).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Find older lines in log files" })).toBeNull();
  expect(screen.getByRole("link", { name: "theatarr.log (1.0 KiB)" }).getAttribute("href")).toBe("/api/theatarr/v1/logs/files/theatarr.log");
});

it("shows a restart marker when the new process has no log entries", async () => {
 vi.useFakeTimers();
 try {
  let count=0;
  mockFetch([
   {method:"GET",path:"/logs",reply:()=>({items:[],components:[],boot_id:count++===0?"old":"new",after:0,seen:0,more:false,skipped:0})},
   {method:"GET",path:"/logs/files",reply:{items:[]}},
   {method:"GET",path:"/logs/level",reply:{level:"INFO"}},
  ]);
  renderPage(<Logs/>);
  await act(async()=>{await vi.advanceTimersByTimeAsync(100)});
  await act(async()=>{await vi.advanceTimersByTimeAsync(5000)});
  expect(screen.getByText(/Theatarr restarted/)).toBeTruthy();
 } finally {vi.useRealTimers();}
});

it("keeps the service log level apart from the viewing filter: a choice applies only with Apply", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/logs", reply: { items: [], components: [], boot_id: "b", after: 0, more: false, skipped: 0 } },
    { method: "GET", path: "/logs/files", reply: { items: [] } },
    { method: "GET", path: "/logs/level", reply: { level: "INFO" } },
    { method: "PUT", path: "/logs/level", reply: { level: "DEBUG" } },
  ]);
  renderPage(<Logs />);
  const card = await screen.findByTestId("log-level");
  // Its own card, above the viewing filter and the lines, so #log-level reaches it.
  expect(screen.getByText("Show lines from:").compareDocumentPosition(card) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy();
  expect(card.id).toBe("log-level");
  const select = within(card).getByRole("combobox", { name: "Service log level" }) as HTMLSelectElement;
  await waitFor(() => expect(select.value).toBe("INFO"));
  const apply = within(card).getByRole("button", { name: "Apply" }) as HTMLButtonElement;
  expect(apply.disabled).toBe(true);
  fireEvent.change(select, { target: { value: "DEBUG" } });
  expect(fetch.calls("PUT", "/logs/level")).toHaveLength(0);
  fireEvent.click(apply);
  await waitFor(() => expect(fetch.calls("PUT", "/logs/level")[0]?.body).toEqual({ level: "DEBUG" }));
});

it("filters the loaded lines by words in the browser and says how many of them match", async () => {
  const line = (seq: number, msg: string, component: string, attrs: Record<string, string> = {}) => ({ seq, time: "2026-11-17T12:00:00Z", level: "INFO", msg, component, attrs });
  const fetch = mockFetch([
    { method: "GET", path: "/logs", reply: { items: [line(1, "grab accepted", "router", { hash: "abc123" }), line(2, "sync done", "provider"), { ...line(3, "grab refused", "router", { hash: "def456" }), level: "ERROR" }], components: ["router", "provider"], boot_id: "b", after: 3, more: false, skipped: 0 } },
    { method: "GET", path: "/logs/files", reply: { items: [] } },
    { method: "GET", path: "/logs/level", reply: { level: "INFO" } },
  ]);
  renderPage(<Logs />);
  await screen.findByText("sync done");
  const reads = fetch.calls("GET", "/logs").length;
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter these lines" }), { target: { value: "Router GRAB" } });
  expect(screen.queryByText("sync done")).toBeNull();
  expect(screen.getByText("grab accepted")).toBeTruthy();
  expect(screen.getByTestId("log-filter-count").textContent).toBe("2 of 3 lines");
  // A field's value matches too.
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter these lines" }), { target: { value: "def456" } });
  expect(screen.getByTestId("log-filter-count").textContent).toBe("1 of 3 lines");
  expect(screen.getByText("grab refused")).toBeTruthy();
  // The level shown on the badge matches too.
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter these lines" }), { target: { value: "error router" } });
  expect(screen.getByTestId("log-filter-count").textContent).toBe("1 of 3 lines");
  expect(screen.getByText("grab refused")).toBeTruthy();
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter these lines" }), { target: { value: "nothing-like-this" } });
  expect(screen.getByText("No loaded line matches.")).toBeTruthy();
  // Filtering asks the server nothing.
  expect(fetch.calls("GET", "/logs").length).toBe(reads);
});
