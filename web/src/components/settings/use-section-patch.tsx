import { sectionExplanation } from "./section-draft";
import { useState } from "react";
import { saveSectionPatch, type SectionMutation } from "@/api/section-patch";
import { sectionsQuery } from "@/api/settings";
import { useSettingsEditor } from "./use-settings-editor";
import { ExplainBox } from "./parts";

export function useSectionPatch(key: string, onSaved: () => void) {
  const [reading, setReading] = useState(false);
  const editor = useSettingsEditor({ initial: { generation: 0 }, rebase: async () => ({ generation: (await sectionsQuery.queryFn()).generation }), gone: { kind: "not_found", title: "Section not found", message: "Reload settings." }, onReload: () => true });
  const save = async (mutate: SectionMutation, secrets?: Record<string, string>, clearSecrets?: string[]) => {
    setReading(true);
    try { await saveSectionPatch(key, mutate, (request) => editor.save(request, onSaved), secrets, clearSecrets); }
    catch (error) { editor.reportError(error); }
    finally { setReading(false); }
  };
  return { save, failure: editor.failure, busy: reading || editor.busy || editor.error?.kind === "stale", feedback: <>{editor.error ? <ExplainBox error={sectionExplanation(editor.error)} onReload={() => void editor.reload(true)} /> : null}{editor.confirm.dialog}</> };
}
