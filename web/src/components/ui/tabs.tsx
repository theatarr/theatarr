// Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/ui/tabs.tsx @ 2ef7757c, GPL-3.0.
// Changes: underline tabs in ink.

import * as React from "react"
import { Tabs as TabsPrimitive } from "radix-ui"

import { cn } from "@/lib/utils"

function Tabs({
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  return <TabsPrimitive.Root data-slot="tabs" {...props} />
}

function TabsList({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn(
        "inline-flex items-center gap-5 border-b border-[var(--line)]",
        className,
      )}
      {...props}
    />
  )
}

function TabsTrigger({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        "-mb-px inline-flex h-[38px] items-center justify-center gap-1.5 whitespace-nowrap border-b-2 border-transparent text-[13.5px] text-[var(--ink-3)] transition-colors",
        "hover:text-[var(--ink)]",
        "data-[state=active]:border-[var(--ink)] data-[state=active]:font-medium data-[state=active]:text-[var(--ink)]",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]",
        "disabled:pointer-events-none disabled:opacity-50",
        className,
      )}
      {...props}
    />
  )
}

function TabsContent({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn("mt-2 rounded-[5px] outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]", className)}
      {...props}
    />
  )
}

export { Tabs, TabsList, TabsTrigger, TabsContent }
