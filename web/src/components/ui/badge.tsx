// Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/ui/badge.tsx @ 2ef7757c, GPL-3.0.
// Changes: a square chip: 22 px, 3 px radius, a dot before the
// words for the state tones; ok, warn, bad and ghost as A's names beside the old tones. Tag is A's outlined mono tag
// for facts (qualities, providers, library keys).
import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

const DOT = "before:size-1.5 before:shrink-0 before:rounded-full before:content-[''] has-[>svg]:before:hidden"

const badgeVariants = cva(
  "inline-flex h-[22px] w-fit shrink-0 items-center justify-center gap-1.5 overflow-hidden whitespace-nowrap rounded-[3px] px-[7px] text-xs font-medium leading-none transition-colors [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      tone: {
        neutral: "bg-muted text-muted-foreground",
        positive: `bg-[var(--ok-soft)] text-[var(--ok-ink)] ${DOT} before:bg-[var(--ok)]`,
        ok: `bg-[var(--ok-soft)] text-[var(--ok-ink)] ${DOT} before:bg-[var(--ok)]`,
        warning: `bg-[var(--warn-soft)] text-[var(--warn-ink)] ${DOT} before:bg-[var(--warn)]`,
        warn: `bg-[var(--warn-soft)] text-[var(--warn-ink)] ${DOT} before:bg-[var(--warn)]`,
        negative: `bg-[var(--bad-soft)] text-[var(--bad-ink)] ${DOT} before:bg-[var(--bad)]`,
        bad: `bg-[var(--bad-soft)] text-[var(--bad-ink)] ${DOT} before:bg-[var(--bad)]`,
        info: `bg-[var(--accent-soft)] text-[var(--accent-ink)] ${DOT} before:bg-[var(--accent)]`,
        accent: "bg-[var(--accent-soft)] text-[var(--accent-ink)]",
        outline: "text-[var(--ink-2)] shadow-[inset_0_0_0_1px_var(--line-2)]",
        ghost: "text-[var(--ink-2)] shadow-[inset_0_0_0_1px_var(--line-2)]",
      },
    },
    defaultVariants: {
      tone: "neutral",
    },
  }
)

function Badge({
  className,
  tone,
  asChild = false,
  ...props
}: React.ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & {
    asChild?: boolean
  }) {
  const Comp = asChild ? Slot.Root : "span"

  return (
    <Comp
      data-slot="badge"
      data-tone={tone ?? "neutral"}
      className={cn(badgeVariants({ tone }), className)}
      {...props}
    />
  )
}

/** Another name for the badge: a square status chip. */
const Chip = Badge

/** An outlined mono tag for a fact: a quality, a provider, a library key. */
function Tag({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      data-slot="tag"
      className={cn(
        "inline-flex h-5 items-center whitespace-nowrap rounded-[3px] px-1.5 font-mono text-[11.5px] font-medium leading-none text-[var(--ink-2)] shadow-[inset_0_0_0_1px_var(--line-2)]",
        className,
      )}
      {...props}
    />
  )
}

export { Badge, Chip, Tag, badgeVariants }
