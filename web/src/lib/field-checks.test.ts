import { describe, expect, it } from "vitest";
import { clockProblem, keyProblem, sectionProblem, sectionProblems, urlProblem } from "./field-checks";

// The inline checks repeat the server's rules (internal/policy) and are never stricter.
describe("inline settings checks", () => {
  it("reads a time as Go's 15:04 does", () => {
    for (const ok of ["09:00", "9:05", "23:59", "00:00"]) expect(clockProblem(ok)).toBeUndefined();
    for (const bad of ["25:99", "24:00", "9:5", "nine"]) expect(clockProblem(bad)).toBeDefined();
  });

  it("takes an http(s) address with a host, refusing a user:password and a key in the query", () => {
    for (const ok of ["http://jackett:9117/api", "HTTPS://example.com", "http://plex:32400/?X-Plex-Product=x"]) expect(urlProblem(ok)).toBeUndefined();
    expect(urlProblem("not a url")).toBe("Enter a full address that starts with http:// or https://.");
    expect(urlProblem("ftp://host")).toBeDefined();
    expect(urlProblem("http:///path")).toBeDefined();
    expect(urlProblem("http://user:pw@host")).toBe("Leave the user name and password out of the address.");
    expect(urlProblem("http://prowlarr:9696/1/api?apikey=abc")).toContain("apikey=");
    expect(urlProblem("http://h/?X-Plex-Token=a")).toBeDefined();
    expect(urlProblem("http://h/?t=1&page=2")).toBeUndefined();
  });

  it("matches each section field's own rule: the zurg log is a path or an address, a notification may carry a token", () => {
    expect(sectionProblem("library.mount.zurg_log", "/logs/zurg.log")).toBeUndefined();
    expect(sectionProblem("library.mount.zurg_log", "http://zurg:9999/logs")).toBeUndefined();
    expect(sectionProblem("library.mount.zurg_log", "logs/zurg.log")).toBeDefined();
    expect(sectionProblem("notify.targets.ntfy.url", "https://ntfy.sh/topic?auth=abc")).toBeUndefined();
    expect(sectionProblem("notify.targets.ntfy.url", "https://u:p@ntfy.sh/topic")).toBeDefined();
    expect(sectionProblem("clients.decypharr.url", "decypharr:8282")).toBeDefined();
    expect(sectionProblem("library.plex.url", "")).toBeUndefined();
    expect(sectionProblem("library.mount.path", "not a url")).toBeUndefined();
    // Only edited values are checked: a stored oddity is the server's to report.
    expect(sectionProblems({ plex: { url: "bad" } }, { plex: { url: "bad" } }, "library")).toEqual({});
    expect(Object.keys(sectionProblems({ plex: { url: "bad2" } }, { plex: { url: "bad" } }, "library"))).toEqual(["library.plex.url"]);
  });

  it("reads a key as checkKey does", () => {
    expect(keyProblem("my-jackett_2")).toBeUndefined();
    expect(keyProblem("My Jackett!!")).toBeDefined();
    expect(keyProblem("-x")).toBeDefined();
    expect(keyProblem("")).toBe("Enter a name.");
  });
});
