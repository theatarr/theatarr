// The rename window's error line (title-toolbar.tsx): a failed rename first, else a failed preview (a closed library
// gate, a mount that is down, a server without the rename). "" when neither failed.

type Failable = { isError: boolean; error: unknown };

function message(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return "unknown error";
}

export function renameError(preview: Failable, apply: Failable): string {
  if (apply.isError) return `Rename failed: ${message(apply.error)}`;
  if (preview.isError) return `Rename preview failed: ${message(preview.error)}`;
  return "";
}
