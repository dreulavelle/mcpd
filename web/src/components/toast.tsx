import {
  createContext, useCallback, useContext, useEffect, useRef, useState,
  type ReactNode,
} from "react";
import { CircleAlert, CircleCheck, Info, TriangleAlert } from "lucide-react";
import { cn } from "@/lib/utils";
import type { Tone } from "./status";

/** Raising a toast. Named, because pages pass it down to the rows that report. */
export type Notify = (tone: Tone, text: string) => void;

interface Toast {
  id: number;
  tone: Tone;
  text: string;
}

const ToastContext = createContext<Notify>(() => undefined);

const ICON: Record<Tone, typeof Info> = {
  good: CircleCheck,
  attention: TriangleAlert,
  problem: CircleAlert,
  info: Info,
  neutral: Info,
};

const TONE: Record<Tone, string> = {
  good: "border-good/30 text-good",
  attention: "border-attention/30 text-attention",
  problem: "border-problem/30 text-problem",
  info: "border-info/30 text-info",
  neutral: "border-border text-foreground",
};

/**
 * One stack for the whole console, above the router: a per-page one vanishes
 * exactly when the action it confirms navigates away.
 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const t of pending) clearTimeout(t);
      pending.clear();
    };
  }, []);

  const notify = useCallback<Notify>((tone, text) => {
    const id = Date.now() + Math.random();
    setToasts((current) => [...current, { id, tone, text }]);
    const timer = setTimeout(() => {
      setToasts((current) => current.filter((t) => t.id !== id));
      timers.current.delete(timer);
    }, 4500);
    timers.current.add(timer);
  }, []);

  return (
    <ToastContext.Provider value={notify}>
      {children}
      <div
        className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2"
        aria-live="polite"
      >
        {toasts.map((t) => {
          const Icon = ICON[t.tone];
          return (
            <div
              key={t.id}
              className={cn(
                "pointer-events-auto flex items-start gap-2 rounded-lg border bg-popover",
                "px-3 py-2 text-sm shadow-lg animate-in slide-in-from-bottom-2 fade-in",
                TONE[t.tone],
              )}
            >
              <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
              <span className="text-popover-foreground">{t.text}</span>
            </div>
          );
        })}
      </div>
    </ToastContext.Provider>
  );
}

/** Raises a toast from anywhere under the provider. */
export function useNotify(): Notify {
  return useContext(ToastContext);
}
