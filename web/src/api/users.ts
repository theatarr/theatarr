// Users, the profile and API keys (docs/design/users.md): GET/PATCH users, users/settings, DELETE users/{id}/sessions,
// PATCH me, POST me/password, and keys per account.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { qk } from "./query-keys";

export type User = Schemas["User"];
export type UsersPage = Schemas["UsersPage"];
export type UserPatch = Schemas["UserPatch"];
export type UserSettings = Schemas["UserSettings"];
export type Key = Schemas["Key"];

export const useUsers = () => useQuery({ queryKey: ["users"], queryFn: () => api<UsersPage>("/users") });

export function usePatchUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: UserPatch }) => api<User>(`/users/${id}`, { method: "PATCH", body: patch }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["users"] });
      void qc.invalidateQueries({ queryKey: qk.me });
    },
  });
}

export function useEndSessions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api<{ ended: number }>(`/users/${id}/sessions`, { method: "DELETE" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["users"] }),
  });
}

export function useSetUserSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: { plex_new_users: "pending" | "active" }) => api<UserSettings>("/users/settings", { method: "PUT", body: s }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["users"] }),
  });
}

/** Keys: your own, or (users permission) one account's or everyone's ("all"). */
export const useKeysOf = (user?: number | "all", enabled = true) =>
  useQuery({
    queryKey: ["keys", user ?? "mine"],
    queryFn: () => api<{ items: Key[] }>("/keys" + qs({ user })),
    enabled,
  });

export function useCreateKeyFor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, user_id }: { name: string; user_id?: number }) =>
      api<{ key: string; info: Key }>("/keys", { method: "POST", body: user_id ? { name, user_id } : { name } }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["keys"] });
      void qc.invalidateQueries({ queryKey: ["users"] });
    },
  });
}

export function useRevokeKeyAny() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api<void>(`/keys/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["keys"] });
      void qc.invalidateQueries({ queryKey: ["users"] });
    },
  });
}

export function usePatchMe() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (p: { display_name: string }) => api<User>("/me", { method: "PATCH", body: p }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.me }),
  });
}

export function useChangePassword() {
  return useMutation({
    mutationFn: (p: { current: string; new: string }) => api<void>("/me/password", { method: "POST", body: p }),
  });
}
