// TMDb (and Radarr/Sonarr) words for a title's status and its release types, as the About tab, the Movies "Next"
// column and the admin release dates print them. Unknown values fall back to the code made readable.
import { wordsFor } from "./code-words";

// A movie reads Announced, In cinemas or Released; a series Continuing, Ended, Cancelled or Upcoming (the Movies and
// Series "status" column and the About tab use the same words).
const STATUS: Record<string, string> = {
  announced: "Announced",
  tba: "Announced",
  rumored: "Announced",
  incinemas: "In cinemas",
  released: "Released",
  deleted: "Removed from TMDb",
  "returning series": "Continuing",
  continuing: "Continuing",
  ended: "Ended",
  canceled: "Cancelled",
  cancelled: "Cancelled",
  "in production": "Upcoming",
  "post production": "Upcoming",
  planned: "Upcoming",
  pilot: "Upcoming",
  upcoming: "Upcoming",
};

/** A title's status in words: "inCinemas" → "In cinemas", "Returning Series" → "Continuing". */
export const titleStatusWords = (s: string | null | undefined) => (s ? STATUS[s.toLowerCase()] ?? wordsFor({}, s) : "");

const RELEASE: Record<string, string> = {
  premiere: "Premiere",
  theatrical_limited: "Limited cinema release",
  theatrical: "In cinemas",
  digital: "Digital",
  physical: "Physical",
  tv: "TV",
};

/** A release type in words: "theatrical_limited" → "Limited cinema release". */
export const releaseTypeWords = (t: string | null | undefined) => wordsFor(RELEASE, t);

const RELEASE_EVENT: Record<string, string> = {
  premiere: "Premiere",
  theatrical_limited: "Limited release",
  theatrical: "Cinema release",
  digital: "Digital release",
  physical: "Physical release",
  tv: "TV release",
};

/** A release as an event, for "<words> · Nov 16" lines: "digital" → "Digital release", "tv" → "TV release". */
export const releaseEventWords = (t: string | null | undefined) => (t ? RELEASE_EVENT[t] ?? `${wordsFor({}, t)} release` : "Release");
