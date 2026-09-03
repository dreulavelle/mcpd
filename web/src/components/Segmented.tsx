import { cn } from "@/lib/utils";

/**
 * A row of mutually exclusive options that reads at a glance.
 *
 * Where a native select hides every answer but the chosen one, this shows
 * all three -- None, Read, Write -- with the held one filled in, so a matrix
 * of eight rows can be read down the page without opening anything. It is a
 * radio group to assistive technology and a set of buttons to a pointer.
 *
 * `readOnly` renders the same row with nothing pressable, for a page that
 * describes rather than edits; the unheld options fade so the held one is
 * what the eye lands on.
 */
export function Segmented<T extends string>({
  value, options, onChange, label, disabled, readOnly, size = "sm", className,
}: {
  value: T;
  options: { value: T; label: string; title?: string }[];
  onChange?: (next: T) => void;
  /** Names the group to assistive technology. */
  label: string;
  disabled?: boolean;
  readOnly?: boolean;
  size?: "sm" | "md";
  className?: string;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn(
        "inline-flex items-stretch rounded-md border bg-muted/40 p-0.5",
        readOnly && "border-transparent bg-transparent p-0",
        className,
      )}
    >
      {options.map((o) => {
        const on = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={on}
            title={o.title}
            disabled={disabled || readOnly}
            onClick={() => onChange?.(o.value)}
            className={cn(
              "rounded-[5px] font-medium whitespace-nowrap transition-colors outline-none",
              size === "sm" ? "px-2.5 py-1 text-xs" : "px-3 py-1.5 text-sm",
              "focus-visible:ring-[3px] focus-visible:ring-ring/50",
              on
                ? readOnly
                  ? "bg-primary/10 text-primary"
                  : "bg-background text-foreground shadow-sm ring-1 ring-border"
                : readOnly
                  ? "text-muted-foreground/40"
                  : "text-muted-foreground hover:text-foreground",
              (disabled || readOnly) && "cursor-default",
            )}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
