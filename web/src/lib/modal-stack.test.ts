import { describe, expect, it } from "vitest";
import { lockScroll, pushModal, SCROLL_LOCK_CLASS, scrollLocks, topModal, type StackNode } from "./modal-stack";

function classes() {
  const set = new Set<string>();
  return { set, classList: { add: (c: string) => void set.add(c), remove: (c: string) => void set.delete(c) } };
}

class Node implements StackNode {
  attrs = new Map<string, string>();
  constructor(public name: string, public kids: Node[] = []) {}
  hasAttribute(n: string) {
    return this.attrs.has(n);
  }
  setAttribute(n: string, v: string) {
    this.attrs.set(n, v);
  }
  removeAttribute(n: string) {
    this.attrs.delete(n);
  }
  contains(o: never): boolean {
    const other = o as unknown as Node;
    return other === this || this.kids.some((k) => k.contains(other as never));
  }
}

describe("scroll lock", () => {
  it("unlocks only when the last modal closes (the add sheet over the search overlay)", () => {
    const root = classes();
    const overlay = lockScroll(root);
    const sheet = lockScroll(root);
    expect(scrollLocks(root)).toBe(2);
    sheet();
    expect(root.set.has(SCROLL_LOCK_CLASS)).toBe(true);
    sheet(); // a second release does nothing
    expect(scrollLocks(root)).toBe(1);
    overlay();
    expect(root.set.has(SCROLL_LOCK_CLASS)).toBe(false);
  });
});

describe("modal stack", () => {
  it("makes everything but the top modal inert, and hands back on close", () => {
    const app = new Node("root");
    const foreign = new Node("foreign");
    foreign.setAttribute("inert", "");
    const overlay = new Node("overlay");
    const sheet = new Node("sheet");
    const body = { children: [app, foreign, overlay, sheet] };
    const popOverlay = pushModal(overlay, body);
    expect(app.hasAttribute("inert")).toBe(true);
    expect(overlay.hasAttribute("inert")).toBe(false);
    const popSheet = pushModal(sheet, body);
    expect(topModal()).toBe(sheet);
    expect(overlay.hasAttribute("inert")).toBe(true);
    expect(sheet.hasAttribute("inert")).toBe(false);
    popSheet();
    expect(topModal()).toBe(overlay);
    expect(overlay.hasAttribute("inert")).toBe(false);
    expect(app.hasAttribute("inert")).toBe(true);
    popOverlay();
    expect(topModal()).toBeUndefined();
    expect(app.hasAttribute("inert")).toBe(false);
    // Someone else's inert stays.
    expect(foreign.hasAttribute("inert")).toBe(true);
  });
});
