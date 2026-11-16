// A screen that fails to load or render. The admin screens load on first use (lazyPage in router.tsx) from files
// named by their content hash, so a tab opened before an update asks for files that are gone: it reloads once,
// which fetches the current build, and shows the error screen below only when that did not help.
import { isRouteErrorResponse, useRouteError } from "react-router";
import { PRODUCT_NAME } from "@/brand";
import { PageHeader } from "@/components/page";
import { Button } from "@/components/ui/button";

/** A screen's code could not be fetched. */
export class PageLoadError extends Error {}

/** Swapped in tests: jsdom's location cannot be stubbed. */
export const browser = { reload: () => window.location.reload() };

/** sessionStorage: the address last reloaded for a failed page load and when, so a server that is down does not loop. */
export const RELOAD_GUARD = "theatarr.page-reload";
const GUARD_MS = 10_000;

/** One reload per address within 10 seconds; none when sessionStorage cannot be used. */
function mayReload(to: string): boolean {
  try {
    const last = JSON.parse(sessionStorage.getItem(RELOAD_GUARD) ?? "null") as { to?: string; at?: number } | null;
    if (last?.to === to && Date.now() - (last.at ?? 0) < GUARD_MS) return false;
    sessionStorage.setItem(RELOAD_GUARD, JSON.stringify({ to, at: Date.now() }));
    return true;
  } catch {
    return false;
  }
}

/**
 * Loads a screen's code. When that fails the router has already put the new address in the address bar, so a
 * reload lands on the page that was asked for, with the current build; the promise then never settles and the
 * loading line stays up until the page goes. A second failure for the same address is a PageLoadError.
 */
export async function loadPage<T>(load: () => Promise<T>): Promise<T> {
  try {
    return await load();
  } catch (err) {
    const { pathname, search, hash } = window.location;
    if (mayReload(pathname + search + hash)) {
      browser.reload();
      return new Promise<T>(() => {});
    }
    throw new PageLoadError(err instanceof Error ? err.message : String(err), { cause: err });
  }
}

/** The routes' errorElement: works with or without the signed-in shell around it. */
export function RouteError() {
  const error = useRouteError();
  const detail =
    error instanceof PageLoadError
      ? `${PRODUCT_NAME} may have been updated or is restarting.`
      : isRouteErrorResponse(error)
        ? `${error.status} ${error.statusText}`.trim()
        : error instanceof Error
          ? error.message
          : String(error);
  return (
    <div className="min-h-dvh bg-[var(--bg)] px-4 py-10 sm:px-8" data-testid="route-error">
      <div className="mx-auto max-w-[640px] [overflow-wrap:anywhere]">
        <PageHeader title="This page could not be loaded." description={detail} />
        <Button onClick={() => browser.reload()}>Reload</Button>
      </div>
    </div>
  );
}
