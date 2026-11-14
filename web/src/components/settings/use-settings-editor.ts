// Shared session and write lifecycle for settings editors. Only an explicit reload moves the pinned generation.
// Draft/credential resets and preview invalidation stay with the editor that owns those fields.
import { setupKey } from "@/api/setup";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { confirmationHeaders } from "@/api/settings";
import { qk } from "@/api/query-keys";
import { explainError, type Explained } from "@/lib/settings";
import { useWriteConfirmation } from "./confirm-write";

export function useSettingsEditor<S extends { generation: number }>({ initial, rebase, gone, onReload }: {
  initial: S;
  rebase: (base: S) => Promise<S | undefined>;
  gone: Explained;
  onReload: (next: S, keep: boolean) => boolean | void;
}) {
  const qc = useQueryClient();
  const confirm = useWriteConfirmation();
  const [base, setBase] = useState(initial);
  const [problem, setProblem] = useState<{ explanation: Explained; failure?: unknown }>();
  const setError = (error?: Explained) => setProblem(error ? { explanation: error } : undefined);
  const reportError = (failure: unknown) => setProblem({ explanation: explainError(failure), failure });
  const [kept, setKept] = useState<number>();
  const [busy, setBusy] = useState(false);

  const reload = async (keep: boolean) => {
    try {
      const next = await rebase(base);
      if (!next) { setError(gone); return; }
      setBase(next);
      setError(undefined);
      const keepDraft = onReload(next, keep) ?? keep;
      setKept(keepDraft ? next.generation : undefined);
    } catch (e) { reportError(e); }
  };

  // Capture the request and callbacks once, so confirmation retries exactly the reviewed payload.
  const save = async <T,>(request: { path: string; method: "POST" | "PUT" | "DELETE"; body?: unknown }, saved: (result: T) => void, callbacks?: {
    onError?: (error: unknown) => void;
    onConfirmation?: () => void;
  }) => {
    const send = async (token?: string): Promise<void> => {
      setBusy(true);
      setError(undefined);
      try {
        const result = await api<T>(request.path, { method: request.method, body: request.body, headers: confirmationHeaders(token) });
        void qc.invalidateQueries({ queryKey: setupKey });
        void qc.invalidateQueries({ queryKey: qk.settings.all });
        void qc.invalidateQueries({ queryKey: qk.policy.all });
        void qc.invalidateQueries({ queryKey: qk.sources });
        void qc.invalidateQueries({ queryKey: qk.gateway });
        // A provider account's tag (auth/me providers) follows an added, changed or removed account.
        if (request.path.includes("/providers")) void qc.invalidateQueries({ queryKey: qk.me });
        saved(result);
      } catch (e) {
        if (confirm.ask(e, (next) => void send(next))) callbacks?.onConfirmation?.();
        else { reportError(e); callbacks?.onError?.(e); }
      } finally { setBusy(false); }
    };
    await send();
  };

  return { base, setBase, error: problem?.explanation, setError, reportError, failure: problem?.failure, kept, busy, setBusy, reload, save, confirm };
}
