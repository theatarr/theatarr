import { describe, expect, it } from "vitest";
import type { Live } from "@/api/events";
import type { Gateway } from "@/api/hooks";
import { liveSummary } from "./live-status";

const gw = (vpn_ok?: boolean) => ({ egress: vpn_ok === undefined ? {} : { vpn_ok } }) as unknown as Gateway;

describe("liveSummary", () => {
  it("shows optional VPN modes as warnings and still counts failed jobs", () => {
    for (const mode of ["off", "monitor-only"] as const) {
      const live: Live = { connected: true, gatewayAt: 120, gateway: { egress: { mode, vpn_ok: false } }, jobsAt: 120, jobCounts: { attention: 2 } };
      expect(liveSummary(live)).toMatchObject({ tone: "warn", label: `${mode === "off" ? "VPN guard off" : "VPN monitor-only"} · 2 failed jobs` });
    }
  });
  it("is connecting before anything arrived", () => {
    expect(liveSummary({ connected: false }).label).toBe("Connecting");
  });
  it("says VPN up only on a current positive answer", () => {
    const live: Live = { connected: true, connectedAt: 100, gateway: gw(true), gatewayAt: 120, jobsAt: 120, jobCounts: {} };
    expect(liveSummary(live)).toMatchObject({ tone: "ok", label: "VPN up" });
    expect(liveSummary({ ...live, gateway: gw(undefined) })).toMatchObject({ tone: "warn", label: "VPN unknown" });
    expect(liveSummary({ ...live, gateway: gw(false) })).toMatchObject({ tone: "bad", label: "VPN down" });
  });
  it("shows a dropped stream as disconnected, not as its last snapshot", () => {
    const live: Live = { connected: false, lostAt: 200, connectedAt: 100, gateway: gw(true), gatewayAt: 120 };
    expect(liveSummary(live)).toMatchObject({ tone: "stale", label: "Disconnected" });
    expect(liveSummary({ connected: false, lostAt: 5 }).label).toBe("Disconnected");
  });
  it("reads Reconnecting for the first 15 seconds of a drop", () => {
    const now = 1_000_000;
    const live: Live = { connected: false, connectedAt: 100, gateway: gw(true), gatewayAt: 120 };
    expect(liveSummary({ ...live, lostAt: now - 5_000 }, now)).toMatchObject({ tone: "idle", label: "Reconnecting" });
    expect(liveSummary({ ...live, lostAt: now - 20_000 }, now)).toMatchObject({ tone: "stale", label: "Disconnected" });
  });
  it("treats a snapshot from an earlier connection as stale", () => {
    const live: Live = { connected: true, connectedAt: 300, gateway: gw(true), gatewayAt: 120 };
    expect(liveSummary(live)).toMatchObject({ tone: "idle", label: "Checking" });
  });
  it("counts failed jobs from the current connection", () => {
    const live: Live = { connected: true, connectedAt: 100, gateway: gw(true), gatewayAt: 120, jobsAt: 130, jobCounts: { attention: 2, failed: 3 } };
    expect(liveSummary(live)).toMatchObject({ tone: "warn", label: "VPN up · 2 failed jobs" });
  });
});
