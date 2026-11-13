import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { DownloadEvent } from "./activity";
import { downloadToast, libraryRefresh, onDownloadEvent, PROGRESS_REFRESH_MS } from "./live-activity";
import { qk } from "./query-keys";

const base: DownloadEvent = {
  id: 1,
  title: { id: 1, name: "Alpha", year: 2020, kind: "movie", poster: "/img/t/1/poster/w342" },
  version: "movie-web",
  state: "ready",
  previous: "downloading",
  step: "ready",
  words: "Download complete, waiting to be linked",
  progress: 100,
};

describe("downloadToast", () => {
  it("toasts a change of state", () => {
    expect(downloadToast(base)).toEqual({
      title: "Alpha (2020)",
      body: "Download complete, waiting to be linked · movie-web",
      tone: "ok",
    });
    expect(downloadToast({ ...base, state: "failed", words: "Download failed" })?.tone).toBe("bad");
  });
  it("stays quiet on progress", () => {
    expect(downloadToast({ ...base, state: "downloading", previous: undefined, progress: 40 })).toBeNull();
    expect(downloadToast({ ...base, state: "downloading", previous: "downloading" })).toBeNull();
  });
  it("names the library only to accounts that see files", () => {
    expect(downloadToast(base, false)?.body).toBe("Download complete, waiting to be linked");
    expect(downloadToast(base, true)?.body).toBe("Download complete, waiting to be linked · movie-web");
  });
  it("toasts a new download", () => {
    expect(downloadToast({ ...base, state: "queued", previous: undefined, words: "Queued" })?.tone).toBe("info");
  });
});

describe("libraryRefresh", () => {
  it("refreshes the title and lists on a change of state, and progress at most every 20 s", () => {
    const t0 = 1_000_000;
    const ev = { ...base, title: { ...base.title, id: 77 } };
    expect(libraryRefresh(ev, t0)).toEqual({ title: true, lists: true });
    const progress = { ...ev, state: "downloading" as const, previous: "downloading" as const };
    expect(libraryRefresh(progress, t0 + 1000)).toEqual({ title: false, lists: false });
    expect(libraryRefresh(ev, t0 + 2000)).toEqual({ title: true, lists: true });
    expect(libraryRefresh(progress, t0 + 2000 + PROGRESS_REFRESH_MS)).toEqual({ title: true, lists: true });
  });
});

describe("onDownloadEvent", () => {
  it("refreshes the calendar, whose entries show downloading and downloaded", () => {
    const qc = new QueryClient();
    const key = [...qk.calendar.all, "2026-11-01", "2026-11-30"];
    qc.setQueryData(key, { items: [] });
    onDownloadEvent(qc, JSON.stringify({ ...base, state: "downloading", previous: "downloading", progress: 40 }));
    expect(qc.getQueryState(key)?.isInvalidated).toBe(true);
  });
});
