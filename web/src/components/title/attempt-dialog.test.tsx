// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { AttemptDialog } from "./attempt-dialog";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };

it("says why the attempt failed above its provider attempts, the stored text folded under Technical details", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity/downloads/41/attempts", reply: {
      attempts: [{ provider: "rd", outcome: "accepted", code: "cached", at: "2026-11-09T09:36:00Z" }],
      reason: "Not imported: these files do not improve on the files you have",
      reason_detail: "This release scores 1,202, yours 6,200. Nothing was replaced.",
      error: "library: no file passed the sample guard (Not a Custom Format upgrade for existing file(s). …)",
    } },
  ]);
  renderPage(<AttemptDialog downloadId={41} library="Anime series" words="Last upgrade attempt Nov 9: not imported: these files do not improve on the files you have" onClose={() => {}} />);
  const dialog = await screen.findByTestId("attempt-dialog");
  const reason = await within(dialog).findByTestId("attempt-reason");
  expect(reason.textContent).toContain("Not imported: these files do not improve on the files you have");
  expect(reason.textContent).toContain("This release scores 1,202, yours 6,200. Nothing was replaced.");
  expect(within(reason).getByText("Technical details")).toBeTruthy();
  // The heading keeps when; the reason is said once.
  expect(within(dialog).getByText("Last upgrade attempt Nov 9")).toBeTruthy();
  expect(within(dialog).getByTestId("attempt-list").textContent).toContain("RD");
});
