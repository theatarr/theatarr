// A file's media info in one line: "1920x1080 · x265 10-bit · HDR10 · EAC3 5.1 · jpn/eng · subs eng/ara". A long
// subtitle list is cut to its first SUBS_SHOWN languages and "+N" (subsList has them all, for a tooltip).
import type { MediaInfo } from "@/api/manage";

export function mediaLine(m: MediaInfo | undefined | null): string {
  if (!m) return "";
  const video = [m.video_codec, m.video_bit_depth && m.video_bit_depth > 8 ? `${m.video_bit_depth}-bit` : ""].filter(Boolean).join(" ");
  const audio = [m.audio_codec, m.audio_channels ? String(m.audio_channels) : ""].filter(Boolean).join(" ");
  const subs = subsWords(m.subtitles);
  return [m.resolution, video, m.video_dynamic_range, audio, m.audio_languages, subs, m.run_time].filter(Boolean).join(" · ");
}

const SUBS_SHOWN = 4;

/** Every subtitle language once, in order: "eng/ara/zho". */
export function subsList(subtitles: string | undefined | null): string {
  return (subtitles ?? "").split("/").filter((s, i, all) => s && all.indexOf(s) === i).join("/");
}

/** "subs eng/ara/zho/hrv +23": the first SUBS_SHOWN languages and how many more. */
function subsWords(subtitles: string | undefined | null): string {
  const all = subsList(subtitles).split("/").filter(Boolean);
  if (!all.length) return "";
  const more = all.length - SUBS_SHOWN;
  return `subs ${all.slice(0, SUBS_SHOWN).join("/")}${more > 0 ? ` +${more}` : ""}`;
}
