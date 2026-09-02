import {
  Component, useCallback, useEffect, useRef, useState,
  type ErrorInfo, type ReactNode,
} from "react";
import { Check, ChevronLeft, Copy } from "lucide-react";
import { Link } from "@/lib/router";
import { cn } from "@/lib/utils";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { Tone } from "./status";

/* -- page furniture -------------------------------------------------------- */

/** The heading block every page starts with. */
export function PageHeader({ title, lede, actions, back }: {
  title: string;
  lede?: ReactNode;
  actions?: ReactNode;
  /** A link out of a detail view, back to the list it came from. */
  back?: { to: string; label: string };
}) {
  return (
    <div className="mb-6 space-y-2">
      {back && (
        <Link
          to={back.to}
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ChevronLeft className="size-4" aria-hidden="true" />
          {back.label}
        </Link>
      )}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {lede && <p className="max-w-[68ch] text-sm text-muted-foreground">{lede}</p>}
    </div>
  );
}

/** A labelled heading over a block within a page. */
export function Section({ title, description, actions, children, className }: {
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("space-y-3", className)}>
      {(title || actions) && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            {title && (
              <h2 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
                {title}
              </h2>
            )}
            {description && (
              <p className="mt-1 text-sm text-muted-foreground">{description}</p>
            )}
          </div>
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}

/** One label-and-value pair, for a detail page. */
export function Detail({ label, children, className }: {
  label: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-1", className)}>
      <dt className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </dt>
      <dd className="text-sm">{children}</dd>
    </div>
  );
}

export function EmptyState({ mark, title, children }: {
  mark?: ReactNode;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-6 py-12 text-center">
      {mark && <div className="text-muted-foreground [&_svg]:size-6" aria-hidden="true">{mark}</div>}
      <h3 className="text-sm font-medium">{title}</h3>
      {children && (
        <p className="max-w-[46ch] text-sm text-muted-foreground">{children}</p>
      )}
    </div>
  );
}

/** Shaped like the content it replaces, so the page does not jump when it arrives. */
export function Loading({ rows = 4 }: { rows?: number }) {
  return (
    <div className="space-y-3" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-9" style={{ width: `${94 - i * 9}%` }} />
      ))}
    </div>
  );
}

/** The same, for a grid of cards. */
export function LoadingCards({ count = 4, className }: {
  count?: number;
  className?: string;
}) {
  return (
    <div
      className={cn("grid gap-3 md:grid-cols-2", className)}
      aria-busy="true" aria-label="Loading"
    >
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="space-y-3 rounded-xl border p-6">
          <Skeleton className="h-4 w-2/5" />
          <Skeleton className="h-3 w-full" />
          <Skeleton className="h-3 w-4/5" />
          <Skeleton className="h-8 w-16" />
        </div>
      ))}
    </div>
  );
}

const NOTICE_TONE: Record<Tone, string> = {
  good: "border-good/30 bg-good-soft text-good",
  attention: "border-attention/30 bg-attention-soft text-attention",
  problem: "border-problem/30 bg-problem-soft text-problem",
  info: "border-info/30 bg-info-soft text-info",
  neutral: "border-border bg-muted text-muted-foreground",
};

/** Something the page has to say, that stays until the reason for it goes. */
export function Notice({ tone = "info", icon, children }: {
  tone?: Tone;
  icon?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Alert
      role={tone === "problem" ? "alert" : undefined}
      className={cn(NOTICE_TONE[tone], "[&>svg]:text-current")}
    >
      {icon}
      {/* `block`, not the description's default grid, which would put a
          notice's <strong> and the sentence after it on separate rows. */}
      <AlertDescription className="block text-current [&_p]:text-current">
        {children}
      </AlertDescription>
    </Alert>
  );
}

/* -- long text --------------------------------------------------------------- */

/**
 * Text that may run to a paragraph, in a place that has room for a line or
 * two. A plugin's health message is a diagnosis, not a status word, and the
 * one that explains a timeout properly is a hundred and fifty words -- which
 * is right on the plugin's page and wrong in a table cell. The first lines
 * stay, the rest is a click away, and nothing is lost.
 */
export function Clamp({ children, lines = 2, className }: {
  children: string;
  lines?: 2 | 3;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  // Roughly what two lines of a 40ch column hold. Short text gets no toggle.
  const long = children.length > (lines === 2 ? 120 : 200);
  return (
    <span className={cn("block", className)}>
      <span className={cn("block", !open && long && (lines === 2 ? "line-clamp-2" : "line-clamp-3"))}>
        {children}
      </span>
      {long && (
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="mt-0.5 text-xs text-primary hover:underline"
          aria-expanded={open}
        >
          {open ? "Less" : "More"}
        </button>
      )}
    </span>
  );
}

/* -- copying --------------------------------------------------------------- */

function useCopy(value: string) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1600);
    } catch {
      // Refused outside a secure context, which a plain-http LAN address is.
      setCopied(false);
    }
  }, [value]);

  return { copied, copy };
}

export function Copyable({ value, label, className }: {
  value: string;
  label?: string;
  className?: string;
}) {
  const { copied, copy } = useCopy(value);
  return (
    <div className={cn("flex items-center gap-2 rounded-md border bg-muted/50 px-2 py-1", className)}>
      <code className="scroll-x min-w-0 flex-1 font-mono text-xs whitespace-nowrap">
        {value}
      </code>
      <Button
        variant="ghost" size="icon-sm" onClick={copy}
        aria-label={label ? `Copy ${label}` : "Copy"}
      >
        {copied
          ? <Check className="size-3.5 text-good" aria-hidden="true" />
          : <Copy className="size-3.5" aria-hidden="true" />}
      </Button>
    </div>
  );
}

export function CodeBlock({ children, className }: { children: string; className?: string }) {
  const { copied, copy } = useCopy(children);
  return (
    <div className={cn("relative rounded-md border bg-muted/50", className)}>
      <Button
        variant="ghost" size="icon-sm" onClick={copy}
        className="absolute top-1.5 right-1.5"
        aria-label="Copy"
      >
        {copied
          ? <Check className="size-3.5 text-good" aria-hidden="true" />
          : <Copy className="size-3.5" aria-hidden="true" />}
      </Button>
      <pre className="scroll-x p-3 pr-10 font-mono text-xs leading-relaxed">{children}</pre>
    </div>
  );
}

/** A link that leaves the app. Marked so it is obvious before clicking. */
export function Out({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a
      className="text-primary underline underline-offset-4 hover:no-underline"
      href={href} target="_blank" rel="noopener noreferrer"
    >
      {children}
    </a>
  );
}

/* -- failure --------------------------------------------------------------- */

/**
 * A page that throws does not take the console with it. A class, because React
 * offers the lifecycle only to those. Recovery is by remount -- the shell keys
 * this on the path -- since a retry in place would fail identically.
 *
 * `quiet` is for something the console offers rather than something it was
 * asked for: a secondary surface that fails should leave the page it was
 * sitting on alone, and an explanation of a failure nobody caused is worth
 * less than the space it takes.
 */
export class ErrorBoundary extends Component<
  { children: ReactNode; quiet?: boolean },
  { problem: string }
> {
  state = { problem: "" };

  static getDerivedStateFromError(error: unknown) {
    return { problem: error instanceof Error ? error.message : String(error) };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    // The console is where an operator is standing when this happens, so the
    // detail goes to the place they can copy it from.
    console.error("a dashboard page failed to render", error, info.componentStack);
  }

  render() {
    if (!this.state.problem) return this.props.children;
    if (this.props.quiet) return null;
    return (
      <Notice tone="problem">
        This page could not be drawn, so it is showing this instead of nothing.
        The rest of the console still works — go somewhere else and come back to
        try it again.
        <span className="mt-1 block font-mono text-xs opacity-80">
          {this.state.problem}
        </span>
      </Notice>
    );
  }
}
