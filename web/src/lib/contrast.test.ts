// The WCAG AA pairs of the settings pages and of the theme, measured on the theme tokens in styles/globals.css, the
// old names through their aliases: text needs 4.5:1, control boundaries (outline buttons, inputs) and state marks 3:1.
import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SEARCH_TRIGGER } from "@/components/shell/app-shell";
import { buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SELECT_CLASS } from "@/components/ui/select";

const css = readFileSync(new URL("../styles/globals.css", import.meta.url), "utf8");

function block(selector: string): Record<string, string> {
  const start = css.indexOf(selector + " {");
  if (start < 0) throw new Error(`no ${selector} block`);
  const body = css.slice(css.indexOf("{", start) + 1, css.indexOf("\n}", start));
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/(--[\w-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

const light = block(":root");
const dark = { ...light, ...block(".dark") };

/** A token's colour as hex, following var() references. */
function token(theme: Record<string, string>, name: string): string {
  let v = theme[name];
  for (let i = 0; v && i < 8; i++) {
    const ref = /^var\((--[\w-]+)\)$/.exec(v);
    if (!ref) break;
    v = theme[ref[1]];
  }
  if (!v || !/^#[0-9a-f]{6}$/i.test(v)) throw new Error(`${name} is not a plain colour: ${v}`);
  return v;
}

type RGB = [number, number, number];
const rgb = (hex: string): RGB => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)) as RGB;
function luminance([r, g, b]: RGB): number {
  const ch = (v: number) => {
    v /= 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b);
}
function contrast(a: RGB, b: RGB): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

const surface = (theme: Record<string, string>) => rgb(token(theme, "--surface"));
const paper = (theme: Record<string, string>) => rgb(token(theme, "--bg"));

describe("theme contrast (WCAG AA)", () => {
  const pairs: [string, RGB, RGB, number][] = [];
  for (const [name, theme] of [["light", light], ["dark", dark]] as const) {
    for (const ink of ["--ink", "--ink-2", "--ink-3", "--accent-ink"]) {
      pairs.push([`${name} ${ink} text on the surface`, rgb(token(theme, ink)), surface(theme), 4.5]);
      pairs.push([`${name} ${ink} text on the paper`, rgb(token(theme, ink)), paper(theme), 4.5]);
    }
    pairs.push(
      [`${name} neutral badge (muted text on muted)`, rgb(token(theme, "--muted-foreground")), rgb(token(theme, "--muted")), 4.5],
      [`${name} old faint text on the card`, rgb(token(theme, "--th-faint")), rgb(token(theme, "--card")), 4.5],
      [`${name} ghost button text on the card`, rgb(token(theme, "--th-muted2")), rgb(token(theme, "--card")), 4.5],
      [`${name} outline button border on the card`, rgb(token(theme, "--th-control")), rgb(token(theme, "--card")), 3],
      [`${name} control border on the paper`, rgb(token(theme, "--control")), paper(theme), 3],
      [`${name} input border on its field`, rgb(token(theme, "--input")), rgb(token(theme, "--field")), 3],
      [`${name} focus ring on the surface`, rgb(token(theme, "--ring")), surface(theme), 3],
      [`${name} filled button text`, rgb(token(theme, "--primary-foreground")), rgb(token(theme, "--primary")), 4.5],
      [`${name} destructive button text`, rgb(token(theme, "--th-danger-on-solid")), rgb(token(theme, "--th-danger-solid")), 4.5],
      [`${name} success solid text`, rgb(token(theme, "--th-success-on-solid")), rgb(token(theme, "--th-success-solid")), 4.5],
    );
    for (const s of ["ok", "warn", "bad"]) {
      pairs.push([`${name} ${s} text on its tint`, rgb(token(theme, `--${s}-ink`)), rgb(token(theme, `--${s}-soft`)), 4.5]);
      pairs.push([`${name} ${s} mark on the surface`, rgb(token(theme, `--${s}`)), surface(theme), 3]);
    }
    pairs.push([`${name} accent text on its tint`, rgb(token(theme, "--accent-ink")), rgb(token(theme, "--accent-soft")), 4.5]);
  }
  it.each(pairs)("%s", (_name, fg, bg, need) => {
    expect(contrast(fg, bg)).toBeGreaterThanOrEqual(need);
  });
});

// The bordered controls as they are styled (their real class names, not a token picked by hand): each border needs 3:1
// against the surface and the paper it sits on, so an outline or secondary control stays visible in both themes.
const inputClass = /class="([^"]*)"/.exec(renderToStaticMarkup(createElement(Input, { "aria-label": "x" })))![1];
const CONTROLS: [string, string][] = [
  ["outline button", buttonVariants({ variant: "outline" })],
  ["secondary button", buttonVariants({ variant: "secondary" })],
  ["input", inputClass],
  ["select", SELECT_CLASS],
  ["top-bar search", SEARCH_TRIGGER],
];

/** The token a class list draws its resting border, fill or text with (border-input, bg-field, border-[var(--x)]). */
function used(classes: string, kind: "border" | "bg" | "text"): string {
  for (const c of classes.split(/\s+/)) {
    const v = new RegExp(`^${kind}-\\[var\\((--[\\w-]+)\\)\\]$`).exec(c);
    if (v) return v[1];
    const named: Record<string, string> = { "border-input": "--input", "bg-field": "--field", "text-foreground": "--foreground" };
    if (c.startsWith(kind + "-") && named[c]) return named[c];
  }
  throw new Error(`no resting ${kind} colour in: ${classes}`);
}

describe("bordered controls (3:1 border, 4.5:1 text)", () => {
  const pairs: [string, RGB, RGB, number][] = [];
  for (const [name, theme] of [["light", light], ["dark", dark]] as const) {
    for (const [control, classes] of CONTROLS) {
      const border = rgb(token(theme, used(classes, "border")));
      pairs.push([`${name} ${control} border on the surface`, border, surface(theme), 3]);
      pairs.push([`${name} ${control} border on the paper`, border, paper(theme), 3]);
    }
    for (const variant of ["outline", "secondary", "default"] as const) {
      const classes = buttonVariants({ variant });
      pairs.push([`${name} ${variant} button text on its fill`, rgb(token(theme, used(classes, "text"))), rgb(token(theme, used(classes, "bg"))), 4.5]);
    }
    pairs.push([`${name} text on the accent fill`, rgb(token(theme, "--on-accent")), rgb(token(theme, "--accent")), 4.5]);
  }
  it.each(pairs)("%s", (_name, fg, bg, need) => {
    expect(contrast(fg, bg)).toBeGreaterThanOrEqual(need);
  });

  it("keeps the dark outline and secondary buttons distinguishable from the card", () => {
    for (const variant of ["outline", "secondary"] as const) {
      const border = rgb(token(dark, used(buttonVariants({ variant }), "border")));
      expect(contrast(border, rgb(token(dark, "--card")))).toBeGreaterThanOrEqual(3);
    }
  });
});

describe("library colour marks (3:1 on the surface and the paper)", () => {
  const at = css.indexOf("/* The library colour marks");
  const keys = (sel: string) => {
    const start = css.indexOf(sel + " {", at);
    return [...css.slice(start, css.indexOf("\n}", start)).matchAll(/(--v-[\w-]+):\s*(#[0-9a-f]{6});/gi)].map((m) => [m[1], m[2]] as const);
  };
  const pairs: [string, RGB, RGB][] = [];
  for (const [name, theme, sel] of [["light", light, ":root"], ["dark", dark, ".dark"]] as const) {
    for (const [k, hex] of keys(sel)) {
      pairs.push([`${name} ${k} on the surface`, rgb(hex), surface(theme)]);
      pairs.push([`${name} ${k} on the paper`, rgb(hex), paper(theme)]);
    }
  }
  it("has ten marks in both themes", () => expect(pairs).toHaveLength(40));
  it.each(pairs)("%s", (_name, fg, bg) => expect(contrast(fg, bg)).toBeGreaterThanOrEqual(3));
});
