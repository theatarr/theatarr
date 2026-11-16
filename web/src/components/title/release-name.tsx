// A release name with its facts coloured (lib/release-highlight): dynamic range, audio, resolution and source each in
// their own text colour (the --rel-* tokens in styles/globals.css), same font and weight. The element's text is the
// name unchanged, so copying it and anything that reads it keep working.
import { highlightRelease } from "@/lib/release-highlight";

const COLOUR = {
  hdr: "text-[var(--rel-hdr)]",
  audio: "text-[var(--rel-audio)]",
  res: "text-[var(--rel-res)]",
  source: "text-[var(--rel-source)]",
} as const;

export function ReleaseName({ name }: { name: string | undefined | null }) {
  if (!name) return null;
  return (
    <>
      {highlightRelease(name).map((s, i) => (s.kind ? <span key={i} className={COLOUR[s.kind]} data-fact={s.kind}>{s.text}</span> : s.text))}
    </>
  );
}
