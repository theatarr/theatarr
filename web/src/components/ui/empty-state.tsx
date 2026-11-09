// The empty state: a quiet round glyph, a line saying what is missing and, when there is one, the next step.
// page.tsx's Empty is this without a title or action.
import type { ComponentType, ReactNode } from "react";
import { Inbox } from "lucide-react";
import { cn } from "@/lib/utils";

type EmptyStateProps = {
  /** The short headline; leave it out for a one-line note. */
  title?: ReactNode;
  children?: ReactNode;
  icon?: ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  /** The next step: a button or link. */
  action?: ReactNode;
  className?: string;
};

function EmptyState({ title, children, icon: Icon = Inbox, action, className }: EmptyStateProps) {
  return (
    <div data-slot="empty-state" className={cn("flex flex-col items-center gap-2 px-4 py-10 text-center text-[13px] text-[var(--ink-3)]", className)}>
      <span className="mb-1 grid size-10 place-items-center rounded-full bg-[var(--sunken)] text-[var(--ink-3)]">
        <Icon className="size-5" aria-hidden />
      </span>
      {title ? <div className="text-[15px] font-semibold text-[var(--ink)]">{title}</div> : null}
      {children}
      {action ? <div className="mt-2">{action}</div> : null}
    </div>
  );
}

export { EmptyState };
