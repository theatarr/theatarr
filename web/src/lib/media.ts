// A file's media info in one line: "1920x1080 · x265 10-bit · HDR10 · EAC3 5.1 · Japanese, English · subtitles
// English, Arabic". Language codes (ISO 639, "eng" or "en") read as English names; one the browser does not know stays
// as written. A long subtitle list is cut to its first SUBS_SHOWN languages and "+N" (subsList has them all, for a
// tooltip).
import type { MediaInfo } from "@/api/manage";

let names: Intl.DisplayNames | null | undefined;

/** Names for codes a browser may lack (Intl.DisplayNames returns the code). */
const FALLBACK_NAMES: Record<string, string> = { enm: "Middle English" };

/** "English" for "eng" or "en", "Brazilian Portuguese" for "pt-BR"; the code itself when there is no name ("und", an
 *  unknown code). */
export function languageName(code: string): string {
  const c = code.trim();
  if (!/^[a-z]{2,3}(-([a-z]{2}|\d{3}))?$/i.test(c) || c.toLowerCase() === "und") return c;
  const fallback = FALLBACK_NAMES[c.toLowerCase()];
  if (names === undefined) {
    try {
      names = new Intl.DisplayNames(["en"], { type: "language" });
    } catch {
      names = null;
    }
  }
  try {
    const n = names?.of(c.toLowerCase());
    return n && n.toLowerCase() !== c.toLowerCase() && n !== "root" ? n : (fallback ?? c);
  } catch {
    return fallback ?? c;
  }
}

/** "jpn/eng" as "Japanese, English". */
export function languages(list: string | undefined | null): string {
  return languageNames(list).join(", ");
}

/** "jpn/eng" as ["Japanese", "English"]: each language once, in first-seen order (a file with seven Russian tracks
 *  reads "Russian" once). */
export function languageNames(list: string | undefined | null): string[] {
  return [...new Set((list ?? "").split("/").filter(Boolean).map(languageName))];
}

/** The first shown names and how many more: "English, Arabic +34". */
export function shortList(names: string[], shown: number): string {
  const more = names.length - shown;
  return `${names.slice(0, shown).join(", ")}${more > 0 ? ` +${more}` : ""}`;
}

export function mediaLine(m: MediaInfo | undefined | null): string {
  if (!m) return "";
  const audio = audioWords(m);
  const subs = subsWords(m.subtitles);
  return [videoWords(m), audio, languages(m.audio_languages), subs, m.run_time].filter(Boolean).join(" · ");
}

/** The video: "3840x2160 · x265 10-bit · HDR10". The compact line names a bit depth above 8 only; the Media group
 *  (every) names any stored one. */
export function videoWords(m: MediaInfo | undefined | null, every = false): string {
  if (!m) return "";
  const depth = m.video_bit_depth ?? 0;
  const codec = [m.video_codec, depth > (every ? 0 : 8) ? `${depth}-bit` : ""].filter(Boolean).join(" ");
  return [m.resolution, codec, m.video_dynamic_range].filter(Boolean).join(" · ");
}

/** The Audio row's lines: each probed track on its own line ("Japanese · FLAC 2.0", the codec alone for a track
 *  without a language or "und"); for a taken-over file whose arr counted several tracks, the count and the first
 *  ("13 tracks; first: TrueHD Atmos 7.1"); else the first track's codec and channels ("TrueHD Atmos 7.1"). */
export function audioLines(m: MediaInfo | undefined | null): string[] {
  const tracks = m?.audio_tracks ?? [];
  if (tracks.length > 0) {
    const lang = (code = "") => (code.trim() === "" || code.trim().toLowerCase() === "und" ? "" : languageName(code));
    return tracks.map((t) => [lang(t.language), audioWords({ audio_codec: t.codec, audio_channels: t.channels })].filter(Boolean).join(" · ")).filter(Boolean);
  }
  const one = audioWords(m);
  if ((m?.audio_streams ?? 0) > 1) return [`${m?.audio_streams} tracks${one ? `; first: ${one}` : ""}`];
  return one ? [one] : [];
}

/** The Media group (a row's Details, the episode window): one row per fact the file has, each in full. Audio
 *  languages only without a track list (a taken-over file): the Audio row lists the tracks with theirs. */
export function mediaRows(m: MediaInfo | undefined | null): [string, string[]][] {
  if (!m) return [];
  const rows: [string, string[]][] = [];
  const add = (label: string, lines: string[]) => {
    const kept = lines.filter(Boolean);
    if (kept.length) rows.push([label, kept]);
  };
  add("Video", [videoWords(m, true)]);
  add("Audio", audioLines(m));
  if ((m.audio_tracks ?? []).length === 0) add("Audio languages", [languages(m.audio_languages)]);
  add("Subtitles", [subsList(m.subtitles)]);
  add("Runtime", [m.run_time ?? ""]);
  return rows;
}

/** The audio codec and channels with one decimal, as the arrs and file naming write them: "EAC3 Atmos 5.1",
 *  "FLAC 2.0". */
export function audioWords(m: MediaInfo | undefined | null): string {
  if (!m) return "";
  return [m.audio_codec, m.audio_channels ? m.audio_channels.toFixed(1) : ""].filter(Boolean).join(" ");
}

const SUBS_SHOWN = 4;

/** Every subtitle language once, in order, by name: "English, Arabic, Chinese". */
export function subsList(subtitles: string | undefined | null): string {
  return subsNames(subtitles).join(", ");
}

/** Every subtitle language once, in order, by name: once by name, so "eng" and "en" read English once. */
export function subsNames(subtitles: string | undefined | null): string[] {
  return languageNames(subtitles);
}

/** "English, Arabic, Chinese, Croatian +23": the first SUBS_SHOWN subtitle languages and how many more. */
export function subsShort(subtitles: string | undefined | null): string {
  return shortList(subsNames(subtitles), SUBS_SHOWN);
}

/** "subtitles English, Arabic, Chinese, Croatian +23". */
function subsWords(subtitles: string | undefined | null): string {
  const s = subsShort(subtitles);
  return s ? `subtitles ${s}` : "";
}
