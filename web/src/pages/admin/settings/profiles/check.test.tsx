// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { expectNamedControls, json, mockFetch, renderPage } from "@/test/page";
import { ReleaseCheck } from "./check";

const dune = {
  id: 7, kind: "movie", name: "Dune: Part Two", year: 2024,
  versions: [{ id: 70, key: "movie-remux", label: "4K Remux", has_file: true }, { id: 71, key: "movie-web", label: "4K WEB", has_file: false }],
};
const searchReply = { query: "dune", library: [dune], library_total: 1, tmdb: [], tmdb_state: "skipped" };
const rejected = {
  release: "Dune.Part.Two.2024.1080p.BluRay.x264-GRP", version: "movie-remux", profile: "remux-2160p", accepted: false, parsed: true,
  movie_titles: ["Dune Part Two"], year: 2024, release_group: "GRP", match: "Title",
  rejections: [
    { reason: "QualityNotWanted", message: "Bluray-1080p is not wanted in profile", spec: "QualityAllowedByProfileSpecification" },
    { reason: "IndexerNotInVersion", message: "Indexer x is not one of version movie-remux's indexers", ours: true },
  ],
  quality: "Bluray-1080p", revision: { version: 1, real: 0, isRepack: false }, profile_rank: 0, profile_ranks: 2,
  formats: [{ name: "LQ", score: -10000 }], score: -10000, upgrade: false,
};
const accepted = {
  release: "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", version: "movie-remux", profile: "remux-2160p",
  accepted: true, parsed: true, match: "Title", movie_titles: ["Dune Part Two"], year: 2024, release_group: "FraMeSToR", languages: ["English"],
  quality: "Remux-2160p", revision: { version: 1, real: 0, isRepack: false }, profile_rank: 1, profile_ranks: 2,
  formats: [{ name: "Remux Tier 01", score: 1950 }, { name: "TrueHD ATMOS", score: 500 }], score: 2450,
  existing: { quality: "WEBDL-2160p", profile_rank: 2, formats: [{ name: "WEB Tier 01", score: 1700 }], score: 1700 },
  upgrade: true, upgrade_reason: "better quality: Remux-2160p over WEBDL-2160p",
};

async function fill(user: ReturnType<typeof userEvent.setup>, release: string, size = "", hash = "") {
  await user.type(screen.getByRole("textbox", { name: "Find a title" }), "dune");
  const title = screen.getByRole("combobox", { name: "Title" });
  await within(title).findByRole("option", { name: "Dune: Part Two (2024)" });
  await user.selectOptions(title, "7");
  await user.selectOptions(screen.getByRole("combobox", { name: "Library" }), "movie-remux");
  if (size) await user.type(screen.getByRole("textbox", { name: "Size in GiB (optional)" }), size);
  await user.type(screen.getByRole("textbox", { name: "Release name" }), release);
  if (hash) await user.type(screen.getByRole("textbox", { name: "Info hash (optional)" }), hash);
  await user.click(screen.getByRole("button", { name: "Check" }));
}

describe("Release check", () => {
  it("renders one form with named controls and checks nothing until it is filled", () => {
    mockFetch([]);
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    expect(screen.getByRole("form", { name: "Check a release" })).toBeTruthy();
    expectNamedControls();
    expect((screen.getByRole("button", { name: "Check" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("combobox", { name: "Library" }) as HTMLSelectElement).disabled).toBe(true);
  });

  it("posts the release for the chosen title and library, and shows rejections and the inputs it lacked", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: rejected }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, rejected.release);
    const result = await screen.findByTestId("release-check-result");
    expect(fetch.calls("POST", "/decide")[0].body).toEqual({ title_id: 7, version: "movie-remux", release: rejected.release });
    expect(within(result).getByText("Rejected")).toBeTruthy();
    const rejections = within(result).getByRole("list", { name: "Rejections" });
    expect(within(rejections).getByText("QualityNotWanted")).toBeTruthy();
    expect(within(rejections).getByText("Bluray-1080p is not wanted in profile")).toBeTruthy();
    expect(within(rejections).getByText("ours")).toBeTruthy();
    expect(within(result).getByTestId("quality").textContent).toBe("Bluray-1080p v1 · not allowed by the profile");
    // Size, indexer, seeders and languages were not given: unknown, not guessed.
    for (const k of ["Size", "Indexer", "Seeders", "Languages"]) expect(within(result).getByText(k).nextElementSibling?.textContent).toBe("unknown");
    expect(within(result).getByText(/no file for this yet/)).toBeTruthy();
    expect(within(result).getByTestId("total-score").textContent).toContain("-10000");
  });

  it("reads a size with a unit as the settings forms do (GB is 10^9 bytes)", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(accepted) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release, "36 GB");
    await screen.findByTestId("release-check-result");
    expect(fetch.calls("POST", "/decide")[0].body).toMatchObject({ size: 36e9 });
  });

  it("refuses size text that is not a size, beside the field, and checks nothing", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(accepted) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release, "oops");
    expect(screen.getByRole("alert").textContent).toContain("Not a size");
    expect(screen.getByRole("textbox", { name: "Size in GiB (optional)" }).getAttribute("aria-invalid")).toBe("true");
    expect((screen.getByRole("button", { name: "Check" }) as HTMLButtonElement).disabled).toBe(true);
    expect(fetch.calls("POST", "/decide")).toHaveLength(0);
  });

  it("sends a given size and shows the quality rank, scores and the upgrade over the current file", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(accepted) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release, "60");
    const result = await screen.findByTestId("release-check-result");
    expect(fetch.calls("POST", "/decide")[0].body).toEqual({ title_id: 7, version: "movie-remux", release: accepted.release, size: 60 * 1024 ** 3 });
    expect(within(result).getByText("Would be accepted")).toBeTruthy();
    expect(within(result).getByTestId("quality").textContent).toBe("Remux-2160p v1 · rank 1 of 2 allowed");
    expect(within(result).getByText("Size").nextElementSibling?.textContent).not.toBe("unknown");
    expect(within(result).getByText("Languages").nextElementSibling?.textContent).toBe("English");
    expect(within(result).getByText("Indexer").nextElementSibling?.textContent).toBe("unknown");
    const existing = within(result).getByTestId("existing");
    expect(within(existing).getByText("WEBDL-2160p · rank 2 of 2 allowed")).toBeTruthy();
    expect(within(existing).getByText(/better quality: Remux-2160p over WEBDL-2160p/)).toBeTruthy();
    expect(within(result).getAllByTestId("total-score")[0].textContent).toContain("2450");
  });

  it("sends a given info hash; a SeaDex library without one says the verdict may differ, and orders by standing first", async () => {
    const seadex = { ...accepted, prefer_seadex: true };
    const hash = "0123456789abcdef0123456789abcdef01234567";
    const fetch = mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(seadex) }]);
    const user = userEvent.setup();
    const page = renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release, "", hash);
    let result = await screen.findByTestId("release-check-result");
    expect(fetch.calls("POST", "/decide")[0].body).toEqual({ title_id: 7, version: "movie-remux", release: accepted.release, info_hash: hash });
    expect(within(result).getByText("Info hash").nextElementSibling?.textContent).toBe(hash);
    expect(within(result).queryByTestId("seadex-unknown")).toBeNull();
    expect(within(result).getByTestId("comparison-note").textContent).toMatch(/SeaDex standing comes first.*Within the same standing.*the quality's rank/);
    page.unmount();

    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(seadex) }]);
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release);
    result = await screen.findByTestId("release-check-result");
    expect(within(result).getByText("Info hash").nextElementSibling?.textContent).toBe("unknown");
    expect(within(result).getByTestId("seadex-unknown").textContent).toContain("the verdict may differ");
  });

  it("without SeaDex preference, says quality comes first and gives no SeaDex warning", async () => {
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(accepted) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release);
    const result = await screen.findByTestId("release-check-result");
    expect(within(result).getByTestId("comparison-note").textContent).toContain("the quality's rank first");
    expect(within(result).getByTestId("comparison-note").textContent).not.toContain("SeaDex");
    expect(within(result).queryByTestId("seadex-unknown")).toBeNull();
  });

  it("labels a language the name does not state as assumed from the title's original language", async () => {
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json({ ...accepted, languages_assumed: true }) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release);
    const result = await screen.findByTestId("release-check-result");
    expect(within(result).getByText("Languages").nextElementSibling?.textContent).toBe("English (assumed: the title's original language; the name states none)");
  });

  it("shows an assumed language the title does not know as unknown", async () => {
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json({ ...accepted, languages: ["Unknown"], languages_assumed: true }) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release);
    const result = await screen.findByTestId("release-check-result");
    expect(within(result).getByText("Languages").nextElementSibling?.textContent).toBe("unknown");
  });

  it("says the comparison is not available for a name it cannot parse or match, not that there is no file", async () => {
    const unparsed = { release: "garbage", version: "movie-remux", profile: "remux-2160p", accepted: false, parsed: false, quality: "Unknown",
      rejections: [{ reason: "UnableToParse", message: "Unable to parse release" }], formats: [], score: 0, upgrade: false };
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(unparsed) }]);
    const user = userEvent.setup();
    let page = renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, "garbage");
    let result = await screen.findByTestId("release-check-result");
    expect(within(result).getByTestId("no-comparison").textContent).toContain("Comparison not available: the name could not be parsed");
    expect(within(result).queryByText(/no file for this yet/)).toBeNull();
    page.unmount();

    const unmatched = { ...unparsed, parsed: true, rejections: [{ reason: "UnknownMovie", message: "Unknown Movie" }] };
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(unmatched) }]);
    page = renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, "Other.Movie.2020.1080p.WEB-DL-GRP");
    result = await screen.findByTestId("release-check-result");
    expect(within(result).getByTestId("no-comparison").textContent).toContain("the release did not match this title");
    page.unmount();

    // A matched series release whose episodes have no file: there is no file, the comparison is not missing.
    const episode = { ...unparsed, release: "Dune.S01E01.1080p.WEB-DL-GRP", parsed: true, match: "Title", series_title: "Dune", episodes: ["S01E01"], quality: "WEBDL-1080p", rejections: [] };
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json(episode) }]);
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, episode.release);
    result = await screen.findByTestId("release-check-result");
    expect(within(result).queryByTestId("no-comparison")).toBeNull();
    expect(within(result).getByText(/no file for this yet/)).toBeTruthy();
  });

  it("shows the server's error", async () => {
    mockFetch([{ method: "GET", path: "/search", reply: searchReply }, { method: "POST", path: "/decide", reply: json({ error: "the decision engine is not running" }, 503) }]);
    const user = userEvent.setup();
    renderPage(<ReleaseCheck />, "/admin/settings/profiles/check");
    await fill(user, accepted.release);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("the decision engine is not running"));
  });
});
