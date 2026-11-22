// Settings opens each page at its top (the window scrolls): one reset for every Settings and Advanced route, mounted
// once in the shell. It runs only when the pathname changes; an address with a #hash (a link to a panel), Back and
// Forward, and a change on the same page (an editor's ?edit=) keep the scroll.
import { useLayoutEffect, useRef } from "react";
import { useLocation, useNavigationType } from "react-router";

/** Whether a path is one of Settings' or Advanced's pages. */
export function settingsPath(pathname: string): boolean {
  return /^\/admin\/(settings|advanced)(\/|$)/.test(pathname);
}

export function SettingsScrollReset() {
  const { pathname, hash } = useLocation();
  const navigation = useNavigationType();
  const shown = useRef(pathname);
  useLayoutEffect(() => {
    if (shown.current === pathname) return;
    shown.current = pathname;
    if (navigation !== "POP" && !hash && settingsPath(pathname)) window.scrollTo(0, 0);
  }, [pathname, hash, navigation]);
  return null;
}
