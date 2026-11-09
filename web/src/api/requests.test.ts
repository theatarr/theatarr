import { describe, expect, it } from "vitest";
import { permsOf } from "./library";
import { requestToast } from "./requests";
import { navFor } from "@/components/shell/nav";
import { requestVersions } from "@/lib/request-state";

describe("permissions", () => {
  it("reads the names", () => {
    const u = permsOf(["browse", "request"]);
    expect(u).toMatchObject({ request: true, add: false, admin: false, seeFiles: false, manageRequests: false });
    expect(permsOf(undefined).admin).toBe(false);
  });
  it("keeps the admin group from users", () => {
    const groups = navFor({ admin: false, users: false });
    expect(groups.map((g) => g.id)).toEqual(["main"]);
    const mgr = navFor({ admin: false, users: true });
    expect(mgr.find((g) => g.id === "admin")?.items.map((i) => i.to)).toEqual(["/admin/users"]);
  });
});

describe("requests", () => {
  const opts = [
    { key: "movie-remux", label: "4K Remux", fits: true, on: false },
    { key: "movie-web", label: "4K WEB", fits: true, on: false },
    { key: "anime-movie", label: "Anime", fits: false, on: false },
  ];
  it("asks for every fitting version by default", () => {
    expect(requestVersions(opts, undefined).map((v) => v.key)).toEqual(["movie-remux", "movie-web"]);
  });
  it("keeps to the account's set, falling back when it covers no fitting version", () => {
    expect(requestVersions(opts, ["movie-web", "tv-2160p"]).map((v) => v.key)).toEqual(["movie-web"]);
    expect(requestVersions(opts, ["tv-2160p"]).map((v) => v.key)).toEqual(["movie-remux", "movie-web"]);
  });
  it("leaves out what the library has", () => {
    expect(requestVersions([{ ...opts[0], on: true }, opts[1]], undefined).map((v) => v.key)).toEqual(["movie-web"]);
  });
  it("words a status change", () => {
    expect(requestToast({ id: 1, status: "available", name: "Dune", mine: true, change: "status" })?.body).toBe("Now available.");
    expect(requestToast({ id: 1, status: "pending", name: "Dune", mine: true, change: "merged" })).toBeNull();
  });
});
