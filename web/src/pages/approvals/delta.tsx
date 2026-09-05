import { type ChangeDelta } from "@/lib/format";
import { cn } from "@/lib/utils";

/**
 * What differs, with the values carrying the weight and the words between them
 * standing back. It is the half of the sentence somebody is deciding on.
 *
 * The parts come from `changeDelta`, which is also what `describeChange`
 * reads: this had its own copy of the rule, and the copy did not know that a
 * create names a thing rather than moving it, so a card read "to Getting
 * started" under a heading that said "called “Getting started”".
 */
export function FieldDelta({ delta, className }: {
  delta: ChangeDelta;
  className?: string;
}) {
  return (
    <span className={cn("text-muted-foreground", className)}>
      {delta.kind === "between" && (
        <>from <Value>{delta.from}</Value> to <Value>{delta.to}</Value></>
      )}
      {delta.kind === "to" && <>to <Value>{delta.to}</Value></>}
      {delta.kind === "called" && <>called <Value>{delta.name}</Value></>}
    </span>
  );
}

function Value({ children }: { children: string }) {
  return <span className="font-medium text-foreground">{children}</span>;
}
