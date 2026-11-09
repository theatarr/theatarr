// Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/ui/table.tsx @ 2ef7757c, GPL-3.0.
// Changes: the theme's tables: 40 px rows (32 dense), hairlines, a sticky small-caps header under the top bar,
// tabular numbers. The header sticks only when the wrapper does not scroll sideways (overflow "clip" or "visible"):
// a scrolling wrapper is its own scroll box, where a sticky offset would push the header down.
import * as React from "react";
import { cn } from "@/lib/utils";

type TableOverflow = "auto" | "clip" | "visible";
type TableLayout = "auto" | "fixed";
type TableDensity = "default" | "dense";

type TableProps = React.TableHTMLAttributes<HTMLTableElement> & {
  wrapperClassName?: string;
  overflow?: TableOverflow;
  layout?: TableLayout;
  density?: TableDensity;
};

function Table({
  className,
  wrapperClassName,
  overflow = "auto",
  layout = "auto",
  density = "default",
  ...props
}: TableProps) {
  const overflowClass =
    overflow === "clip"
      ? "overflow-x-clip"
      : overflow === "visible"
        ? "overflow-visible"
        : "overflow-x-auto";

  return (
    <div
      data-slot="table-wrapper"
      className={cn("relative w-full", overflowClass, wrapperClassName)}
    >
      <table
        data-slot="table"
        data-density={density}
        data-layout={layout}
        data-sticky={overflow === "auto" ? undefined : "true"}
        className={cn(
          "group/table w-full caption-bottom tabular-nums",
          density === "dense" ? "text-[13px]" : "text-[13.5px]",
          layout === "fixed" && "table-fixed",
          className,
        )}
        {...props}
      />
    </div>
  );
}

function TableHeader({ className, ...props }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead data-slot="table-header" className={cn("[&_tr]:border-b", className)} {...props} />;
}

function TableBody({ className, ...props }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody data-slot="table-body" className={cn("[&_tr:last-child]:border-0", className)} {...props} />;
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  return <tr data-slot="table-row" className={cn("border-b border-[var(--line)] transition-colors", className)} {...props} />;
}

function TableHead({ className, ...props }: React.ThHTMLAttributes<HTMLTableHeaderCellElement>) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "group-data-[sticky=true]/table:sticky group-data-[sticky=true]/table:top-[var(--top)] group-data-[sticky=true]/table:z-[1] h-8 px-3 text-left align-middle text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--ink-3)] bg-[var(--bg)] border-b border-[var(--line)]",
        className,
      )}
      {...props}
    />
  );
}

function TableCell({ className, ...props }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td data-slot="table-cell" className={cn("h-10 px-3 py-1.5 align-middle group-data-[density=dense]/table:h-8 group-data-[density=dense]/table:py-1", className)} {...props} />;
}

function TableCheckboxHead({
  className,
  ...props
}: React.ThHTMLAttributes<HTMLTableHeaderCellElement>) {
  return (
    <TableHead
      data-slot="table-checkbox-head"
      className={cn("w-12 px-2 text-center", className)}
      {...props}
    />
  );
}

function TableCheckboxCell({
  className,
  ...props
}: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <TableCell
      data-slot="table-checkbox-cell"
      className={cn("w-12 px-2 text-center", className)}
      {...props}
    />
  );
}

function TableActionsHead({
  className,
  ...props
}: React.ThHTMLAttributes<HTMLTableHeaderCellElement>) {
  return (
    <TableHead
      data-slot="table-actions-head"
      className={cn("w-32 px-3 text-center", className)}
      {...props}
    />
  );
}

function TableActionsCell({
  className,
  ...props
}: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <TableCell
      data-slot="table-actions-cell"
      className={cn("w-32 px-3 text-center", className)}
      {...props}
    />
  );
}

function TableCodeCell({
  className,
  ...props
}: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <TableCell
      data-slot="table-code-cell"
      className={cn(
        "whitespace-nowrap font-mono tabular-nums",
        className,
      )}
      {...props}
    />
  );
}

export {
  Table,
  TableActionsCell,
  TableActionsHead,
  TableBody,
  TableCell,
  TableCheckboxCell,
  TableCheckboxHead,
  TableCodeCell,
  TableHead,
  TableHeader,
  TableRow,
};
