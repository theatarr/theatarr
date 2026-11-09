// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { useSetup } from "./setup";
import { useSettingsWrite } from "./settings";
import { useSettingsEditor } from "@/components/settings/use-settings-editor";

function Save({ shared }: { shared: boolean }) {
 const setup = useSetup();
 const write = useSettingsWrite();
 const editor = useSettingsEditor({ initial: { generation: 1 }, rebase: async () => undefined, gone: { kind: "validation", title: "gone", message: "" }, onReload: () => {} });
 return <><output>{setup.data?.missing.join(",") || "ready"}</output><button onClick={() => shared ? void editor.save({ method: "PUT", path: "/settings/providers/1", body: {} }, () => {}) : write.mutate({ method: "PUT", path: "/providers/1", body: {} })}>Save provider</button></>;
}
it.each([true, false])("saving a provider refreshes Setup (shared editor %s)", async shared => {
 let saved = false;
 const fetch = mockFetch([
  { method: "GET", path: "/setup", reply: () => ({ first_run: false, missing: saved ? [] : ["provider"] }) },
  { method: "PUT", path: "/settings/providers/1", reply: () => { saved = true; return {}; } },
 ]);
 renderPage(<Save shared={shared} />);
 await screen.findByText("provider");
 fireEvent.click(screen.getByRole("button", { name: "Save provider" }));
 await waitFor(() => expect(screen.getByRole("status").textContent).toBe("ready"));
 expect(fetch.calls("GET", "/setup")).toHaveLength(2);
});
