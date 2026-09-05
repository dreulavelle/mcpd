import { Mark } from "@/components/mark";

/**
 * The brand, on the one page that is about the whole host.
 *
 * The overview leads with a verdict, and the verdict stays outside this: the
 * band is the same night as the sign-in panel, and the status colours are
 * chosen for the console's surfaces rather than for it. So this carries only
 * what is true of the host whatever its state -- the mark, the name, the one
 * line the README opens with, and the version when it is known -- and the
 * hub motif from the banner, drawn faintly in the accent.
 */
export function Masthead({ version }: { version?: string }) {
  return (
    <div
      className="relative mb-8 overflow-hidden rounded-xl border bg-panel px-6 py-5 text-panel-foreground"
      style={{
        backgroundImage:
          "linear-gradient(120deg, var(--panel), color-mix(in oklab, var(--panel) 88%, var(--panel-accent)))",
      }}
    >
      <Orbit className="pointer-events-none absolute -top-16 right-6 hidden h-[17rem] w-[17rem] text-panel-accent sm:block" />

      <div className="relative flex items-center gap-3">
        <Mark className="size-9 text-panel-accent" />
        <div className="min-w-0">
          <div className="flex items-baseline gap-2.5">
            <span className="text-2xl font-semibold tracking-tight">mcpd</span>
            {version && (
              <span className="font-mono text-xs text-panel-muted">{version}</span>
            )}
          </div>
          <p className="text-sm text-panel-muted">
            Private infrastructure, connected to AI.
          </p>
        </div>
      </div>
    </div>
  );
}

/**
 * The banner's hub-and-spoke motif: a hub, two rings around it, and three
 * systems on the rings, each with the hairline of a satellite. The geometry
 * is the banner's, scaled to a square, and every stroke is currentColor so
 * the surface it sits on decides how loud it is.
 */
function Orbit({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 320 320" fill="none" aria-hidden="true" className={className}>
      <g stroke="currentColor" strokeWidth="1.25" opacity="0.22">
        <circle cx="160" cy="160" r="150" />
        <circle cx="160" cy="160" r="116" />
        <path d="M0 160H320M160 0V320" />
      </g>
      <circle cx="160" cy="160" r="86" stroke="currentColor" strokeWidth="1.25" opacity="0.35" />
      <circle cx="160" cy="160" r="44" stroke="currentColor" strokeWidth="1.25" opacity="0.5" />
      <g transform="translate(115 115) scale(2.8)" opacity="0.9">
        <g stroke="currentColor" strokeWidth="2.5" strokeLinecap="round">
          <path d="M16 16 7 7M16 16 25 10M16 16 16 26" />
        </g>
        <g fill="currentColor">
          <circle cx="16" cy="16" r="5" />
          <circle cx="7" cy="7" r="3" />
          <circle cx="25" cy="10" r="3" />
          <circle cx="16" cy="26" r="3" />
        </g>
      </g>
      <g stroke="currentColor" strokeWidth="1.25" opacity="0.45">
        <circle cx="97" cy="97" r="26" />
        <circle cx="223" cy="118" r="26" />
        <circle cx="160" cy="230" r="26" />
      </g>
      <g fill="currentColor" opacity="0.9">
        <circle cx="97" cy="97" r="7" />
        <circle cx="223" cy="118" r="7" />
        <circle cx="160" cy="230" r="7" />
      </g>
    </svg>
  );
}
