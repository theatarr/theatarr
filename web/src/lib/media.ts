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
function languages(list: string | undefined | null): string {
  return (list ?? "").split("/").filter(Boolean).map(languageName).join(", ");
}

export function mediaLine(m: MediaInfo | undefined | null): string {
  if (!m) return "";
  const video = [m.video_codec, m.video_bit_depth && m.video_bit_depth > 8 ? `${m.video_bit_depth}-bit` : ""].filter(Boolean).join(" ");
  const audio = [m.audio_codec, m.audio_channels ? String(m.audio_channels) : ""].filter(Boolean).join(" ");
  const subs = subsWords(m.subtitles);
  return [m.resolution, video, m.video_dynamic_range, audio, languages(m.audio_languages), subs, m.run_time].filter(Boolean).join(" · ");
}

const SUBS_SHOWN = 4;

/** Every subtitle language once, in order, by name: "English, Arabic, Chinese". */
export function subsList(subtitles: string | undefined | null): string {
  return subsCodes(subtitles).map(languageName).join(", ");
}

function subsCodes(subtitles: string | undefined | null): string[] {
  return (subtitles ?? "").split("/").filter((s, i, all) => s && all.indexOf(s) === i);
}

/** "subtitles English, Arabic, Chinese, Croatian +23": the first SUBS_SHOWN languages and how many more. */
function subsWords(subtitles: string | undefined | null): string {
  const all = subsCodes(subtitles);
  if (!all.length) return "";
  const more = all.length - SUBS_SHOWN;
  return `subtitles ${all.slice(0, SUBS_SHOWN).map(languageName).join(", ")}${more > 0 ? ` +${more}` : ""}`;
}
