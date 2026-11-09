// The side sheet every record editor opens in (docs/design/settings.md, "Editors"): a Modal variant that slides in from
// the right on desktop and fills the screen on a phone. Modal does the portal, the inert page, the focus trap, Escape
// and focus return; the sheet adds the layout: a header with the focusable heading, a scrolling body and a footer
// for Save, Cancel and Delete. Confirmations open as dialogs on top of it (ConfirmDialog stacks above, z-[85]).
import { X } from "lucide-react";
import { useEffect, useId, useLayoutEffect, useRef, type ReactNode, type RefObject } from "react";
import { useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";

export function Sheet({
  label,
  onClose,
  children,
  initialFocus,
  returnFocus,
  testid = "sheet",
}: {
  label: string;
  onClose: () => void;
  children: ReactNode;
  /** Focused on open: the sheet's heading (tabIndex -1), so a screen reader starts at its name. */
  initialFocus?: RefObject<HTMLElement | null>;
  /** Where focus goes on close instead of the opener (the opener is gone after a delete). */
  returnFocus?: () => HTMLElement | null | undefined;
  testid?: string;
}) {
  return (
    <Modal
      label={label}
      onClose={onClose}
      initialFocus={initialFocus}
      returnFocus={returnFocus}
      testid={testid}
      className="z-[80] flex justify-end"
      backdropClassName="bg-[var(--scrim)] animate-in fade-in-0"
      panelSlot="sheet-content"
      panelClassName="flex h-dvh w-full flex-col border-l border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in slide-in-from-right-8 sm:max-w-[640px]"
    >
      {children}
    </Modal>
  );
}

/** The sheet's parts: header (heading, a line under it, Close), the scrolling body and the footer with the actions. */
export function SheetLayout({
  title,
  subtitle,
  headingRef,
  headingId,
  onClose,
  footer,
  children,
  testid,
  ...rest
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  headingRef?: RefObject<HTMLHeadingElement | null>;
  headingId?: string;
  onClose: () => void;
  footer?: ReactNode;
  children: ReactNode;
  testid?: string;
} & Record<`data-${string}`, string | undefined>) {
  const own = useId();
  const id = headingId ?? own;
  return (
    <section className="flex min-h-0 flex-1 flex-col" aria-labelledby={id} data-testid={testid} {...rest}>
      <header className="flex items-start justify-between gap-3 border-b border-[var(--line)] px-4 py-3.5 sm:px-5">
        <div className="min-w-0">
          <h2 ref={headingRef} id={id} tabIndex={-1} className="rounded-sm font-heading text-[22px] font-medium leading-tight text-[var(--ink)] outline-none [overflow-wrap:anywhere] focus-visible:ring-2 focus-visible:ring-[var(--line-focus)]" data-testid={testid ? `${testid}-heading` : undefined}>
            {title}
          </h2>
          {subtitle ? <div className="mt-0.5 text-[12.5px] text-muted-foreground [overflow-wrap:anywhere]">{subtitle}</div> : null}
        </div>
        <Button type="button" size="icon-sm" variant="ghost" onClick={onClose} aria-label="Close" data-testid="sheet-close">
          <X />
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-5">{children}</div>
      {footer ? (
        <footer className="flex flex-wrap items-center gap-2 border-t border-[var(--line)] bg-[var(--bg)] px-4 py-3 sm:px-5" data-testid="sheet-footer">
          {footer}
        </footer>
      ) : null}
    </section>
  );
}

/** The record a page's sheet is editing, kept in the URL as ?edit=<key> ("new" for a record being added), so a reload
 * opens the same sheet again. open is called with the key once ready (the records are loaded) when the URL names one
 * and no sheet is open; the returned setter writes the key (null on close). Written with replace: opening and closing
 * a sheet adds no history entries. */
export function useEditParam(ready: boolean, isOpen: boolean, open: (key: string) => void): (key: string | null) => void {
  const [params, setParams] = useSearchParams();
  const param = params.get("edit");
  const latest = useRef({ isOpen, open });
  useLayoutEffect(() => {
    latest.current = { isOpen, open };
  });
  // Only a change of the key (or the records arriving) opens a sheet, so closing one never reopens it.
  useEffect(() => {
    if (param && ready && !latest.current.isOpen) latest.current.open(param);
  }, [param, ready]);
  return (key: string | null) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (key === null) next.delete("edit");
        else next.set("edit", key);
        return next;
      },
      { replace: true },
    );
}
