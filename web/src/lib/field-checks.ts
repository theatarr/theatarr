// Inline checks of a few settings values before Save (docs/design/ui.md "Settings forms"): the same rules the server
// applies on save (internal/policy: parseClock, checkURL, checkKey), said under the field while typing, so Save can be
// held with the reason instead of answering with a refusal. The server stays the authority.

/** A daily time as Go's "15:04" reads it: 0:00 to 23:59, minutes in two digits. */
export function clockProblem(v: string): string | undefined {
  return /^([01]?\d|2[0-3]):[0-5]\d$/.test(v.trim()) ? undefined : "Enter a time as HH:MM, from 00:00 to 23:59.";
}

/** An http(s) address with a host. */
export function urlProblem(v: string): string | undefined {
  try {
    const u = new URL(v.trim());
    if ((u.protocol === "http:" || u.protocol === "https:") && u.host) return undefined;
  } catch {
    // not a URL
  }
  return "Enter a full address that starts with http:// or https://.";
}

/** A record key (a stable name other settings refer to). */
export function keyProblem(v: string): string | undefined {
  if (!v.trim()) return "Enter a name.";
  return /^[a-z0-9][a-z0-9_-]*$/.test(v) ? undefined : "Use lower case letters, digits, - and _, starting with a letter or digit.";
}

// The settings sections' fields the server checks as http(s) addresses (checkURL in internal/policy).
const URL_PATHS = [
  /^cardigann\.flaresolverr_url$/,
  /^cardigann\.prowlarr\.url$/,
  /^egress\.check_url$/,
  /^library\.mount\.(zurg_log|zurg_api)$/,
  /^library\.mount\.(rclone_rc|decypharr)\.url$/,
  /^library\.plex\.url$/,
  /^metadata\.sources\.[^.]+\.url$/,
];

/** The inline problem of a settings section value at path, if any (empty values are left to the server: most fields
 * take their default then). */
export function sectionProblem(path: string, value: unknown): string | undefined {
  if (typeof value !== "string" || value === "") return undefined;
  if (path === "notify.digest_at") return clockProblem(value);
  if (URL_PATHS.some((re) => re.test(path))) return urlProblem(value);
  return undefined;
}

/** The inline problems of a section draft, by path, for the values that differ from the saved ones (a stored value
 * is the server's business). */
export function sectionProblems(value: unknown, saved: unknown, path: string): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (v: unknown, s: unknown, p: string) => {
    if (v && typeof v === "object" && !Array.isArray(v)) {
      const so = s && typeof s === "object" && !Array.isArray(s) ? (s as Record<string, unknown>) : {};
      for (const [k, x] of Object.entries(v as Record<string, unknown>)) walk(x, so[k], `${p}.${k}`);
      return;
    }
    if (v === s) return;
    const problem = sectionProblem(p, v);
    if (problem) out[p] = problem;
  };
  walk(value, saved, path);
  return out;
}
