// The title page's rename button. "Names match" is said only after a preview succeeded and found nothing to rename:
// while the preview loads, or when it failed (a closed library gate, a mount that is down, a server without the
// rename), the button says so instead of claiming the names match.

export type RenamePreviewLike = { files: unknown[]; skipped: number };

type QueryLike = { isPending: boolean; isError: boolean; error: unknown; data: RenamePreviewLike | undefined };
type MutationLike = { isPending: boolean; isError: boolean; error: unknown };

export type RenameButton = {
  label: string;
  disabled: boolean;
  title: string;
  /** An error to show next to the button; "" when there is none. */
  error: string;
};

function message(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return "unknown error";
}

export function renameButton(preview: QueryLike, apply: MutationLike): RenameButton {
  if (apply.isPending) return { label: "Renaming…", disabled: true, title: "Moving the links", error: "" };
  const applyError = apply.isError ? `Rename failed: ${message(apply.error)}` : "";
  if (preview.isError) {
    const err = `Rename preview failed: ${message(preview.error)}`;
    return { label: "Rename unavailable", disabled: true, title: err, error: applyError || err };
  }
  if (preview.isPending || !preview.data) {
    return { label: "Checking names…", disabled: true, title: "Loading the rename preview", error: applyError };
  }
  const n = preview.data.files.length;
  const skipped = preview.data.skipped ? `${preview.data.skipped} ${preview.data.skipped === 1 ? "link" : "links"} skipped` : "";
  if (n > 0) {
    return { label: `Rename (${n})`, disabled: false, title: skipped || "Rename the links to the current naming", error: applyError };
  }
  return { label: "Names match", disabled: true, title: skipped || "Every link already has the current name", error: applyError };
}
