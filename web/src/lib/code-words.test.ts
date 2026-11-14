import { describe, expect, it } from "vitest";
import { animeFormatWords, animeSourceWords, mappingCounts, mappingWords, orderWords, roleWords } from "./anime-words";
import { readableCode, wordsFor } from "./code-words";
import { errorComponentWords, errorSourceWords, errorStatusWords } from "./error-words";
import { evidenceSourceWords } from "./evidence-words";
import { rejectionWords } from "./rejection-words";
import { attemptCodeWords, attemptOutcomeWords, backoffWords, cacheSourceWords, cacheStateWords } from "./router-words";
import { releaseEventWords, releaseTypeWords, titleStatusWords } from "./tmdb-words";
import { torrentJournalBy, torrentJournalWords, torrentStateWords } from "./torrent-words";

describe("readableCode (the fallback for a code no map knows)", () => {
  it("turns a code into sentence-case words, never blank", () => {
    expect(readableCode("uncached_deleted")).toBe("Uncached deleted");
    expect(readableCode("DiskHigherPreference")).toBe("Disk higher preference");
    expect(readableCode("inCinemas")).toBe("In cinemas");
    expect(readableCode("TVDB_SPECIAL")).toBe("TVDB special");
    expect(readableCode("router.copy")).toBe("Router copy");
    expect(readableCode("")).toBe("");
    expect(readableCode(undefined)).toBe("");
    expect(wordsFor({ a: "Words" }, "a")).toBe("Words");
    expect(wordsFor({ a: "Words" }, "some_new_code")).toBe("Some new code");
  });
});

describe("rejection reasons", () => {
  it("in words, with a readable fallback", () => {
    expect(rejectionWords("DiskHigherPreference")).toBe("Not better than the file held");
    expect(rejectionWords("CustomFormatMinimumScore")).toBe("Format score too low");
    expect(rejectionWords("UnknownMovie")).toBe("Movie not recognised");
    expect(rejectionWords("UnknownSeries")).toBe("Series not recognised");
    expect(rejectionWords("BrandNewReason")).toBe("Brand new reason");
  });
});

describe("Why? evidence sources", () => {
  it("names the origin and keeps the indexer or instance", () => {
    expect(evidenceSourceWords("rss:dmm")).toBe("RSS (dmm)");
    expect(evidenceSourceWords("search:prowlarr-therarbg")).toBe("Search (prowlarr-therarbg)");
    expect(evidenceSourceWords("arr_history:sonarr_4k_zurg")).toBe("Sonarr/Radarr history (sonarr_4k_zurg)");
    expect(evidenceSourceWords("shadow_search:nyaa")).toBe("Watch-only search (nyaa)");
    expect(evidenceSourceWords("tmdb:US")).toBe("TMDb release note (US)");
    expect(evidenceSourceWords("new_origin:x")).toBe("New origin (x)");
    expect(evidenceSourceWords("")).toBe("");
  });
});

describe("TMDb words", () => {
  it("statuses and release types", () => {
    expect(titleStatusWords("inCinemas")).toBe("In cinemas");
    expect(titleStatusWords("released")).toBe("Released");
    expect(titleStatusWords("Returning Series")).toBe("Continuing");
    expect(titleStatusWords("In Production")).toBe("Upcoming");
    expect(titleStatusWords("someStatus")).toBe("Some status");
    expect(releaseTypeWords("theatrical_limited")).toBe("Limited cinema release");
    expect(releaseTypeWords("theatrical")).toBe("In cinemas");
    expect(releaseTypeWords("tv")).toBe("TV");
    expect(releaseTypeWords("drive_in")).toBe("Drive in");
    expect(releaseEventWords("theatrical_limited")).toBe("Limited release");
    expect(releaseEventWords("tv")).toBe("TV release");
    expect(releaseEventWords("drive_in")).toBe("Drive in release");
    expect(releaseEventWords(undefined)).toBe("Release");
  });
});

describe("anime words", () => {
  it("formats, mapping statuses, roles, sources and orders", () => {
    expect(animeFormatWords("TVDB_SPECIAL")).toBe("TVDB special");
    expect(animeFormatWords("TV")).toBe("TV series");
    expect(animeFormatWords("COMPILATION_SET")).toBe("Compilation set");
    expect(mappingWords("auto")).toBe("Matched");
    expect(mappingWords("heuristic")).toBe("Matched by date and title");
    expect(mappingWords("odd_one")).toBe("Odd one");
    expect(mappingCounts({ auto: 5, heuristic: 1 })).toBe("Matched 5 · Matched by date and title 1");
    expect(mappingCounts(undefined)).toBe("");
    expect(roleWords("side")).toBe("Side story");
    expect(animeSourceWords("tvdb-only")).toBe("TVDB only");
    expect(orderWords("release")).toBe("Release order");
    expect(orderWords(undefined)).toBe("Order");
  });
});

describe("router words", () => {
  it("attempt outcomes and codes, cache states and sources, back-offs", () => {
    expect(attemptOutcomeWords("refused")).toBe("Refused");
    expect(attemptOutcomeWords("would_add")).toBe("Would add (dry run)");
    expect(attemptCodeWords("uncached_deleted")).toBe("not cached, our add deleted");
    expect(attemptCodeWords("rd_name_rule")).toBe("refused by Real-Debrid's name rule");
    expect(attemptCodeWords("brand_new")).toBe("Brand new");
    expect(cacheStateWords("miss")).toBe("not cached");
    expect(cacheStateWords("cached")).toBe("cached");
    expect(cacheSourceWords("router:miss")).toBe("from a search that found it nowhere");
    expect(cacheSourceWords("check:torbox")).toBe("from a cache check");
    expect(cacheSourceWords("add:rd")).toBe("from an add");
    expect(cacheSourceWords("feed:dmm")).toBe("from the dmm feed");
    expect(cacheSourceWords("other:thing")).toBe("other thing");
    expect(cacheSourceWords("plain")).toBe("plain");
    expect(backoffWords("indexer:nyaa")).toBe("Indexer nyaa is backed off");
    expect(backoffWords("provider:rd")).toBe("Provider rd is backed off");
    expect(backoffWords("odd")).toBe("odd is backed off");
  });
});

describe("torrent words", () => {
  it("states, journal actions and who ran them", () => {
    expect(torrentStateWords("downloaded", 100)).toBe("Complete");
    expect(torrentStateWords("downloading", 40.2)).toBe("Downloading 40%");
    expect(torrentStateWords("stalled_new", 100)).toBe("Stalled new");
    expect(torrentJournalWords("backup", "ok")).toBe("Backed up");
    expect(torrentJournalWords("delete", "failed")).toBe("Delete failed");
    expect(torrentJournalWords("delete", "deleted")).toBe("Deleted");
    expect(torrentJournalWords("restore", "added")).toBe("Restored");
    expect(torrentJournalWords("restore", "uncached")).toBe("Restored (downloading)");
    expect(torrentJournalWords("prune", "ok")).toBe("Prune");
    expect(torrentJournalWords("prune", "partial")).toBe("Prune partial");
    expect(torrentJournalBy("schedule")).toBe("Scheduled");
    expect(torrentJournalBy("nathan")).toBe("nathan");
  });
});

describe("error words", () => {
  it("statuses, sources and health-check components", () => {
    expect(errorStatusWords("seen")).toBe("Seen");
    expect(errorSourceWords("http")).toBe("Web request");
    expect(errorSourceWords("other")).toBe("Other");
    expect(errorComponentWords("VpnEgressCheck")).toBe("VPN egress");
    expect(errorComponentWords("BrandNewCheck")).toBe("Brand new");
    expect(errorComponentWords("GET /api/v1/journal")).toBe("GET /api/v1/journal");
    expect(errorComponentWords(undefined)).toBe("");
  });
});
