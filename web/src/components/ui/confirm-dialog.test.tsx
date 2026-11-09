// @vitest-environment jsdom
// An import now that did not import is answered as a warning, not as a success.
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ConfirmDialog } from "./confirm-dialog";
import { retryScope } from "@/lib/ops";

describe("confirm dialog answer", () => {
  it("shows a done answer as success, and as a warning when the action fell short", () => {
    const scope = retryScope("ready", "movie-web");
    const { rerender } = render(<ConfirmDialog scope={scope} onConfirm={() => {}} onClose={() => {}} done="Imported now" />);
    expect(screen.getByTestId("confirm-done").dataset.warning).toBeUndefined();
    rerender(<ConfirmDialog scope={scope} onConfirm={() => {}} onClose={() => {}} done="Not imported: rejected" doneWarning />);
    const done = screen.getByTestId("confirm-done");
    expect(done.dataset.warning).toBe("true");
    expect(done.className).toContain("--th-warning");
  });
});
