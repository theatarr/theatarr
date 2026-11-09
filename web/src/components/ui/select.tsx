// A native <select> in the theme's control shape: 32 px, the control
// border, a soft focus ring, and a chevron drawn by the .select-chevron rule in styles/globals.css (gradients, so the
// CSP needs no data: images). Native rather than a Radix Select: the phone gets its own picker and the CSP refuses the
// <style> elements Radix injects.
import * as React from "react";
import { cn } from "@/lib/utils";

export const SELECT_CLASS =
  "select-chevron h-8 w-full min-w-0 appearance-none rounded-[5px] border border-input bg-field py-0 pl-2.5 pr-8 text-[13.5px] text-foreground outline-none transition-[color,box-shadow,border-color] hover:border-[var(--ink-3)] focus-visible:border-[var(--line-focus)] focus-visible:ring-[3px] focus-visible:ring-[var(--accent-soft)] disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-[var(--bad)]";

function Select({ className, ...props }: React.ComponentProps<"select">) {
  return <select data-slot="select" className={cn(SELECT_CLASS, className)} {...props} />;
}

export { Select };
