// The quality profile pages (docs/design/settings.md, "Profiles, presets and formats"): the overview of every profile and
// format with its TRaSH origin and score states, TRaSH presets, the decision impact of an edit and the default
// profile per kind of version.
import { useQuery } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { confirmationHeaders } from "./settings";

export type QualityOverview = Schemas["QualityOverview"];
export type ProfileSummary = Schemas["ProfileSummary"];
export type ScoreRow = Schemas["ProfileScoreRow"];
export type FormatSummary = Schemas["FormatSummary"];
export type OriginDiff = Schemas["ProfileOriginDiff"];
export type ProfileOrigin = Schemas["ProfileOrigin"];
export type Preset = Schemas["ProfilePreset"];
export type ProfileImpact = Schemas["ProfileImpact"];
export type ProfileDefaults = Schemas["ProfileDefaults"];
export type Category = "movie" | "series" | "anime_movie" | "anime_series";
export type Built = { profile: Record<string, unknown>; origin: ProfileOrigin; trash_ids: Record<string, string>; groups: Record<string, string>; unscored: string[]; notes: string[]; formats: number };

export const CATEGORY_LABEL: Record<Category, string> = { movie: "Movies", series: "Series", anime_movie: "Anime movies", anime_series: "Anime series" };
export const MODIFIER_LABEL: Record<string, string> = {
  fallbacks_1080p: "Add 1080p fallbacks below the top quality",
  bluray_2160p: "Allow Bluray-2160p encodes",
  block_dv_no_hdr: "Block DV without HDR fallback",
};
export const MODIFIER_SHORT: Record<string, string> = { fallbacks_1080p: "1080p fallbacks", bluray_2160p: "Bluray-2160p", block_dv_no_hdr: "DV w/o HDR blocked" };

/** The category a version belongs to: anime "both" (null) counts as regular. */
export const categoryOf = (kind: string, anime: boolean | null | undefined): Category =>
  kind === "series" ? (anime ? "anime_series" : "series") : anime ? "anime_movie" : "movie";

/** "WEB 2160p + 1080p fallbacks": a stored or guessed origin in words. */
export const originLabel = (o: ProfileOrigin) => [o.name, ...(o.modifiers ?? []).map((m) => MODIFIER_SHORT[m] ?? m)].join(" + ");

export const overviewKey = ["settings", "quality-overview"] as const;
export const useQualityOverview = () => useQuery({ queryKey: overviewKey, queryFn: () => api<QualityOverview>("/settings/quality/overview") });
export type Presets = { flavour: string; presets: Preset[]; unavailable?: string; source?: "bundled" | "checkout"; commit?: string; commit_date?: string };
export const usePresets = (flavour: string, enabled = true) =>
  useQuery({ queryKey: ["settings", "presets", flavour], queryFn: () => api<Presets>("/settings/presets" + qs({ flavour })), enabled });

/** The stored profiles made from a TRaSH preset (a stored origin, not a guessed one), by key. */
export const presetCopies = (profiles: ProfileSummary[], flavour: string, trashID: string) =>
  profiles.filter((p) => !p.guessed && p.origin?.preset === trashID && (p.origin.flavour || flavour) === flavour).map((p) => p.key).sort();

/** A record key from a name ("WEB-2160p (Alternative)" gives "web-2160p-alternative"). */
export const keyFromName = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "profile";

/** base when no record uses it, else base-2, base-3, …: a profile from a preset needs a key no profile or format set has. */
export function freeKey(base: string, taken: Iterable<string>): string {
  const used = new Set(taken);
  if (!used.has(base)) return base;
  for (let n = 2; ; n++) if (!used.has(`${base}-${n}`)) return `${base}-${n}`;
}
export const buildPreset = (flavour: string, preset: string, modifiers: string[]) =>
  api<Built>("/settings/presets/build", { method: "POST", body: { flavour, preset, modifiers } });
export const createFromPreset = (body: { generation: number; key: string; flavour: string; preset: string; modifiers: string[]; config?: Record<string, unknown> }, token?: string) =>
  api<{ generation: number; key: string; binding: string }>("/settings/presets/create", { method: "POST", body, headers: confirmationHeaders(token) });
export const profileImpact = (generation: number, key: string, config: Record<string, unknown>, signal?: AbortSignal) =>
  api<ProfileImpact>("/settings/profiles/impact", { method: "POST", body: { generation, key, config }, signal });
export const saveDefaults = (defaults: Record<string, string>) => api<ProfileDefaults>("/settings/profile-defaults", { method: "PUT", body: { defaults } });
