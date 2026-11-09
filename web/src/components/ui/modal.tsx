// One accessible modal for every overlay and sheet (the search overlay, the add sheet, the catalog filter sheet, the
// phone navigation). Our own rather than Radix Dialog: Radix's scroll lock (react-remove-scroll) injects a <style>
// element, which the CSP (style-src 'self') refuses. What it does, all without inline styles:
//
// - a portal on <body> with role="dialog" and aria-modal on the panel;
// - the rest of the page inert while it is open (lib/modal-stack.ts), so neither the mouse nor Tab reaches it, and a
//   modal opened from another makes the first inert until it closes;
// - focus moves in on open (initialFocus, else the first field or button, else the panel), Tab and Shift-Tab wrap
//   inside it, and focus returns to whatever had it before on close;
// - a reference-counted scroll lock: the page scrolls again only when the last modal closes;
// - Escape and a click on the backdrop close it.
import { useEffect, useLayoutEffect, useRef, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { lockScroll, pushModal, topModal } from "@/lib/modal-stack";
import { cn } from "@/lib/utils";

const FOCUSABLE =
  'a[href],area[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),iframe,[contenteditable="true"],[tabindex]:not([tabindex="-1"])';

/** The elements Tab can reach inside root, in order (hidden ones left out). */
export function focusables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => !el.closest("[inert]") && (el.offsetParent !== null || el.getClientRects().length > 0),
  );
}

export type ModalProps = {
  /** The dialog's accessible name. */
  label: string;
  onClose: () => void;
  children: ReactNode;
  /** The fixed full-screen layer (position and stacking: z-index, flex placement of the panel). */
  className?: string;
  /** The dimmed layer behind the panel; a click on it closes. */
  backdropClassName?: string;
  backdropSlot?: string;
  /** The dialog itself. */
  panelClassName?: string;
  panelSlot?: string;
  /** Focused on open instead of the first focusable element. */
  initialFocus?: RefObject<HTMLElement | null>;
  testid?: string;
  panelTestid?: string;
  /** For a sheet hidden from a breakpoint up (md:hidden, lg:hidden): the width in px from which it closes, so a
   * window made wider never leaves the page inert behind a sheet no one can see. */
  closeFrom?: number;
  /** Where focus goes on close instead of the element focused before opening (asked at close: a sheet whose record
   * was just deleted names a neighbour, as its opener is gone). Nothing, or a missing element, keeps the default. */
  returnFocus?: () => HTMLElement | null | undefined;
};

export function Modal({
  label,
  onClose,
  children,
  className,
  backdropClassName,
  backdropSlot,
  panelClassName,
  panelSlot,
  initialFocus,
  testid,
  panelTestid,
  closeFrom,
  returnFocus,
}: ModalProps) {
  const root = useRef<HTMLDivElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const opener = useRef<Element | null>(typeof document === "undefined" ? null : document.activeElement);
  const close = useRef(onClose);
  const giveBack = useRef(returnFocus);
  useLayoutEffect(() => {
    close.current = onClose;
    giveBack.current = returnFocus;
  });

  useEffect(() => {
    const el = root.current;
    if (!el) return;
    const back = opener.current;
    const unlock = lockScroll(document.documentElement);
    const pop = pushModal(el, document.body);
    const first = initialFocus?.current ?? focusables(panel.current ?? el)[0] ?? panel.current;
    first?.focus({ preventScroll: true });
    // Focus that lands outside the top modal (a script, a click on a page element that escaped inert) comes back.
    const onFocusIn = (e: FocusEvent) => {
      if (topModal() !== el || !(e.target instanceof Node) || el.contains(e.target)) return;
      (focusables(panel.current ?? el)[0] ?? panel.current)?.focus({ preventScroll: true });
    };
    document.addEventListener("focusin", onFocusIn);
    // Keys go through the document, not the modal's own element: when the focused control unmounts (a sheet that
    // swaps its form for a result) focus falls to <body>, and Escape and Tab must still work.
    const onKeyDown = (e: KeyboardEvent) => {
      if (topModal() !== el || e.defaultPrevented) return;
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        close.current();
        return;
      }
      const box = panel.current;
      if (e.key !== "Tab" || !box) return;
      const list = focusables(box);
      if (list.length === 0) {
        e.preventDefault();
        box.focus();
        return;
      }
      const at = document.activeElement;
      const firstEl = list[0];
      const lastEl = list[list.length - 1];
      const outside = !at || !box.contains(at);
      if (e.shiftKey && (outside || at === firstEl || at === box)) {
        e.preventDefault();
        lastEl.focus();
      } else if (!e.shiftKey && (outside || at === lastEl)) {
        e.preventDefault();
        firstEl.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("focusin", onFocusIn);
      pop();
      unlock();
      const to = giveBack.current?.() ?? back;
      const give = () => to instanceof HTMLElement && to.isConnected && !to.closest("[inert]") && (to.focus({ preventScroll: true }), true);
      // A sheet that closes together with the dialog on top of it (a confirmed delete) unmounts first, while the
      // dialog still holds the page inert: try again once every modal of this commit has gone.
      if (!give()) queueMicrotask(give);
    };
    // initialFocus is read once, on open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!closeFrom || typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(`(min-width: ${closeFrom}px)`);
    if (mq.matches) close.current();
    const onChange = () => mq.matches && close.current();
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [closeFrom]);

  return createPortal(
    <div ref={root} className={cn("fixed inset-0", className)} data-testid={testid}>
      <div
        aria-hidden="true"
        data-slot={backdropSlot}
        className={cn("absolute inset-0", backdropClassName)}
        onMouseDown={(e) => {
          if (e.target === e.currentTarget) close.current();
        }}
      />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        data-slot={panelSlot}
        data-testid={panelTestid}
        className={cn("relative outline-none", panelClassName)}
      >
        {children}
      </div>
    </div>,
    document.body,
  );
}
