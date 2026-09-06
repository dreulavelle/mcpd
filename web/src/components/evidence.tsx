import { cn } from "@/lib/utils";

/**
 * The evidence behind a sentence, one click away.
 *
 * The copy rule is that an error code, a route, a host name or a log line goes
 * under "Technical details" rather than into prose. This is that block in the
 * size a table row or a list entry can carry: a native `<details>`, so it is
 * keyboard-operable and findable by the browser's own search once open.
 *
 * `Disclosure` is the same idea drawn as a card, for a section of a page.
 * This one has no border of its own, because it sits inside something that
 * already has one.
 *
 * Renders nothing when there is nothing to show, so a caller can hand it a
 * detail that is usually absent without guarding every use.
 */
export function Evidence({ detail, className, label = "Technical details" }: {
  detail?: string;
  className?: string;
  label?: string;
}) {
  if (!detail) return null;
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
      <p className="mt-1.5 rounded-md border bg-muted/50 p-2 font-mono text-[11px] break-all text-muted-foreground">
        {detail}
      </p>
    </details>
  );
}
