import type * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * A plain `<select>`, styled to sit beside the other controls.
 *
 * Deliberately not Radix's Select. That component exists to make a listbox out
 * of divs so it can be styled and animated; it costs roughly twenty kilobytes,
 * needs a portal, and has to reimplement typeahead, scrolling and touch
 * behaviour the platform already has. Nothing on this console asks a select to
 * do anything a native one does not, and the native one is what a phone
 * renders as a proper picker.
 *
 * The cost of that choice is that the open list is the browser's, so it has to
 * be coloured through the two things the browser reads: the options' own
 * colours (set on the elements in index.css) and this control's background,
 * which Chromium copies onto the popup. The popover tokens are used for both
 * so the control and the list it opens are the same surface -- and so a
 * transparent control can never again hand the theme's foreground to a white
 * panel, which is what made these unreadable in the dark theme.
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
