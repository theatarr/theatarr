// The bookkeeping behind <Modal> (components/ui/modal.tsx), kept free of React so it can be tested:
//
// - lockScroll: a reference-counted scroll lock. The page stops scrolling while any modal is open and starts again
//   only when the last one closes, so closing an add sheet opened from the search overlay leaves the overlay's lock.
//   It is a class on <html>, not an inline style: the CSP (style-src 'self') refuses the <style> element and the
//   style attribute that Radix's react-remove-scroll writes.
// - pushModal: the stack of open modals. Everything beside the top modal's root, among <body>'s children, gets the
//   inert attribute (no focus, no clicks, hidden from assistive tech); a modal below the top is inert too. Closing
//   the top hands the page back to the next one, or to nobody. Attributes set by others are left alone.

type ClassHolder = { classList: { add(c: string): void; remove(c: string): void } };

export const SCROLL_LOCK_CLASS = "overflow-hidden";
const counts = new WeakMap<ClassHolder, number>();

/** Locks scrolling on root until the returned release is called (once; later calls do nothing). */
export function lockScroll(root: ClassHolder): () => void {
  const n = counts.get(root) ?? 0;
  counts.set(root, n + 1);
  if (n === 0) root.classList.add(SCROLL_LOCK_CLASS);
  let done = false;
  return () => {
    if (done) return;
    done = true;
    const left = (counts.get(root) ?? 1) - 1;
    counts.set(root, left);
    if (left <= 0) root.classList.remove(SCROLL_LOCK_CLASS);
  };
}

/** How many locks root holds now. */
export function scrollLocks(root: ClassHolder): number {
  return counts.get(root) ?? 0;
}

/** The part of an element the stack needs (a DOM Element in the app; a stand-in in tests). */
export type StackNode = {
  hasAttribute(n: string): boolean;
  setAttribute(n: string, v: string): void;
  removeAttribute(n: string): void;
  contains(other: never): boolean;
};
type Host = { children: ArrayLike<StackNode> };

const stack: StackNode[] = [];
const ours = new Set<StackNode>();

function sync(host: Host) {
  for (const el of ours) el.removeAttribute("inert");
  ours.clear();
  const top = stack[stack.length - 1];
  if (!top) return;
  for (const el of Array.from(host.children)) {
    if (el === top || el.contains(top as never) || el.hasAttribute("inert")) continue;
    el.setAttribute("inert", "");
    ours.add(el);
  }
}

/** Puts el (a child of host, normally document.body) on top: everything else under host goes inert. The returned
 * function takes it off again. */
export function pushModal(el: StackNode, host: Host): () => void {
  stack.push(el);
  sync(host);
  let done = false;
  return () => {
    if (done) return;
    done = true;
    const i = stack.lastIndexOf(el);
    if (i >= 0) stack.splice(i, 1);
    sync(host);
  };
}

/** The modal on top, if any. */
export function topModal(): StackNode | undefined {
  return stack[stack.length - 1];
}
