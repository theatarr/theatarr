// The facts in a release name, for colouring them in the release list (docs/design/ui.md "Interactive search"): dynamic
// range (DV, HDR10+ …), audio (Atmos, TrueHD 7.1, DDP5.1 …), resolution (2160p, UHD …) and source (REMUX, WEB-DL …).
// Only whole tokens count: a token is bounded by the ends of the name or one of ". _ - space [ ] ( ) , +" (the "+" of
// HDR10+ and DD+ is part of the token); a word of two parts matches across one ". _ - space". At each place the longest
// match wins (HDR10+ over HDR10 over HDR). A bare WEB is a source only after a resolution token, so the film Madame Web
// keeps its title plain. Everything else (title, year, episode, codec, group) stays plain; the segments join to the name.

export type ReleaseFact = "hdr" | "audio" | "res" | "source";
export interface ReleaseSegment {
  text: string;
  kind?: ReleaseFact;
}

const SEP = /[._\- [\](),+]/;
/** Between the parts of a two-part word. */
const J = "[._\\- ]";
/** A channel count written right after an audio codec: "5.1", "7 1", ".7.1", " 2.0"; nothing looser. */
const CH = "(?:[. ]?[12567][. ][01])";
/** The token ends at the end of the name or a separator. */
const END = "(?=$|[._\\- [\\](),+])";

const RULES: { kind: ReleaseFact; re: RegExp; afterRes?: boolean }[] = [
  ...[
    "DV",
    "DoVi",
    `Dolby${J}Vision`,
    "HDR10\\+",
    "HDR10Plus",
    `HDR10${J}PLUS`,
    "HDR10P",
    "HDR10",
    "HDR",
    "HLG",
  ].map((p) => ({ kind: "hdr" as const, p })),
  ...[
    "Atmos",
    "TrueHD",
    "DTS[-:]X",
    `DTS${J}HD${J}MA`,
    `DTS${J}HD${J}HRA`,
    `DTS${J}HD`,
    "DTS",
    "DDP",
    "DD\\+",
    "E-?AC-?3",
    "AC-?3",
    "AAC",
    "FLAC",
    "LPCM",
    "Opus",
  ].map((p) => ({ kind: "audio" as const, p: `${p}${CH}?` })),
  // DD alone says nothing; with a channel count (DD5.1) it is Dolby Digital.
  { kind: "audio" as const, p: `DD${CH}` },
  ...["2160p", "4K", "UHD", "1080p", "1080i", "720p", "576p", "480p"].map((p) => ({ kind: "res" as const, p })),
  ...["REMUX", `Blu${J}?Ray`, "BDRip", "BRRip", `WEB${J}?DL`, `WEB${J}?Rip`, "HDTV", "DVDRip"].map((p) => ({ kind: "source" as const, p })),
  { kind: "source" as const, p: "WEB", afterRes: true },
].map(({ p, ...r }) => ({ ...r, re: new RegExp(`${p}${END}`, "iy") }));

/** The release name cut into plain and coloured parts; joined they give the name back exactly. */
export function highlightRelease(name: string): ReleaseSegment[] {
  const out: ReleaseSegment[] = [];
  const plain = (t: string) => {
    const last = out[out.length - 1];
    if (last && !last.kind) last.text += t;
    else out.push({ text: t });
  };
  let seenRes = false;
  let i = 0;
  while (i < name.length) {
    let best: { kind: ReleaseFact; len: number } | undefined;
    if (i === 0 || SEP.test(name[i - 1])) {
      for (const r of RULES) {
        if (r.afterRes && !seenRes) continue;
        r.re.lastIndex = i;
        const m = r.re.exec(name);
        if (m && m[0].length > 0 && (!best || m[0].length > best.len)) best = { kind: r.kind, len: m[0].length };
      }
    }
    if (best) {
      out.push({ text: name.slice(i, i + best.len), kind: best.kind });
      if (best.kind === "res") seenRes = true;
      i += best.len;
    } else {
      plain(name[i]);
      i++;
    }
  }
  return out;
}
