import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * The evidence behind a sentence, one click away.
 *
 * The copy rule is that an error code, a route, a host name or a log line goes
 * under "Technical details" rather than into prose. This is that block in the
 * size a table row or a list entry can carry: a native `<details>`, so it is
 * keyboard-operable and findable by the browser's own search once open.
 *
 * `Disclosure` is the same idea drawn as a card, for a section of a page. This
 * one has no border of its own, because it sits inside something that already
 * has one.
 *
 * `detail` is the common case -- one string of evidence. A caller with several
 * kinds of it passes them as children instead, so that a panel carrying a
 * status code, an error and a log line stays one disclosure rather than
 * becoming three.
 *
 * Renders nothing when there is nothing to show, so a caller can hand it a
 * detail that is usually absent without guarding every use.
 */
export function Evidence({ detail, children, className, label = "Technical details" }: {
  detail?: string;
  children?: ReactNode;
  className?: string;
  label?: string;
}) {
  if (!detail && !children) return null;
  return (
    <details className={cn("mt-1", className)}>
      <summary
        className={cn(
          "cursor-pointer list-none text-[11px] font-semibold tracking-wider",
          "text-muted-foreground uppercase select-none",
          "hover:text-foreground focus-visible:outline-none",
          "focus-visible:ring-[3px] focus-visible:ring-ring/50",
          "[&::-webkit-details-marker]:hidden",
        )}
      >
        {label}
      </summary>
      <div className="mt-1.5 space-y-2">
        {detail && <EvidenceText>{detail}</EvidenceText>}
        {children}
      </div>
    </details>
  );
}

/**
 * One block of evidence inside the disclosure: monospaced, breaking anywhere,
 * and set apart from the sentences around it.
 *
 * Exported so a caller assembling several of these gets the same panel rather
 * than its own copy of the classes.
 */
export function EvidenceText({ children, className }: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <p className={cn(
      "rounded-md border bg-muted/50 p-2 font-mono text-[11px] break-all",
      "text-muted-foreground",
      className,
    )}>
      {children}
    </p>
  );
}
