import {
  useCallback, useEffect, useMemo, useRef, useState, type Ref,
} from "react";
import { Circle, CircleCheck, ListChecks, X } from "lucide-react";
import type { Capability } from "@/lib/capabilities";
import {
  isHidden, nothingLeft, probeAll, probeInOrder, progress, remember, STEPS,
  whenIdle, type Found, type Step,
} from "@/lib/getting-started";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

/**
 * What is left to set up on this host, pinned out of the way.
 *
 * Three rules shape it. It answers each step from real state, so a configured
 * host is never told to add its first plugin. It stops rendering itself once
 * there is nothing left, because a permanent "Get started" on a finished host
 * is clutter nobody should have to dismiss. And it is never modal: it takes no
 * focus on load, covers nothing until it is opened, and every request it makes
 * waits for the page it is sitting on.
 */
export function GettingStarted() {
  // Up front rather than inside the filter: `useCan` is a hook, and a predicate
  // may not run for every entry.
  const read = useCan("read");
  const propose = useCan("propose");
  const approve = useCan("approve");
  const admin = useCan("admin");

  const steps = useMemo<readonly Step[]>(() => {
    const held: Record<Capability, boolean> = { read, propose, approve, admin };
    return STEPS.filter((step) => held[step.capability]);
  }, [read, propose, approve, admin]);

  const [found, setFound] = useState<Found>(() => new Map());
  const [probed, setProbed] = useState(false);
  const [open, setOpen] = useState(false);
  const [gone, setGone] = useState(isHidden);

  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  // Set only when a close was somebody's decision, so the trigger is not
  // grabbed on first render.
  const returnFocus = useRef(false);

  useEffect(() => {
    if (gone || steps.length === 0) return;
    let live = true;
    const cancel = whenIdle(() => {
      probeInOrder(steps).then((f) => {
        if (!live) return;
        setFound(f);
        setProbed(true);
      });
    });
    return () => {
      live = false;
      cancel();
    };
  }, [gone, steps]);

  const done = probed && nothingLeft(steps, found);

  useEffect(() => {
    if (!done) return;
    // Remembered as soon as it is true, so the five requests are never made
    // again on this browser. It stays on screen while somebody is reading it
    // and goes when they close it, rather than vanishing under them.
    remember("complete");
    if (!open) setGone(true);
  }, [done, open]);

  useEffect(() => {
    if (open) {
      panel.current?.focus();
      return;
    }
    if (returnFocus.current) {
      returnFocus.current = false;
      trigger.current?.focus();
    }
  }, [open]);

  const show = useCallback(() => {
    setOpen(true);
    // Everything, now that somebody is reading the list: the ordered probe
    // stopped at the first unfinished step and knows nothing about the rest.
    probeAll(steps).then(setFound);
  }, [steps]);

  const close = useCallback(() => {
    returnFocus.current = true;
    setOpen(false);
  }, []);

  const dismiss = useCallback(() => {
    remember("dismissed");
    setGone(true);
  }, []);

  if (gone || steps.length === 0 || !probed) return null;

  return (
    // The toast region owns this corner too, at z-50. That is the right way
    // round: a toast is a moment and this is furniture, so the moment goes on
    // top and is gone again.
    <div className="fixed right-4 bottom-4 z-30 print:hidden">
      {open
        ? <Panel
            ref={panel}
            steps={steps}
            found={found}
            onNavigate={() => setOpen(false)}
            onClose={close}
            onDismiss={dismiss}
          />
        : (
          <Button
            ref={trigger}
            variant="outline"
            size="sm"
            className="shadow-md"
            onClick={show}
          >
            <ListChecks className="size-4" aria-hidden="true" />
            Get started
          </Button>
        )}
    </div>
  );
}

/**
 * A dialog, but deliberately not a modal one: no `aria-modal`, no focus trap
 * and no overlay, because nothing here is worth interrupting an operator for.
 * Escape is bound to the panel rather than to the window for the same reason --
 * a global handler would also fire for a modal opened over it.
 */
function Panel({ ref, steps, found, onNavigate, onClose, onDismiss }: {
  ref: Ref<HTMLDivElement>;
  steps: readonly Step[];
  found: Found;
  onNavigate: () => void;
  onClose: () => void;
  onDismiss: () => void;
}) {
  const { done, total } = progress(steps, found);

  return (
    <div
      ref={ref}
      role="dialog"
      aria-labelledby="getting-started-title"
      tabIndex={-1}
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        e.stopPropagation();
        onClose();
      }}
      className="w-[min(24rem,calc(100vw-2rem))] rounded-xl border bg-popover text-popover-foreground shadow-lg"
    >
      <div className="flex items-start gap-2 border-b px-4 py-3">
        <div className="min-w-0 flex-1">
          <h2 id="getting-started-title" className="text-sm font-semibold">
            Get started
          </h2>
          {total > 0 && (
            <p className="text-xs text-muted-foreground tabular-nums">
              {done} of {total} done
            </p>
          )}
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onClose}>
          <X className="size-4" aria-hidden="true" />
          <span className="sr-only">Close</span>
        </Button>
      </div>

      <ul className="space-y-0.5 p-2">
        {steps.map((step) => {
          const outcome = found.get(step.id);
          // A step that does not apply here, and one whose state could not be
          // read, are both left out: a row that cannot say where it stands is
          // the noise this panel exists to avoid.
          if (outcome?.kind !== "done" && outcome?.kind !== "todo") return null;
          const finished = outcome.kind === "done";
          const to = outcome.kind === "todo" ? outcome.to ?? step.to : step.to;

          return (
            <li key={step.id}>
              <Link
                to={to}
                onClick={onNavigate}
                className="flex gap-2.5 rounded-lg px-2 py-2 transition-colors hover:bg-accent/60"
              >
                {finished
                  ? <CircleCheck className="mt-0.5 size-4 shrink-0 text-good" aria-hidden="true" />
                  : <Circle className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />}
                <span className="min-w-0">
                  <span className={cn("block text-sm font-medium", finished && "text-muted-foreground")}>
                    {step.label}
                    <span className="sr-only">{finished ? " — done" : " — still to do"}</span>
                  </span>
                  <span className="block text-xs text-muted-foreground">{step.detail}</span>
                </span>
              </Link>
            </li>
          );
        })}
      </ul>

      <div className="border-t px-2 py-1.5">
        <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={onDismiss}>
          Don't show this again
        </Button>
      </div>
    </div>
  );
}
