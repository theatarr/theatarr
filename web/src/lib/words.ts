// The one rule for free-text filter boxes (docs/design/ui.md, Product rules, "Filter boxes"): the typed text is
// split on whitespace and every word must be found, in any order, case-insensitively, as a substring. The server's
// LIKE filters follow the same rule (internal/textsearch).

/** How many words a filter uses; the rest are ignored. */
export const MAX_WORDS = 10;

/** The typed text's words, lower-cased, at most MAX_WORDS. */
export function searchWords(text: string): string[] {
  return text.toLowerCase().split(/\s+/).filter(Boolean).slice(0, MAX_WORDS);
}

type Haystack = string | undefined | null;

/** A test for one typed text, to filter a list row by row: whether every word is in at least one haystack. Empty
 * text matches everything. */
export function wordMatcher(text: string): (...haystacks: Haystack[]) => boolean {
  const words = searchWords(text);
  if (!words.length) return () => true;
  return (...haystacks) => {
    const all = haystacks.filter((h): h is string => !!h).join("\0").toLowerCase();
    return words.every((w) => all.includes(w));
  };
}

/** wordMatcher for a single test. */
export function matchesWords(text: string, ...haystacks: Haystack[]): boolean {
  return wordMatcher(text)(...haystacks);
}
