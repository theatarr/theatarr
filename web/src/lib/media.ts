// A file's media info in one line: "1920x1080 · x265 10-bit · HDR10 · EAC3 5.1 · Japanese, English · subtitles
// English, Arabic". Language codes (ISO 639, "eng" or "en") read as English names; one the browser does not know stays
// as written. A long subtitle list is cut to its first SUBS_SHOWN languages and "+N" (subsList has them all, for a
// tooltip).
import type { MediaInfo } from "@/api/manage";

let names: Intl.DisplayNames | null | undefined;

/** "English" for "eng" or "en"; the code itself when there is no name ("und", an unknown code). */
export function languageName(code: string): string {
  const c = code.trim();
  if (!/^[a-z]{2,3}$/i.test(c) || c.toLowerCase() === "und") return c;
  if (names === undefined) {
    try {
      names = new Intl.DisplayNames(["en"], { type: "language" });
    } catch {
      names = null;
    }
  }
  try {
    const n = names?.of(c.toLowerCase());
    return n && n.toLowerCase() !== c.toLowerCase() && n !== "root" ? n : c;
  } catch {
    return c;
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
  const video = [m.video_codec, m.video_bit_depth && m.video_bit_depth > 8 ? `${m.video_bit_depth}-bit` : ""].filter(Boolean).join(" ");
  const audio = audioWords(m);
  const subs = subsWords(m.subtitles);
  return [m.resolution, video, m.video_dynamic_range, audio, languages(m.audio_languages), subs, m.run_time].filter(Boolean).join(" · ");
}

/** The audio codec and channels: "EAC3 Atmos 5.1". */
export function audioWords(m: MediaInfo | undefined | null): string {
  if (!m) return "";
  return [m.audio_codec, m.audio_channels ? String(m.audio_channels) : ""].filter(Boolean).join(" ");
}

const SUBS_SHOWN = 4;

/** Every subtitle language once, in order, by name: "English, Arabic, Chinese". */
export function subsList(subtitles: string | undefined | null): string {
  return subsNames(subtitles).join(", ");
}

/** Every subtitle language once, in order, by name. */
export function subsNames(subtitles: string | undefined | null): string[] {
  return subsCodes(subtitles).map(languageName);
}

function subsCodes(subtitles: string | undefined | null): string[] {
  return (subtitles ?? "").split("/").filter((s, i, all) => s && all.indexOf(s) === i);
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
