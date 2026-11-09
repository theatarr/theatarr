// Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/ui/skeleton.tsx @ 2ef7757c, GPL-3.0.
// Changes: quieter, on the sunken well.
import { cn } from "@/lib/utils"

function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-[3px] bg-[var(--sunken)]", className)}
      {...props}
    />
  )
}

export { Skeleton }
