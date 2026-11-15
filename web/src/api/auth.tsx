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

/** GET auth/first-account: whether the install has no account yet (the sign-in page then offers to create the admin),
 * and whether an older version's generated admin password file is still unused. */
export type FirstAccountStatus = { needs_account: boolean; password_pending: boolean };

export function useFirstAccount() {
  return useQuery({
    queryKey: ["auth", "first-account"],
    queryFn: () => api<FirstAccountStatus>("/auth/first-account", { redirectOn401: false }),
    retry: false,
  });
}

/** Creates the first account, an admin, and signs it in (POST auth/first-account; only while no account exists). */
export function useCreateFirstAdmin() {
  const qc = useQueryClient();
  return async (username: string, password: string) => {
    const r = await api<{ username: string; csrf_token: string }>("/auth/first-account", {
      method: "POST",
      body: { username, password },
      redirectOn401: false,
    });
    setCSRF(r.csrf_token);
    await qc.invalidateQueries({ queryKey: qk.me });
    // Not awaited: the page moves on as signed in, without showing the sign-in form in between.
    void qc.invalidateQueries({ queryKey: ["auth", "first-account"] });
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

/** A Plex sign-in; with invite (an invite link's token) the confirmed account redeems the invite instead. */
export function usePlexLogin() {
  const qc = useQueryClient();
  return async (signal?: AbortSignal, invite?: string) => {
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
          p = await api<Poll>("/auth/plex/poll", { method: "POST", body: invite ? { id: st.id, invite } : { id: st.id }, redirectOn401: false });
        } catch (e) {
          if (e instanceof ApiError) {
            const status = e.status === 409 ? "taken" : e.status === 410 ? "expired" : e.status === 403 ? (/disabled/i.test(e.message) ? "disabled" : "denied") : "error";
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

/** What an invite link's page shows (POST auth/invite): the name the admin gave, the expiry, whether Plex is offered.
 * The token is read from the link's fragment and sent in the body only. */
export type InviteInfo = { name: string; expires_at: string; plex: boolean };

export function useInvite(token: string) {
  return useQuery({
    queryKey: ["auth", "invite", token],
    queryFn: () => api<InviteInfo>("/auth/invite", { method: "POST", body: { token }, redirectOn401: false }),
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
  });
}

/** Redeems an invite with a chosen username and password and signs in (POST auth/invite/accept). */
export function useAcceptInvite() {
  const qc = useQueryClient();
  return async (token: string, username: string, password: string) => {
    const r = await api<{ username: string; csrf_token: string }>("/auth/invite/accept", {
      method: "POST",
      body: { token, username, password },
      redirectOn401: false,
    });
    setCSRF(r.csrf_token);
    await qc.invalidateQueries({ queryKey: qk.me });
  };
}
