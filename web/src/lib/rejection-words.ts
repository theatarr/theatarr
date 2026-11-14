// Why a release was rejected, in words (the decision engine's reasons, internal/decide/profile/decision.go and the
// parse checks in internal/decide). Unknown reasons fall back to the code made readable (code-words.ts).
import { wordsFor } from "./code-words";

const REJECTIONS: Record<string, string> = {
  // Parsing and identity.
  UnableToParse: "Name not understood",
  Unknown: "Not matched",
  UnknownMovie: "Not this movie",
  UnknownSeries: "Not this series",
  UnknownEpisode: "Episode not recognised",
  WrongMovie: "A different movie",
  WrongSeries: "A different series",
  WrongSeason: "A different season",
  WrongEpisode: "A different episode",
  MatchesAnotherSeries: "Matches another series",
  AmbiguousNumbering: "Episode numbers unclear",
  SplitEpisode: "Split episode",
  TitleMissing: "Episode title missing",
  TitleTba: "Episode title not announced",
  NoInfoHash: "No torrent hash",
  IndexerNotInVersion: "Indexer not used by this library",
  Availability: "Not released yet",
  Error: "Could not be checked",
  DecisionError: "Could not be checked",
  // Size and content.
  UnknownRuntime: "Runtime unknown",
  BelowMinimumSize: "Too small",
  AboveMaximumSize: "Too large",
  MaximumSizeExceeded: "Too large",
  Sample: "A sample",
  Raw: "Raw disc files",
  MinimumSeeders: "Too few seeders",
  // Quality and formats.
  QualityNotWanted: "Quality not wanted",
  QualityUpgradesDisabled: "Upgrades are off",
  CustomFormatMinimumScore: "Format score too low",
  WantedLanguage: "Wrong language",
  SeaDexQualityPending: "Waiting for SeaDex",
  // Repacks and groups.
  PropersDisabled: "Propers are off",
  ProperForOldFile: "Proper for an older file",
  RepackDisabled: "Repacks are off",
  RepackUnknownReleaseGroup: "Repack from an unknown group",
  RepackReleaseGroupDoesNotMatch: "Repack from another group",
  UnknownReleaseGroup: "Unknown release group",
  ReleaseGroupDoesNotMatch: "From another release group",
  // Seasons and episodes.
  EpisodeNotMonitored: "Episode not monitored",
  SeriesNotMonitored: "Series not monitored",
  FullSeason: "Full season pack",
  FullSeasonNotAired: "Season not fully aired",
  MultiSeason: "Several seasons",
  NotSeasonPack: "Not a season pack",
  ExistingFileHasMoreEpisodes: "The file held has more episodes",
  // Against the file held.
  DiskHigherPreference: "Not better than the file held",
  DiskHigherRevision: "The file held is a newer revision",
  DiskCutoffMet: "The file held meets the cutoff",
  DiskCustomFormatCutoffMet: "The file held meets the format cutoff",
  DiskCustomFormatScore: "Format score not above the file held",
  DiskCustomFormatScoreIncrement: "Format score gain too small",
  DiskUpgradesNotAllowed: "Upgrades are off",
  AlreadyImportedSameHash: "Already imported",
  AlreadyImportedSameName: "Already imported",
};

/** A rejection reason in words; the code stays in the element's title. */
export const rejectionWords = (code: string | null | undefined) => wordsFor(REJECTIONS, code);
