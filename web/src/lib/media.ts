// A file's media info in one line: "1920x1080 · x265 10-bit · HDR10 · EAC3 5.1 · jpn/eng · subs eng/ara".
import type { MediaInfo } from "@/api/manage";

export function mediaLine(m: MediaInfo | undefined | null): string {
  if (!m) return "";
  const video = [m.video_codec, m.video_bit_depth && m.video_bit_depth > 8 ? `${m.video_bit_depth}-bit` : ""].filter(Boolean).join(" ");
  const audio = [m.audio_codec, m.audio_channels ? String(m.audio_channels) : ""].filter(Boolean).join(" ");
  const subs = m.subtitles ? `subs ${m.subtitles.split("/").filter((s, i, all) => all.indexOf(s) === i).join("/")}` : "";
  return [m.resolution, video, m.video_dynamic_range, audio, m.audio_languages, subs, m.run_time].filter(Boolean).join(" · ");
}
