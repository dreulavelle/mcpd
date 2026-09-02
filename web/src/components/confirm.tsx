import {
  createContext, useCallback, useContext, useRef, useState, type ReactNode,
} from "react";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";

/** What a confirmation asks, and what its button says. */
export interface Question {
  title: string;
  description?: string;
  /** The verb on the button. Defaults to the first word of the title. */
  action?: string;
  /** Painted as destructive. Defaults to true: most things worth asking about are. */
  destructive?: boolean;
}

type Ask = (question: Question | string) => Promise<boolean>;

const ConfirmContext = createContext<Ask>(() => Promise.resolve(false));

/**
 * "Delete alice@example.com? This cannot be undone." -- the question is the
 * title, and what follows it is the description. A plain string is accepted
 * so a call site can keep its one sentence and still get a dialog in the
 * console's own style rather than the browser's.
 */
export function parseQuestion(q: Question | string): Question {
  if (typeof q !== "string") return q;
  const at = q.indexOf("?");
  if (at < 0) return { title: q };
  return { title: q.slice(0, at + 1).trim(), description: q.slice(at + 1).trim() || undefined };
}

/**
 * A yes-or-no put to the operator, awaited.
 *
 * Replaces the browser's confirm(), which cannot be styled, cannot be told
 * apart from a site's own dialog, and in some browsers is suppressed after
 * the first one on a page -- at which point every "Delete" quietly returns
 * false and nothing says why. One provider for the console, so a row deep in
 * a table asks the same way the audit page does.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [question, setQuestion] = useState<Question | null>(null);
  const settle = useRef<((yes: boolean) => void) | null>(null);

  const ask = useCallback<Ask>((q) => {
    // A second question while one is open answers the first with "no"
    // rather than stacking dialogs nobody can see.
    settle.current?.(false);
    setQuestion(parseQuestion(q));
    return new Promise<boolean>((resolve) => { settle.current = resolve; });
  }, []);

  function answer(yes: boolean) {
    settle.current?.(yes);
    settle.current = null;
    setQuestion(null);
  }

  const action = question?.action ?? question?.title.split(/\s/)[0]?.replace(/[?.!]/g, "") ?? "Continue";

  return (
    <ConfirmContext.Provider value={ask}>
      {children}
      <AlertDialog open={question !== null} onOpenChange={(open) => { if (!open) answer(false); }}>
        {question && (
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{question.title}</AlertDialogTitle>
              {question.description && (
                <AlertDialogDescription>{question.description}</AlertDialogDescription>
              )}
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel onClick={() => answer(false)}>Cancel</AlertDialogCancel>
              <AlertDialogAction
                variant={question.destructive === false ? "default" : "destructive"}
                onClick={() => answer(true)}
              >
                {action}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        )}
      </AlertDialog>
    </ConfirmContext.Provider>
  );
}

/** Asks. Resolves true only when the operator chose the action. */
export function useConfirm(): Ask {
  return useContext(ConfirmContext);
}
