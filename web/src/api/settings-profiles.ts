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
export const usePresets = (flavour: string, enabled = true) =>
  useQuery({ queryKey: ["settings", "presets", flavour], queryFn: () => api<{ flavour: string; presets: Preset[]; unavailable?: string }>("/settings/presets" + qs({ flavour })), enabled });
export const buildPreset = (flavour: string, preset: string, modifiers: string[]) =>
  api<Built>("/settings/presets/build", { method: "POST", body: { flavour, preset, modifiers } });
export const createFromPreset = (body: { generation: number; key: string; flavour: string; preset: string; modifiers: string[]; config?: Record<string, unknown> }, token?: string) =>
  api<{ generation: number; key: string; binding: string }>("/settings/presets/create", { method: "POST", body, headers: confirmationHeaders(token) });
export const profileImpact = (generation: number, key: string, config: Record<string, unknown>, signal?: AbortSignal) =>
  api<ProfileImpact>("/settings/profiles/impact", { method: "POST", body: { generation, key, config }, signal });
export const saveDefaults = (defaults: Record<string, string>) => api<ProfileDefaults>("/settings/profile-defaults", { method: "PUT", body: { defaults } });
