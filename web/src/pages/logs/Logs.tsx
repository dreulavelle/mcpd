import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
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

/**
 * How a line is coloured, by level.
 *
 * The wash is what makes a problem findable while scrolling past hundreds of
 * lines -- a coloured word is easy to miss at this size, a tinted row is not.
 * It is only on the two levels worth interrupting for: tinting every line
 * would be the same as tinting none.
 */
function toneOf(level: string): { word: string; row: string } {
  switch (level) {
    case "ERROR": return { word: "text-problem font-semibold", row: "bg-problem-soft" };
    case "WARN": return { word: "text-attention font-semibold", row: "bg-attention-soft" };
    case "DEBUG": return { word: "text-muted-foreground", row: "" };
    default: return { word: "text-info", row: "" };
  }
}

/**
 * Which attribute names a line's source rather than describing what happened.
 *
 * Drawn as a chip in front of the message, because "which part of the host is
 * talking" is the question being asked while scanning, and answering it from
 * a `component=tunnel` pair somewhere among the others means reading the whole
 * line to find out whether it is worth reading.
 */
const SOURCE_KEYS = ["component", "plugin", "tunnel", "endpoint"] as const;

function sourceOf(rest: Record<string, unknown>): { key: string; label: string } | null {
  for (const key of SOURCE_KEYS) {
    const v = rest[key];
    if (typeof v === "string" && v !== "") return { key, label: v };
  }
  return null;
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
      <div className="grid h-[calc(100vh-24rem)] min-h-96 place-items-center rounded-md border text-sm text-muted-foreground">
        {paused ? "Paused." : "Nothing yet."}
      </div>
    );
  }

  return (
    <div
      ref={box} onScroll={onScroll}
      className="h-[calc(100vh-24rem)] min-h-96 overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-xs"
    >
      {lines.map((line) => (
        <LogLine key={line.key} line={line} />
      ))}
    </div>
  );
}

const LogLine = memo(function LogLine({ line }: { line: Line }) {
  if (line.gap !== undefined) {
    return (
      <div className="my-1 text-attention">
        — {line.gap} {line.gap === 1 ? "line" : "lines"} not shown: this page
        could not keep up —
      </div>
    );
  }

  const tone = toneOf(line.level);
  const source = sourceOf(line.rest);
  // `line` is the embedded tunnel client logging through this host: a whole
  // record of its own inside an attribute. Shown beneath rather than inline,
  // where it would push everything after it off the screen.
  const nested = typeof line.rest.line === "string" ? line.rest.line : "";
  // The chip's own key is not repeated among the attributes; every other one
  // is, including a second source key, which is information rather than noise
  // -- "the tunnel component, for the echo tunnel" is two facts.
  const attrs = Object.entries(line.rest).filter(
    ([k]) => k !== "line" && k !== source?.key,
  );

  return (
    <div className={cn("flex gap-2 rounded-sm px-1 py-px hover:bg-accent/60", tone.row)}>
      <span className="shrink-0 text-muted-foreground tabular-nums">{clock(line.time)}</span>
      <span className={cn("w-12 shrink-0", tone.word)}>{line.level}</span>
      {source && (
        <span className="shrink-0 rounded bg-accent px-1 text-accent-foreground">
          {source.label}
        </span>
      )}
      <span className="min-w-0 flex-1 break-words">
        <span className="text-foreground">{line.msg}</span>
        {attrs.map(([k, v]) => (
          <span key={k} className="ml-2 whitespace-nowrap text-muted-foreground">
            {k}=<span className="text-foreground">{render(v)}</span>
          </span>
        ))}
        {nested && (
          <span className="mt-px block border-l-2 border-border pl-2 text-muted-foreground">
            {nested}
          </span>
        )}
      </span>
    </div>
  );
});

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
