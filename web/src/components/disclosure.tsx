import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * Something the page keeps but does not lead with.
 *
 * The copy rule is that an error code, a route, an id or a log line goes under
 * "Technical details" rather than into a sentence. This is that block: a
 * native `<details>`, so it is keyboard-operable, findable by the browser's
 * own find-in-page once open, and costs no dependency.
 *
 * Closed by default on purpose. It holds the evidence for a sentence stated
 * above it, and a reader who does not need the evidence should not have to
 * read past it.
 */
export function Disclosure({ summary, children, className }: {
  summary: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <details className={cn("group rounded-lg border bg-card", className)}>
      <summary
        className={cn(
          "flex cursor-pointer list-none items-center gap-2 rounded-lg px-4 py-3",
          "text-sm font-medium select-none",
          "hover:bg-muted/50 focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none",
          "[&::-webkit-details-marker]:hidden",
        )}
      >
        <ChevronRight
          aria-hidden="true"
          className="size-4 shrink-0 text-muted-foreground group-open:rotate-90"
        />
        {summary}
      </summary>
      <div className="space-y-4 border-t px-4 py-4">{children}</div>
    </details>
  );
}
