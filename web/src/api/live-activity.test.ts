import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { DownloadEvent } from "./activity";
import { downloadToast, libraryRefresh, onDownloadEvent, onTitleEvent, PROGRESS_LIVE_MS, PROGRESS_REFRESH_MS, stateChanged } from "./live-activity";
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
      body: "Download complete, waiting to be linked · WEB",
      tone: "ok",
    });
    expect(downloadToast({ ...base, state: "failed", words: "Download failed" })?.tone).toBe("bad");
    // A removal ended it: History has it, the queue the toast opens does not.
    expect(downloadToast({ ...base, state: "failed", previous: "downloading", words: "Download failed", removed: true })).toBeNull();
  });
  it("stays quiet on progress", () => {
    expect(downloadToast({ ...base, state: "downloading", previous: undefined, progress: 40 })).toBeNull();
    expect(downloadToast({ ...base, state: "downloading", previous: "downloading" })).toBeNull();
  });
  it("names the library only to accounts that see files", () => {
    expect(downloadToast(base, false)?.body).toBe("Download complete, waiting to be linked");
    expect(downloadToast(base, true)?.body).toBe("Download complete, waiting to be linked · WEB");
    // The library's name from auth/me, never its key.
    expect(downloadToast(base, true, (k) => (k === "movie-web" ? "4K WEB" : k))?.body).toBe("Download complete, waiting to be linked · 4K WEB");
  });
  it("toasts a new download", () => {
    expect(downloadToast({ ...base, state: "queued", previous: undefined, words: "Queued" })?.tone).toBe("info");
  });
});

describe("libraryRefresh", () => {
  it("refreshes the title and lists on a change of state, and progress at most every 20 s", () => {
    const t0 = 1_000_000;
    const ev = { ...base, title: { ...base.title, id: 77 } };
    expect(libraryRefresh(ev, true, t0)).toEqual({ title: true, lists: true });
    expect(libraryRefresh(ev, false, t0 + 1000)).toEqual({ title: false, lists: false });
    expect(libraryRefresh(ev, true, t0 + 2000)).toEqual({ title: true, lists: true });
    expect(libraryRefresh(ev, false, t0 + 2000 + PROGRESS_REFRESH_MS)).toEqual({ title: true, lists: true });
  });
});

describe("stateChanged", () => {
  it("is a change on a download's first event and when its state moves, not on progress", () => {
    // What the server sends: previous only on a change.
    expect(stateChanged({ id: 50, state: "downloading" })).toBe(true);
    expect(stateChanged({ id: 50, state: "downloading" })).toBe(false);
    expect(stateChanged({ id: 51, state: "downloading" })).toBe(true);
    expect(stateChanged({ id: 50, state: "ready" })).toBe(true);
  });
});

describe("onDownloadEvent", () => {
  const keys = {
    calendar: [...qk.calendar.all, "2026-11-01", "2026-11-30"],
    now: [...qk.activity.all, "now", {}],
    continue: [...qk.activity.all, "continue", 12],
    history: [...qk.activity.all, "history", {}, {}],
  };
  function fresh() {
    const qc = new QueryClient();
    for (const key of Object.values(keys)) qc.setQueryData(key, { items: [] });
    return qc;
  }
  const invalidated = (qc: QueryClient) =>
    Object.fromEntries(Object.entries(keys).map(([name, key]) => [name, qc.getQueryState(key)?.isInvalidated]));

  it("refreshes the activity lists and the calendar, whose entries show downloading and downloaded, on a change of state", () => {
    const qc = fresh();
    onDownloadEvent(qc, JSON.stringify({ ...base, id: 10, state: "downloading", previous: "queued", progress: 0 }), 1_000_000);
    expect(invalidated(qc)).toEqual({ calendar: true, now: true, continue: true, history: true });
  });
  it("refreshes only the queue and the Continue row on progress, at most every 5 s per download", () => {
    // Progress events as the server sends them: no previous.
    const progress = (id: number, at: number) => {
      const qc = fresh();
      onDownloadEvent(qc, JSON.stringify({ ...base, id, state: "downloading", previous: undefined, progress: 40 }), at);
      return invalidated(qc);
    };
    const t0 = 2_000_000;
    // The first event of a download is a change.
    expect(progress(11, t0)).toEqual({ calendar: true, now: true, continue: true, history: true });
    expect(progress(11, t0 + 1000)).toEqual({ calendar: false, now: true, continue: true, history: false });
    expect(progress(11, t0 + 2000)).toEqual({ calendar: false, now: false, continue: false, history: false });
    expect(progress(12, t0 + 2000).history).toBe(true);
    expect(progress(11, t0 + 1000 + PROGRESS_LIVE_MS).now).toBe(true);
  });
});

describe("onTitleEvent", () => {
  it("refreshes the torrent lists on a versions event (a removal's clean-up ended), not on art", () => {
    const qc = new QueryClient();
    qc.setQueryData(qk.torrents.report, {});
    onTitleEvent(qc, JSON.stringify({ id: 2, name: "Beta", reason: "art" }));
    expect(qc.getQueryState(qk.torrents.report)?.isInvalidated).toBe(false);
    onTitleEvent(qc, JSON.stringify({ id: 2, name: "Beta", reason: "versions" }));
    expect(qc.getQueryState(qk.torrents.report)?.isInvalidated).toBe(true);
  });
});
