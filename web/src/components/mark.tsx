import { cn } from "@/lib/utils";

/**
 * The mcpd mark: one hub, three things reached through it.
 *
 * The same geometry as public/favicon.svg and the assets under docs/assets,
 * drawn in currentColor so it wears whatever the text beside it is wearing
 * -- the accent in the rail, the panel's own lettering on the sign-in page.
 * One shape kept in step, rather than a file per surface that can drift.
 */
export function Mark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 32 32"
      fill="none"
      aria-hidden="true"
      className={cn("size-6 shrink-0", className)}
    >
      <g stroke="currentColor" strokeWidth="2.5" strokeLinecap="round">
        <path d="M16 16 7 7M16 16 25 10M16 16 16 26" />
      </g>
      <g fill="currentColor">
        <circle cx="16" cy="16" r="5" />
        <circle cx="7" cy="7" r="3" />
        <circle cx="25" cy="10" r="3" />
        <circle cx="16" cy="26" r="3" />
      </g>
    </svg>
  );
}
