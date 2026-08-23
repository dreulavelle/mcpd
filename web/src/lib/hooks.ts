import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "./api";

/** Re-runs a loader on an interval, and cleans up. */
export function usePoll(fn: () => void, ms: number) {
  useEffect(() => {
    fn();
    const t = setInterval(fn, ms);
    return () => clearInterval(t);
  }, [fn, ms]);
}

export interface Loaded<T> {
  data: T | null;
  /** Null until the first attempt has finished, either way. */
  error: string | null;
  reload: () => void;
}

/**
 * Loads something, keeps what it has while reloading, and never sets state
 * after unmount.
 *
 * Keeping the previous value through a reload is the point: these pages poll,
 * and a loader that cleared to null on every tick would flash a skeleton over
 * a table the operator was reading.
 *
 * `load` must be stable -- wrap it in useCallback -- because it is the effect's
 * dependency.
 */
export function useLoader<T>(
  load: () => Promise<T>,
  fallbackMessage: string,
  intervalMs?: number,
): Loaded<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const live = useRef(true);

  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);

  const reload = useCallback(() => {
    load().then(
      (value) => {
        if (!live.current) return;
        setData(value);
        setError(null);
      },
      (err) => {
        if (!live.current) return;
        setError(err instanceof ApiError ? err.detail : fallbackMessage);
      },
    );
  }, [load, fallbackMessage]);

  useEffect(() => {
    reload();
    if (!intervalMs) return;
    const t = setInterval(reload, intervalMs);
    return () => clearInterval(t);
  }, [reload, intervalMs]);

  return { data, error, reload };
}
