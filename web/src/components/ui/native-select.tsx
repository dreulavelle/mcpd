import type * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * A plain `<select>`, styled to sit beside the other controls.
 *
 * `bg-popover` is load-bearing, not decoration: Chromium copies the control's
 * background onto the popup it opens, and a transparent one leaves the theme's
 * foreground on the engine's white panel. The other half of the pair is the
 * `option, optgroup` rule in index.css.
 */
function NativeSelect({ className, children, ...props }: React.ComponentProps<"select">) {
  return (
    <div className="relative">
      <select
        data-slot="native-select"
        className={cn(
          "h-9 w-full appearance-none rounded-md border border-input",
          "bg-popover text-popover-foreground",
          "py-1 pr-8 pl-3 text-sm shadow-xs transition-colors",
          "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
          "disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDown
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 right-2.5 size-4 -translate-y-1/2 opacity-60"
      />
    </div>
  );
}

export { NativeSelect };
