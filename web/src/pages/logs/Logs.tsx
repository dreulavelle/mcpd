import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Notice, PageHeader } from "@/components/chrome";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";

/**
 * How many lines the page will hold.
 *
 * A log is unbounded and a browser tab is not. Ten times what the host keeps
 * for a new arrival, so somebody who leaves this open through an incident has
 * the whole of it, and the tab does not grow until it is killed.
 */
const HELD = 5000;

/** One record, as the host renders it. */
interface Line {
  /** Ours, not the host's: two records can share a timestamp. */
  key: number;
  time: string;
  level: string;
  msg: string;
  /** Everything else the record carried, already redacted by the host. */
  rest: Record<string, unknown>;
  /** Set on the marker that stands in for lines this browser never received. */
  gap?: number;
}

const LEVELS = ["ALL", "DEBUG", "INFO", "WARN", "ERROR"] as const;
type Level = (typeof LEVELS)[number];

/** Ranked so a filter means "this and worse", which is how people read one. */
const RANK: Record<string, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

function toneOf(level: string): string {
  switch (level) {
    case "ERROR": return "text-problem";
    case "WARN": return "text-attention";
    case "DEBUG": return "text-muted-foreground";
    default: return "text-muted-foreground";
  }
}

/**
 * The host's log, as it happens.
 *
 * Server-sent events rather than a socket: nothing travels upwards, and
 * EventSource reconnects on its own after a restart or a dropped connection,
 * which is most of what a hand-written client would have to get right.
 */
export function Logs() {
  const [lines, setLines] = useState<Line[]>([]);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState("");
  const [paused, setPaused] = useState(false);
  const [level, setLevel] = useState<Level>("ALL");
  const [needle, setNeedle] = useState("");

  // Read by the event handler, which is created once and would otherwise close
  // over the first value of `paused` for ever.
  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  const nextKey = useRef(0);

  const append = useCallback((line: Omit<Line, "key">) => {
    if (pausedRef.current) return;
    setLines((held) => {
      const next = held.concat({ ...line, key: nextKey.current++ });
      return next.length > HELD ? next.slice(next.length - HELD) : next;
    });
  }, []);

  useEffect(() => {
    const source = new EventSource("/api/logs/stream", { withCredentials: true });

    source.onopen = () => { setConnected(true); setError(""); };

    source.onmessage = (e) => {
      try {
        const { time, level: lvl, msg, ...rest } = JSON.parse(e.data);
        append({
          time: typeof time === "string" ? time : "",
          level: typeof lvl === "string" ? lvl : "INFO",
          msg: typeof msg === "string" ? msg : "",
          rest,
        });
      } catch {
        // A line this page cannot parse is still a line the host wrote, and
        // hiding it would make the log lie about what happened.
        append({ time: "", level: "INFO", msg: e.data, rest: {} });
      }
    };

    // The host says so when a browser has fallen too far behind to keep up.
    source.addEventListener("dropped", (e) => {
      append({ time: "", level: "WARN", msg: "", rest: {}, gap: Number(e.data) || 0 });
    });

    source.onerror = () => {
      setConnected(false);
      // Not an error to report: EventSource reconnects on its own, and a host
      // restarting is the ordinary reason this fires. The badge says what is
      // true; a red notice would cry wolf on every deploy.
      setError("");
    };

    return () => source.close();
  }, [append]);

  const shown = useMemo(() => {
    const floor = level === "ALL" ? -1 : (RANK[level] ?? -1);
    const q = needle.trim().toLowerCase();
    return lines.filter((l) => {
      if (l.gap !== undefined) return true;
      if (floor >= 0 && (RANK[l.level] ?? 1) < floor) return false;
      if (!q) return true;
      return (l.msg + " " + JSON.stringify(l.rest)).toLowerCase().includes(q);
    });
  }, [lines, level, needle]);

  return (
    <>
      <PageHeader
        title="Logs"
        lede="What this host is doing, as it does it."
        actions={
          <div className="flex items-center gap-2">
            <Button variant="outline" onClick={() => setPaused((p) => !p)}>
              {paused ? "Resume" : "Pause"}
            </Button>
            <Button variant="outline" onClick={() => setLines([])}>Clear</Button>
          </div>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}

      <Card className="mt-4">
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="log-level">Level</Label>
              <NativeSelect
                id="log-level" value={level}
                onChange={(e) => setLevel(e.target.value as Level)}
              >
                {LEVELS.map((l) => (
                  <option key={l} value={l}>{l === "ALL" ? "Everything" : l}</option>
                ))}
              </NativeSelect>
            </div>

            <div className="min-w-48 flex-1 space-y-1.5">
              <Label htmlFor="log-find">Containing</Label>
              <Input
                id="log-find" value={needle} placeholder="tunnel, cnmaestro, refused…"
                onChange={(e) => setNeedle(e.target.value)}
              />
            </div>

            <div className="flex items-center gap-2 pb-2 text-sm text-muted-foreground">
              <span
                aria-hidden="true"
                className={cn("size-2 rounded-full", connected ? "bg-good" : "bg-attention")}
              />
              {connected ? "Live" : "Reconnecting…"}
              {paused && " · paused"}
            </div>
          </div>

          <LogView lines={shown} paused={paused} />

          <p className="text-xs text-muted-foreground">
            Values under keys like <code className="font-mono">api_key</code>,{" "}
            <code className="font-mono">token</code> and{" "}
            <code className="font-mono">password</code> are replaced before a
            line is written, so they are not here and not in the host's own log
            either. A secret written into the text of a message rather than
            given a key of its own is not caught by that.
          </p>
        </CardContent>
      </Card>
    </>
  );
}

/**
 * The lines themselves.
 *
 * Follows the newest line only while the reader is already at the bottom.
 * Scrolling up is how somebody reads what just happened, and a view that
 * yanked them back down on the next line would make that impossible.
 */
function LogView({ lines, paused }: { lines: Line[]; paused: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const following = useRef(true);

  useEffect(() => {
    const el = box.current;
    if (!el || !following.current) return;
    el.scrollTop = el.scrollHeight;
  }, [lines]);

  function onScroll() {
    const el = box.current;
    if (!el) return;
    // A few pixels of slack: a scroll that lands one pixel short of the end is
    // somebody at the bottom, not somebody who has left it.
    following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  }

  if (lines.length === 0) {
    return (
      <div className="grid h-96 place-items-center rounded-md border text-sm text-muted-foreground">
        {paused ? "Paused." : "Nothing yet."}
      </div>
    );
  }

  return (
    <div
      ref={box} onScroll={onScroll}
      className="h-96 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs"
    >
      {lines.map((line) => (
        <LogLine key={line.key} line={line} />
      ))}
    </div>
  );
}

function LogLine({ line }: { line: Line }) {
  if (line.gap !== undefined) {
    return (
      <div className="my-1 text-attention">
        — {line.gap} {line.gap === 1 ? "line" : "lines"} not shown: this page
        could not keep up —
      </div>
    );
  }

  const attrs = Object.entries(line.rest);
  return (
    <div className="flex gap-2 py-px break-words">
      <span className="shrink-0 text-muted-foreground">{clock(line.time)}</span>
      <span className={cn("w-12 shrink-0", toneOf(line.level))}>{line.level}</span>
      <span className="min-w-0 flex-1">
        {line.msg}
        {attrs.map(([k, v]) => (
          <span key={k} className="ml-2 text-muted-foreground">
            {k}=<span className="text-foreground">{render(v)}</span>
          </span>
        ))}
      </span>
    </div>
  );
}

/** Just the time. The date is the same for every line anybody is watching. */
function clock(stamp: string): string {
  const at = new Date(stamp);
  return Number.isNaN(at.getTime())
    ? "--:--:--"
    : at.toLocaleTimeString(undefined, { hour12: false });
}

function render(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v);
}
