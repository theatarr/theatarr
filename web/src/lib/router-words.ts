// The router's codes in words (internal/router, internal/provider/adder): what each provider attempt came to, its
// step code, and the cache evidence a hash has. Unknown codes are made readable; the raw code stays in the title.
import { wordsFor } from "./code-words";

const OUTCOMES: Record<string, string> = {
  accepted: "Added",
  deferred: "Waiting",
  skipped: "Skipped",
  refused: "Refused",
  miss: "Not cached",
  error: "Error",
  cancelled: "Cancelled",
  would_add: "Would add (dry run)",
};

/** An attempt's outcome: "refused" → "Refused", "miss" → "Not cached". */
export const attemptOutcomeWords = (o: string | null | undefined) => wordsFor(OUTCOMES, o);

const CODES: Record<string, string> = {
  added: "added",
  add: "added",
  reused: "already on the account",
  paced: "waiting for the provider's pace",
  miss: "not cached",
  known_miss: "known not cached",
  known_uncached: "known not cached",
  torrent_not_cached: "not cached",
  uncached_deleted: "not cached, our add deleted",
  uncached_kept: "not cached, our add kept",
  uncached_kept_shared: "not cached, kept (may not be ours)",
  provider_done: "provider not tried again in this search",
  alt_selection_skipped: "second file selection not tried",
  cached_first: "cached on another provider first",
  add_unsettled: "add not settled yet",
  held_unread: "held, not readable yet",
  download_moved_on: "the download moved on",
  reconciled: "settled from the account",
  upgrades_off: "upgrades are off",
  rd_name_rule: "refused by Real-Debrid's name rule",
  torrent_blocked: "refused by the provider",
  account_hold: "Real-Debrid holds the account",
  too_many_active: "too many active downloads",
  too_many_active_downloads: "too many active downloads",
  traffic_exceeded: "account allowance spent",
  rate_limited: "too many requests",
  auth: "key refused",
  bad_token: "key refused",
  no_key: "no API key",
  invalid: "magnet refused as invalid",
  unavailable: "provider not reachable",
  unknown: "unrecognised answer",
  blocklisted: "release blocked",
  private: "private torrent",
  no_hash: "no torrent hash",
  hash_conflict: "hash already used by another download",
  in_flight: "already being added",
  route_cancelled: "search cancelled",
  no_provider: "no provider",
  unknown_provider: "provider not configured",
  not_debrid: "not a debrid provider",
  no_client: "no download client",
  client_add: "sent to the download client",
  client_adopted: "taken from the download client",
  client_deferred: "download client busy",
  client_not_cached: "not cached (download client)",
  client_refused: "refused by the download client",
  indexer_providers: "indexer limits the providers",
};

/** An attempt's step code: "uncached_deleted" → "not cached, our add deleted". */
export const attemptCodeWords = (c: string | null | undefined) => wordsFor(CODES, c);

const CACHE_STATES: Record<string, string> = {
  cached: "cached",
  uncached: "not cached",
  miss: "not cached",
};

/** A provider's cache state for a hash. */
export const cacheStateWords = (s: string | null | undefined) => wordsFor(CACHE_STATES, s).toLowerCase();

const CACHE_SOURCES: Record<string, string> = {
  "router:add": "from an add",
  "router:check": "from a cache check",
  "router:miss": "from a search that found it nowhere",
  "router:451": "refused by the provider",
  "router:client": "from the download client",
  check: "from a cache check",
  add: "from an add",
  feed: "from a cached feed",
};

/** Where cache evidence came from: "router:miss" → "from a search that found it nowhere", "feed:dmm" → "from the
 *  dmm feed". */
export function cacheSourceWords(src: string | null | undefined): string {
  if (!src) return "";
  if (CACHE_SOURCES[src]) return CACHE_SOURCES[src];
  const i = src.indexOf(":");
  const head = i < 0 ? src : src.slice(0, i);
  const rest = i < 0 ? "" : src.slice(i + 1);
  if (head === "feed" && rest) return `from the ${rest} feed`;
  return CACHE_SOURCES[head] ?? wordsFor({}, src).toLowerCase();
}

const BACKOFF_KINDS: Record<string, string> = { provider: "Provider", indexer: "Indexer", budget: "Request budget" };

/** A gateway back-off's subject as a sentence: "indexer:nyaa" → "Indexer nyaa is backed off". */
export function backoffWords(subject: string | null | undefined): string {
  const s = subject ?? "";
  const i = s.indexOf(":");
  const kind = i < 0 ? "" : BACKOFF_KINDS[s.slice(0, i)];
  return kind ? `${kind} ${s.slice(i + 1)} is backed off` : `${s || "A source"} is backed off`;
}
