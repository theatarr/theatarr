// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppBackups } from "./app-backups";
import type { AppBackupList } from "@/api/backups";

const name = "theatarr-20261102-030000";
let list: AppBackupList;
const fetcher = vi.fn();
beforeEach(() => {
  list = { dir: "/data/backups", keep: 14, hour: 3, enabled: true, settings_source: "db", last_restore: null,
    sets: [{ name, created_at: "2026-11-02T03:00:00Z", schema: 103, bytes: 4096, policy_state: "absent", complete: true, restorable: true, why_not: "" }] };
  fetcher.mockReset();
  fetcher.mockImplementation(async (_url: string, init?: RequestInit) => new Response(JSON.stringify(init?.method === "POST" ? { safety_set: "safety", message: "Restarting" } : list), { status: init?.method === "POST" ? 202 : 200 }));
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><AppBackups /></QueryClientProvider>);
}
describe("AppBackups", () => {
  it("renders the sets and schedule", async () => {
    mount();
    expect(await screen.findByText(name)).toBeTruthy();
    expect(screen.getByText("Nightly at 03:00, keeping 14.")).toBeTruthy();
    expect(screen.getByText("4.0 KiB")).toBeTruthy();
  });
  it("says how old a day-old backup is", async () => {
    list.sets[0].created_at = new Date(Date.now() - 30 * 3600_000).toISOString();
    mount();
    expect(await screen.findByText("1 d ago")).toBeTruthy();
    expect(screen.queryByText("1 days")).toBeNull();
  });
  it("requires the exact name and shows restarting after 202", async () => {
    mount(); fireEvent.click(await screen.findByRole("button", { name: "Restore…" }));
    const button = screen.getByRole("button", { name: "Restore" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    const input = screen.getByLabelText("Backup name confirmation");
    fireEvent.change(input, { target: { value: name + " " } });
    expect(button.disabled).toBe(true);
    fireEvent.change(input, { target: { value: name } });
    expect(button.disabled).toBe(false);
    fireEvent.click(button);
    expect(await screen.findByText("Restarting…")).toBeTruthy();
    expect(fetcher.mock.calls.some(([url, init]) => String(url).endsWith(`/system/backups/${name}/restore`) && JSON.parse(init.body).confirm === name)).toBe(true);
  });
  it.each([true, false])("renders the last restore banner (ok=%s)", async (ok) => {
    list.last_restore = { set: name, ok, error: ok ? "" : "hash mismatch", started_at: new Date().toISOString(), finished_at: new Date().toISOString(), requested_by: "admin", safety_set: "safety", kept_database: "/data/old.db", policy_out: "", message: "" };
    mount();
    if (ok) {
      expect(await screen.findByText(/Your previous state is saved as safety/)).toBeTruthy();
      expect(screen.getByText("docker exec theatarr theatarr library relink -dry-run")).toBeTruthy();
      expect(screen.getByText(/Provider sync was started/)).toBeTruthy();
    } else { expect(await screen.findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("hash mismatch")); }
  });
  it("disables incomplete sets and shows why", async () => {
    list.sets[0].complete = false; list.sets[0].restorable = false; list.sets[0].why_not = "No manifest";
    mount(); await screen.findByText("No manifest");
    await waitFor(() => expect((screen.getByRole("button", { name: "Restore…" }) as HTMLButtonElement).disabled).toBe(true));
  });
});
