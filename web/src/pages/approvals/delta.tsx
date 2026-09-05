import type { Operation } from "@/lib/api";
import { fieldValue } from "@/lib/format";
import { cn } from "@/lib/utils";

/**
 * The first recorded field, as the two halves a layout can weight separately.
 *
 * Null where nothing was recorded, and null where the value is structured:
 * `{…}` in a sentence is the evidence-in-prose bug, and the field table on the
 * detail page carries every value in full anyway.
 */
export function fieldDelta(op: Operation): { from: string | null; to: string } | null {
  const first = op.changes?.[0];
  if (!first) return null;
  const to = fieldValue(first.to);
  if (to === null) return null;
  return { from: fieldValue(first.from), to };
}

/**
 * What differs, with the values carrying the weight and the words between them
 * standing back. It is the half of the sentence somebody is deciding on.
 */
export function FieldDelta({ delta, className }: {
  delta: { from: string | null; to: string };
  className?: string;
}) {
  return (
    <span className={cn("text-muted-foreground", className)}>
      {delta.from !== null && (
        <>from <span className="font-medium text-foreground">{delta.from}</span>{" "}</>
      )}
      to <span className="font-medium text-foreground">{delta.to}</span>
    </span>
  );
}
