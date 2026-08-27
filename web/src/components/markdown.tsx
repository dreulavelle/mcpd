import { Fragment, type ReactNode } from "react";

/**
 * Release notes, rendered.
 *
 * Deliberately a subset rather than a markdown library. The only markdown this
 * console ever shows is a changelog written by release-please, which is a very
 * regular document: a heading, a section per commit type, and a bullet per
 * change with links to the pull request and the commit. A full parser and its
 * dependency tree would be carried on every page to render that one panel.
 *
 * Anything outside the subset degrades to its own source text rather than
 * disappearing, so an unexpected document is ugly instead of missing.
 *
 * **It builds React elements and never HTML.** These notes come from the
 * GitHub API -- content this host did not write and cannot vouch for -- and
 * `dangerouslySetInnerHTML` over a remote string is how a release note becomes
 * a script. Links are checked too: only http and https survive, because a
 * `javascript:` href is a link that runs.
 */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return <div className={className}>{blocks(text.trim())}</div>;
}

/** Groups lines into headings, lists and paragraphs. */
function blocks(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const lines = text.split("\n");
  let paragraph: string[] = [];
  let items: string[] = [];

  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    out.push(
      <p key={`p${out.length}`} className="mt-2 first:mt-0">
        {inline(paragraph.join(" "))}
      </p>,
    );
    paragraph = [];
  };
  const flushList = () => {
    if (items.length === 0) return;
    out.push(
      <ul key={`u${out.length}`} className="mt-2 ml-4 list-disc space-y-1 first:mt-0">
        {items.map((item, i) => (
          <li key={i}>{inline(item)}</li>
        ))}
      </ul>,
    );
    items = [];
  };
  const flush = () => {
    flushParagraph();
    flushList();
  };

  for (const raw of lines) {
    const line = raw.trimEnd();

    if (line.trim() === "") {
      flush();
      continue;
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      flush();
      // One step down from the page's own hierarchy: a changelog's top heading
      // is a version, which sits under the card's title rather than beside it.
      const depth = heading[1]!.length;
      const size = depth <= 2 ? "text-sm font-medium" : "text-xs font-medium";
      out.push(
        <p key={`h${out.length}`} className={`mt-3 first:mt-0 ${size} text-foreground`}>
          {inline(heading[2]!)}
        </p>,
      );
      continue;
    }

    const bullet = /^\s*[*-]\s+(.*)$/.exec(line);
    if (bullet) {
      flushParagraph();
      items.push(bullet[1]!);
      continue;
    }

    flushList();
    paragraph.push(line.trim());
  }
  flush();
  return out;
}

/** Matches a link, bold run, or code span, in that order of preference. */
const INLINE = /\[([^\]]*)\]\(([^)\s]+)\)|\*\*([^*]+)\*\*|`([^`]+)`/g;

/** Turns one line into text and the marked-up runs inside it. */
function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let key = 0;

  for (const m of text.matchAll(INLINE)) {
    const at = m.index;
    if (at > last) out.push(<Fragment key={key++}>{text.slice(last, at)}</Fragment>);
    last = at + m[0].length;

    const [, linkText, href, bold, code] = m;
    if (href !== undefined) {
      const safe = safeHref(href);
      if (safe === null) {
        // Rendered as it was written. A link this host will not follow is
        // still information, and silently dropping it would hide the fact
        // that a note contained one.
        out.push(<Fragment key={key++}>{m[0]}</Fragment>);
      } else {
        out.push(
          <a
            key={key++}
            href={safe}
            target="_blank"
            rel="noreferrer noopener"
            className="underline underline-offset-2"
          >
            {linkText || safe}
          </a>,
        );
      }
    } else if (bold !== undefined) {
      out.push(
        <strong key={key++} className="font-medium text-foreground">
          {bold}
        </strong>,
      );
    } else if (code !== undefined) {
      out.push(
        <code key={key++} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.9em]">
          {code}
        </code>,
      );
    }
  }
  if (last < text.length) out.push(<Fragment key={key++}>{text.slice(last)}</Fragment>);
  return out;
}

/**
 * The href, if it is one this host is willing to render as a link.
 *
 * Only http and https. `javascript:` is the reason this exists; `data:` is the
 * other one. Anything else comes back null and is shown as text.
 */
function safeHref(raw: string): string | null {
  try {
    const url = new URL(raw, window.location.origin);
    return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
  } catch {
    return null;
  }
}
