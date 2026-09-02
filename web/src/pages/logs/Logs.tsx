import { Fragment, memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Check, Copy, Download } from "lucide-react";
import { PageHeader } from "@/components/chrome";
import { Chip } from "@/components/status";
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

/**
 * Reads one logfmt record: `key=value`, or `key="value with spaces"`.
 *
 * Needed because the tunnel client logs a whole record of its own and this
 * host relays it as a single `line` attribute. Left as it arrives, every one
 * of those is a row whose message is the constant "tunnel-client" and whose
 * content is six hundred characters of grey -- and, worse, an error inside one
 * is wrapped in an INFO record and never turns red.
 */
function parseLogfmt(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  // key=, then either a quoted run (honouring backslash escapes) or a bare one.
  const pair = /([\w.-]+)=("(?:[^"\\]|\\.)*"|[^\s]*)/g;
  for (const [, key, raw] of text.matchAll(pair)) {
    if (!key || raw === undefined) continue;
    out[key] = raw.startsWith('"')
      ? raw.slice(1, -1).replace(/\\(.)/g, "$1")
      : raw;
  }
  return out;
}

/**
 * Attributes of a relayed record that say nothing on the page.
 *
 * The instance id is one constant repeated on every line of a run, and the
 * tunnel is already the chip in front of the message. Dropped from the summary
 * only -- opening a line still shows everything it carried, because "nothing
 * on the page" is a judgement about scanning, not about the record.
 */
const NOISE = new Set(["client_instance_id", "tunnel_id", "time", "level", "msg"]);

/**
 * Unwraps a relayed record into the row it deserves.
 *
 * Its own level, so an error inside a tunnel is an error on this page. Its own
 * message, because "tunnel-client" is the name of the pipe rather than
 * anything that happened.
 */
function unwrap(level: string, msg: string, rest: Record<string, unknown>): {
  level: string; msg: string; rest: Record<string, unknown>;
} {
  const relayed = rest.line;
  if (typeof relayed !== "string" || relayed === "") return { level, msg, rest };

  const inner = parseLogfmt(relayed);
  if (!inner.msg) return { level, msg, rest };

  const merged: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(rest)) if (k !== "line") merged[k] = v;
  for (const [k, v] of Object.entries(inner)) if (!NOISE.has(k)) merged[k] = v;

  return {
    level: RANK[inner.level ?? ""] !== undefined ? inner.level! : level,
    msg: inner.msg,
    rest: merged,
  };
}

// Exported for the tests, which check it against records copied verbatim off
// a running host. A parser written against an imagined format is a parser
// tested against the imagination.
export const parseLogfmtForTest = parseLogfmt;
export const asTextForTest = (line: Omit<Line, "key">) => asText({ ...line, key: 0 });

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
// Most specific first. "cnmaestro" tells a reader which tunnel is talking;
// "tunnel" only tells them what they can already see from the rest of the
// screen, because on a busy host most lines are tunnel lines.
const SOURCE_KEYS = ["plugin", "tunnel", "component", "endpoint"] as const;

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
  const [paused, setPaused] = useState(false);
  const [level, setLevel] = useState<Level>("ALL");
  const [needle, setNeedle] = useState("");
  // "" is every source. A chip per source seen so far, because "which part
  // of the host is talking" is the question being asked while scanning.
  const [source, setSource] = useState("");

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
    const stream = new EventSource("/api/logs/stream", { withCredentials: true });

    stream.onopen = () => setConnected(true);

    stream.onmessage = (e) => {
      try {
        const { time, level: lvl, msg, ...rest } = JSON.parse(e.data);
        const record = unwrap(
          typeof lvl === "string" ? lvl : "INFO",
          typeof msg === "string" ? msg : "",
          rest,
        );
        append({ time: typeof time === "string" ? time : "", ...record });
      } catch {
        // A line this page cannot parse is still a line the host wrote, and
        // hiding it would make the log lie about what happened.
        append({ time: "", level: "INFO", msg: e.data, rest: {} });
      }
    };

    // The host says so when a browser has fallen too far behind to keep up.
    stream.addEventListener("dropped", (e) => {
      append({ time: "", level: "WARN", msg: "", rest: {}, gap: Number(e.data) || 0 });
    });

    // Not an error to report: EventSource reconnects on its own, and a host
    // restarting is the ordinary reason this fires. The badge says what is
    // true; a red notice would cry wolf on every deploy.
    stream.onerror = () => setConnected(false);

    return () => stream.close();
  }, [append]);

  const sources = useMemo(() => {
    const seen = new Map<string, number>();
    for (const l of lines) {
      const label = sourceOf(l.rest)?.label;
      if (label) seen.set(label, (seen.get(label) ?? 0) + 1);
    }
    return [...seen.entries()].sort((a, b) => b[1] - a[1]).map(([label]) => label);
  }, [lines]);

  const shown = useMemo(() => {
    const floor = level === "ALL" ? -1 : (RANK[level] ?? -1);
    const q = needle.trim().toLowerCase();
    return lines.filter((l) => {
      if (l.gap !== undefined) return true;
      if (floor >= 0 && (RANK[l.level] ?? 1) < floor) return false;
      if (source && sourceOf(l.rest)?.label !== source) return false;
      if (!q) return true;
      return (l.msg + " " + JSON.stringify(l.rest)).toLowerCase().includes(q);
    });
  }, [lines, level, needle, source]);

  // What is on screen, as the host would have written it, for a ticket.
  function download() {
    const text = shown
      .filter((l) => l.gap === undefined)
      .map((l) => asText(l))
      .join("\n");
    const blob = new Blob([text + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `mcpd-log-${new Date().toISOString().replace(/[:.]/g, "-")}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  }

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
            <Button
              variant="outline" onClick={download} disabled={shown.length === 0}
              title="Save the lines on screen as a text file"
            >
              <Download className="size-4" aria-hidden="true" />
              Save
            </Button>
          </div>
        }
      />

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

          {sources.length > 1 && (
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Source">
              <button
                type="button"
                onClick={() => setSource("")}
                aria-pressed={source === ""}
              >
                <Chip tone={source === "" ? "info" : "neutral"}>every source</Chip>
              </button>
              {sources.map((label) => (
                <button
                  key={label}
                  type="button"
                  onClick={() => setSource(source === label ? "" : label)}
                  aria-pressed={source === label}
                >
                  <Chip tone={source === label ? "info" : "neutral"}>{label}</Chip>
                </button>
              ))}
            </div>
          )}

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

/**
 * One record, on one line.
 *
 * A log viewer earns its keep by being scannable, and a row that wraps to four
 * lines when an attribute is long turns a screenful from forty records into
 * eight. So the summary is a single truncated line and the detail is a click
 * away -- which is also the only honest place to put the attributes a record
 * carries but a reader does not usually want.
 */
const LogLine = memo(function LogLine({ line }: { line: Line }) {
  const [open, setOpen] = useState(false);

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
  const attrs = Object.entries(line.rest).filter(([k]) => k !== source?.key);

  return (
    <div className={cn("rounded-sm hover:bg-accent/60", tone.row)}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group flex w-full gap-2 px-1 py-px text-left"
      >
        <span className="shrink-0 text-muted-foreground tabular-nums">
          {clock(line.time)}
        </span>
        <span className={cn("w-12 shrink-0", tone.word)}>{line.level}</span>
        {source && (
          <span className="shrink-0 rounded bg-accent px-1 text-accent-foreground">
            {source.label}
          </span>
        )}
        {/* min-w-0 is load-bearing: without it a flex child refuses to shrink
            below its content and truncate never engages. */}
        <span className="min-w-0 flex-1 truncate">
          <span className="text-foreground">{line.msg}</span>
          {attrs.map(([k, v]) => (
            <span key={k} className="ml-2 text-muted-foreground">
              {k}=<span className="text-foreground">{render(v)}</span>
            </span>
          ))}
        </span>
      </button>

      {open && (
        <div className="ml-3 border-l-2 border-border py-1 pl-3">
          {attrs.length > 0 && (
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-px">
              {attrs.map(([k, v]) => (
                <Fragment key={k}>
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="break-all text-foreground">{render(v)}</dd>
                </Fragment>
              ))}
            </dl>
          )}
          <CopyLine line={line} />
        </div>
      )}
    </div>
  );
});

/**
 * One record, as text, for a ticket. The support caller wants the line as
 * the host wrote it, correlation id and all, not a screenshot of it.
 */
function asText(line: Line): string {
  const attrs = Object.entries(line.rest)
    .map(([k, v]) => `${k}=${typeof v === "string" && /\s/.test(v) ? JSON.stringify(v) : render(v)}`)
    .join(" ");
  return `${line.time || "-"} ${line.level} ${line.msg}${attrs ? " " + attrs : ""}`;
}

function CopyLine({ line }: { line: Line }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(asText(line));
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Refused outside a secure context, which a plain-http LAN address is.
    }
  }
  return (
    <button
      type="button"
      onClick={copy}
      className="mt-1 inline-flex items-center gap-1 text-muted-foreground hover:text-foreground"
    >
      {copied
        ? <Check className="size-3 text-good" aria-hidden="true" />
        : <Copy className="size-3" aria-hidden="true" />}
      {copied ? "Copied" : "Copy this line"}
    </button>
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
