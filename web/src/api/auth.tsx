// Who is signed in. The session cookie is HttpOnly; the SPA learns the user and the CSRF token from GET /auth/me,
// and gets a fresh token from POST /auth/login or a finished Plex sign-in.
//
// Plex sign-in (docs/design/users.md) follows Seerr's PlexOAuth (github.com/seerr-team/seerr src/utils/plex.ts @ c167958,
// MIT): a popup opened at once on the click (so no popup blocker stops it), pointed at app.plex.tv once the server has
// made the pin, and a poll once a second until the person confirms. The pin, the poll to plex.tv and the account
// check all run on the server; the browser never sees a Plex token.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, setCSRF } from "./client";
import { useMe } from "./hooks";
import { qk } from "./query-keys";

export function useAuth() {
  const me = useMe();
  if (me.data?.csrf_token) setCSRF(me.data.csrf_token);
  return me;
}

export function useLogin() {
  const qc = useQueryClient();
  return async (username: string, password: string) => {
    const r = await api<{ username: string; csrf_token: string }>("/auth/login", {
      method: "POST",
      body: { username, password },
      redirectOn401: false,
    });
    setCSRF(r.csrf_token);
    await qc.invalidateQueries({ queryKey: qk.me });
  };
}

export function useLogout() {
  const qc = useQueryClient();
  return async () => {
    try {
      await api<void>("/auth/logout", { method: "POST", redirectOn401: false });
    } finally {
      setCSRF("");
      qc.clear();
      window.location.assign("/login");
    }
  };
}

/** Whether Plex sign-in is set up on the server. */
export function usePlexEnabled() {
  return useQuery({
    queryKey: ["auth", "plex"],
    queryFn: () => api<{ enabled: boolean }>("/auth/plex", { redirectOn401: false }),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

type Started = { id: string; auth_url: string; expires_at: string };
type Poll = { status: string; error?: string; username?: string; csrf_token?: string };

/** The outcome of a Plex sign-in that did not end in a session. */
export class PlexSignInError extends Error {
  status: string;
  constructor(status: string, message: string) {
    super(message);
    this.status = status;
  }
}

function openPopup(): Window | null {
  const w = 600;
  const h = 700;
  const left = window.screenX + (window.innerWidth - w) / 2;
  const top = window.screenY + (window.innerHeight - h) / 2;
  return window.open("/login/plex/loading", "plex-auth", `scrollbars=yes,width=${w},height=${h},top=${top},left=${left}`);
}

const sleep = (ms: number) => new Promise((r) => window.setTimeout(r, ms));

export function usePlexLogin() {
  const qc = useQueryClient();
  return async (signal?: AbortSignal) => {
    const popup = openPopup();
    if (!popup) throw new PlexSignInError("popup", "The browser blocked the Plex window. Allow pop-ups for this site and try again.");
    try {
      const st = await api<Started>("/auth/plex/start", { method: "POST", body: {}, redirectOn401: false });
      popup.location.href = st.auth_url;
      const deadline = Math.min(Date.parse(st.expires_at) || Infinity, Date.now() + 15 * 60_000);
      while (Date.now() < deadline) {
        if (signal?.aborted) throw new PlexSignInError("aborted", "Sign-in canceled.");
        await sleep(1000);
        let p: Poll;
        try {
          p = await api<Poll>("/auth/plex/poll", { method: "POST", body: { id: st.id }, redirectOn401: false });
        } catch (e) {
          if (e instanceof ApiError) {
            const status = e.status === 410 ? "expired" : e.status === 403 ? (/disabled/i.test(e.message) ? "disabled" : "denied") : "error";
            throw new PlexSignInError(status, e.message);
          }
          throw e;
        }
        if (p.status === "waiting") continue;
        if (p.status === "signed_in") {
          setCSRF(p.csrf_token);
          await qc.invalidateQueries({ queryKey: qk.me });
          return p;
        }
        throw new PlexSignInError(p.status, p.error ?? p.status);
      }
      throw new PlexSignInError("expired", "The Plex sign-in expired. Start again.");
    } finally {
      try {
        popup.close();
      } catch {
        // already gone
      }
    }
  };
}
