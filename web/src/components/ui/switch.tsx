// The on/off switch: a button with role="switch" (no Radix: nothing to inject, and a form value is not needed
// by any caller). The track is the accent when on and the control border when off; Space and Enter toggle it as on
// any button. size "sm" is the dense rows' switch (16 px); aria-disabled looks like disabled but keeps the title
// tooltip and focus, for a switch whose reason is worth reading.
import * as React from "react";
import { cn } from "@/lib/utils";

type SwitchProps = Omit<React.ComponentProps<"button">, "onChange" | "value"> & {
  checked: boolean;
  onCheckedChange?: (checked: boolean) => void;
  size?: "default" | "sm";
};

function Switch({ checked, onCheckedChange, className, disabled, onClick, size = "default", ...props }: SwitchProps) {
  const sm = size === "sm";
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      data-slot="switch"
      data-state={checked ? "checked" : "unchecked"}
      disabled={disabled}
      onClick={(e) => {
        onClick?.(e);
        if (!e.defaultPrevented) onCheckedChange?.(!checked);
      }}
      className={cn(
        "relative inline-flex shrink-0 items-center rounded-full border transition-colors outline-none",
        sm ? "h-4 w-7" : "h-5 w-[34px]",
        "focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)] disabled:cursor-not-allowed disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50",
        checked ? "border-[var(--accent)] bg-[var(--accent)]" : "border-[var(--control)] bg-[var(--sunken)]",
        className,
      )}
      {...props}
    >
      <span
        aria-hidden
        className={cn(
          "pointer-events-none block rounded-full shadow-[var(--shadow-1)] transition-transform",
          sm ? "size-2.5" : "size-3.5",
          checked ? cn(sm ? "translate-x-[13px]" : "translate-x-[15px]", "bg-[var(--on-accent)]") : "translate-x-[2px] bg-[var(--surface)]",
        )}
      />
    </button>
  );
}

export { Switch };
