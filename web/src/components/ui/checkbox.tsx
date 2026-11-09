// A native checkbox in the theme's size and accent (the box itself is the browser's, coloured with accent-color, so
// keyboard, forms and indeterminate keep working). Pass indeterminate to show a partial selection.
import * as React from "react";
import { cn } from "@/lib/utils";

type CheckboxProps = Omit<React.ComponentProps<"input">, "type"> & { indeterminate?: boolean };

function Checkbox({ className, indeterminate = false, ref, ...props }: CheckboxProps) {
  const own = React.useRef<HTMLInputElement>(null);
  React.useImperativeHandle(ref, () => own.current as HTMLInputElement);
  React.useEffect(() => {
    if (own.current) own.current.indeterminate = indeterminate;
  }, [indeterminate]);
  return (
    <input
      ref={own}
      type="checkbox"
      data-slot="checkbox"
      className={cn(
        "size-4 shrink-0 cursor-pointer accent-[var(--accent)] outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)] disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export { Checkbox };
