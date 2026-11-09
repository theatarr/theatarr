// Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/ui/button.tsx @ 2ef7757c, GPL-3.0.
// Changes: the theme's buttons: default and primary filled with the accent,
// outline on the sheet with the control border (3:1 against the card), danger on --bad-solid, quiet; 32 px, sm 28.
import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-[5px] text-[13px] font-medium transition-colors disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4 [&_svg]:shrink-0 outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)] aria-invalid:border-[var(--bad)]",
  {
    variants: {
      variant: {
        default:
          "border border-[var(--accent)] bg-[var(--accent)] text-[var(--on-accent)] hover:border-[var(--accent-hover)] hover:bg-[var(--accent-hover)]",
        primary:
          "border border-[var(--accent)] bg-[var(--accent)] text-[var(--on-accent)] hover:border-[var(--accent-hover)] hover:bg-[var(--accent-hover)]",
        destructive:
          "border border-[var(--bad-solid)] bg-[var(--bad-solid)] text-[var(--on-solid)] hover:bg-[var(--th-danger-solid-hover)]",
        danger:
          "border border-[var(--bad-solid)] bg-[var(--bad-solid)] text-[var(--on-solid)] hover:bg-[var(--th-danger-solid-hover)]",
        warning:
          "border border-[var(--th-warning-border)] bg-[var(--warn-soft)] text-[var(--warn-ink)] hover:border-[var(--th-warning-border-strong)]",
        success:
          "border border-[var(--th-success-border)] bg-[var(--ok-soft)] text-[var(--ok-ink)] hover:border-[var(--th-success-border-strong)]",
        outline:
          "border border-[var(--control)] bg-[var(--surface)] text-[var(--ink)] hover:bg-[var(--hover)]",
        secondary:
          "border border-[var(--control)] bg-[var(--sunken)] text-[var(--ink)] hover:bg-[var(--hover)]",
        ghost:
          "text-[var(--ink-2)] hover:bg-[var(--hover)] hover:text-[var(--ink)]",
        quiet:
          "text-[var(--ink-2)] hover:bg-[var(--hover)] hover:text-[var(--ink)]",
        link: "text-[var(--accent-ink)] underline-offset-2 hover:underline",
      },
      size: {
        default: "h-8 px-3 has-[>svg]:px-2.5",
        xs: "h-6 gap-1 rounded-[3px] px-2 text-xs has-[>svg]:px-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-7 gap-1.5 px-2.5 text-[12.5px] has-[>svg]:px-2 [&_svg:not([class*='size-'])]:size-3.5",
        lg: "h-9 px-4 has-[>svg]:px-3",
        icon: "size-8",
        "icon-xs": "size-6 rounded-[3px] [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-7",
        "icon-lg": "size-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean
  }) {
  const Comp = asChild ? Slot.Root : "button"

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
