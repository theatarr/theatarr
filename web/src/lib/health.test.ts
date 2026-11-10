import { describe, expect, it } from "vitest";
import { backupCheck, danglingNext, healthAttention, healthBadgeTone, healthEffect, healthLink, healthStatTone, type HealthCheck } from "./health";

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
