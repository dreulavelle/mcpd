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
 * Loads something, keeps what it has while reloading, never sets state after
 * unmount. `load` must be stable: it is the effect's dependency.
 */
export function useLoader<T>(
  load: () => Promise<T>,
  fallbackMessage: string,
  intervalMs?: number,
): Loaded<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const live = useRef(true);
  // Which request is the latest. Changing a filter starts a new one while
  // the old may still be in flight, and answers do not arrive in order; the
  // old one landing second would show the wrong list until the next poll.
  const latest = useRef(0);

  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);

  const reload = useCallback(() => {
    const seq = ++latest.current;
    load().then(
      (value) => {
        if (!live.current || seq !== latest.current) return;
        setData(value);
        setError(null);
      },
      (err) => {
        if (!live.current || seq !== latest.current) return;
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
