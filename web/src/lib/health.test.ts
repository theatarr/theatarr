import { describe, expect, it } from "vitest";
import { accountNotice, backupCheck, danglingNext, healthAttention, healthBadgeTone, healthEffect, healthLink, healthStatTone, premiumEnd, type HealthCheck } from "./health";

const c = (source: string, type: HealthCheck["type"]): HealthCheck => ({ source, name: source, type, message: source });

describe("health", () => {
  it("lists warnings and errors, worst first, without the ones Needs attention names already", () => {
    const got = healthAttention([c("MountCheck", "ok"), c("BackupCheck", "warning"), c("VpnEgressCheck", "error"),
      c("FailedJobsCheck", "warning"), c("DiskSpaceCheck", "error"), c("MetadataRefreshCheck", "notice")]);
    expect(got.map((x) => x.source)).toEqual(["DiskSpaceCheck", "BackupCheck"]);
    expect(healthAttention(undefined)).toEqual([]);
  });
  it("tones and links", () => {
    expect(healthBadgeTone("error")).toBe("negative");
    expect(healthBadgeTone("notice")).toBe("info");
    expect(healthBadgeTone("ok")).toBe("positive");
    expect(healthStatTone("warning")).toBe("warn");
    expect(healthStatTone(undefined)).toBeUndefined();
    expect(healthLink("JobBacklogCheck")).toBe("/admin/system/tasks");
    expect(healthLink("VpnEgressCheck")).toBe("/admin/settings/indexers/vpn");
    expect(healthLink("ProviderSyncCheck")).toBe("/admin/settings/downloads");
    // The debrid account check is fixed where the key is, not on Status (the page that lists it).
    expect(healthLink("ProviderAccountCheck")).toBe("/admin/settings/downloads");
    expect(healthEffect("ProviderAccountCheck").next).toContain("Settings › Download clients");
    expect(healthLink("BackupCheck")).toBe("/admin/system/backup#backup-check");
    expect(healthLink("DiskSpaceCheck")).toBe("/admin/system/backup");
    expect(healthLink("SetupCheck")).toBe("/admin/setup");
    // A startup fallback is an error on Overview's Needs attention, linking to the settings reload.
    expect(healthLink("SettingsFallbackCheck")).toBe("/admin/settings");
    expect(healthEffect("SettingsFallbackCheck").effect).toMatch(/not in force/);
    expect(healthAttention([{ source: "SettingsFallbackCheck", type: "error" } as HealthCheck]).map((c) => c.source)).toEqual(["SettingsFallbackCheck"]);
    expect(healthLink("SomethingNew")).toBe("/admin");
    expect(healthEffect("DanglingLinksCheck").next).toMatch(/^Open the list below/);
    expect(["dead", "active", "unlinked", undefined].map(danglingNext)).toEqual(["Link marked missing", "Waiting for the next repair sweep", "A link Theatarr let go of", "Not a link Theatarr made"]);
  });
  it("names an effect and a next step for each known check", () => {
    for (const s of ["MountCheck", "JobBacklogCheck", "IndexerStatusCheck", "ProviderSyncCheck", "VpnEgressCheck", "BackupCheck", "DiskSpaceCheck", "MetadataRefreshCheck", "DanglingLinksCheck", "SetupCheck"]) {
      expect(healthEffect(s).effect).not.toBe("");
    }
    expect(healthEffect("SomethingNew")).toEqual({ effect: "", next: "Open the check." });
  });
  it("says what the newest backup set holds and how to fix a failing check", () => {
    const base = { dir: "/data/backups", newest: "theatarr-20261026T030000Z.db", age_s: 3600, count: 7 };
    const incomplete = backupCheck({ ...c("BackupCheck", "warning"), detail: { ...base, policy: true, manifest: false } });
    expect(incomplete.parts).toEqual([
      { label: "Database copy", present: true },
      { label: "Policy archive", present: true },
      { label: "Manifest", present: false },
    ]);
    expect(incomplete.fix).toMatch(/incomplete: run theatarr backup now/);
    const old = backupCheck({ ...c("BackupCheck", "error"), detail: { ...base, age_s: 80 * 3600, policy: true, manifest: true } });
    expect(old.fix).toMatch(/db\.nightly/);
    expect(old.fix).toContain("check it on Tasks");
    expect(backupCheck({ ...c("BackupCheck", "warning"), detail: undefined }).fix).toMatch(/Backups are off/);
    expect(backupCheck({ ...c("BackupCheck", "error"), detail: { dir: "/b" } }).fix).toMatch(/No set/);
    const ok = backupCheck({ ...c("BackupCheck", "ok"), detail: { ...base, policy: true, manifest: true } });
    expect(ok.fix).toBeUndefined();
    expect(ok.parts.every((p) => p.present)).toBe(true);
    expect(backupCheck(undefined).parts).toEqual([]);
  });
});

it("links indexer and provider checks to their settings", () => {
 expect(healthLink("IndexerStatusCheck")).toBe("/admin/settings/indexers");
 expect(healthLink("ProviderSyncCheck")).toBe("/admin/settings/downloads");
});

it("names Tasks in queue guidance", () => {
 expect(healthEffect("JobBacklogCheck").next).toBe("Look at the queue on Tasks.");
});

describe("premiumEnd", () => {
  const now = new Date("2026-11-14T12:00:00Z");
  const checks = [{ source: "ProviderAccountCheck", name: "Debrid accounts", type: "warning", message: "",
    detail: { tb: { premium: true, expires: "2026-11-17T12:00:00Z" }, rd: { premium: true, expires: "2027-03-01T00:00:00Z" },
      ad: { premium: false, expires: "2026-11-10T00:00:00Z" }, x: { error: "timeout" } } }] as HealthCheck[];
  it("says a premium end within a week as a warning and an ended one as an error", () => {
    expect(premiumEnd(checks, "tb", now)).toEqual({ text: "Premium ends Nov 17 (3 days)", tone: "warn" });
    expect(premiumEnd(checks, "ad", now)).toEqual({ text: "Premium ended Nov 10", tone: "bad" });
    expect(premiumEnd(checks, "rd", now)).toBeNull();
    expect(premiumEnd(checks, "x", now)).toBeNull();
    expect(premiumEnd(undefined, "tb", now)).toBeNull();
  });
});

describe("accountNotice", () => {
  const now = new Date("2026-11-15T17:00:00Z");
  const check = (detail: object) => ({ source: "ProviderAccountCheck", name: "Debrid accounts", type: "warning", message: "", detail }) as HealthCheck;
  it("names the provider and says to renew a premium that ends or ended", () => {
    expect(accountNotice(check({ tb: { premium: true, expires: "2026-11-17T12:00:00Z" }, rd: { premium: true, expires: "2027-03-01T00:00:00Z" } }), (k) => k, now))
      .toEqual({ effect: "TorBox premium ends Nov 17 (in 43 h).", next: "Renew at TorBox to keep premium access." });
    expect(accountNotice(check({ "ad-main": { premium: false, expires: "2026-11-10T00:00:00Z" } }), () => "ad", now))
      .toEqual({ effect: "AllDebrid premium ended Nov 10.", next: "Renew at AllDebrid to keep premium access." });
  });
  it("keeps the API-key advice for a refused key or an unreadable account", () => {
    const n = accountNotice(check({ rd: { error: "provider: authentication refused", refused: true }, tb: { premium: true, expires: "2026-11-17T12:00:00Z" } }), (k) => k, now);
    expect(n?.effect).toBe("Real-Debrid refused the API key. TorBox premium ends Nov 17 (in 43 h).");
    expect(n?.next).toBe("Renew at TorBox to keep premium access.\nCheck the account and its API key under Settings › Download clients.");
    expect(accountNotice(check({ rd: { premium: true, expires: "2027-03-01T00:00:00Z" } }), (k) => k, now)).toBeNull();
  });
  it("gives the health check's own advice for an account it could not read, so both lists agree", () => {
    const advice = "Theatarr could not reach AllDebrid: check the network and the VPN.";
    const n = accountNotice(check({ ad: { error: "dial tcp: i/o timeout", advice }, rd: { error: "provider: authentication refused", refused: true } }), (k) => k, now);
    expect(n?.effect).toBe("AllDebrid's account could not be read. Real-Debrid refused the API key.");
    expect(n?.next).toBe(`${advice}\nCheck the account and its API key under Settings › Download clients.`);
    expect(accountNotice(check({ ad: { error: "dial tcp: i/o timeout", advice } }), (k) => k, now)?.next).toBe(advice);
  });
  it("gives each account's advice as a sentence of its own line", () => {
    // The health check's advice comes without a full stop (its Checks row joins the lines with semicolons).
    const n = accountNotice(check({
      rd: { error: "dial tcp: i/o timeout", advice: "Real-Debrid cannot be reached: check this server's network and VPN" },
      tb: { error: "dial tcp: i/o timeout", advice: "TorBox cannot be reached: check this server's network and VPN" },
    }), (k) => k, now);
    expect(n?.next).toBe("Real-Debrid cannot be reached: check this server's network and VPN.\nTorBox cannot be reached: check this server's network and VPN.");
  });
});
