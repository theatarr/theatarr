// @vitest-environment jsdom
import { act, screen } from "@testing-library/react";
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
