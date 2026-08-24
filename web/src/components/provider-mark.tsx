import { KeyRound } from "lucide-react";
import type { ProviderName } from "@/lib/api";

/**
 * The mark on a provider's sign-in button.
 *
 * Drawn here rather than fetched. A remote favicon is the obvious way to do
 * this and it does not work: the dashboard's content policy allows images from
 * this host and nowhere else, so every one of them would be a blank box —
 * which `curl` cannot tell you, because `curl` does not enforce a policy. The
 * marks are also the one thing on this page a person recognises before they
 * read anything, so they should not depend on a network the browser may not
 * have.
 *
 * Each is the provider's own mark at its own colours, which is what the brand
 * guidelines of all three ask for and also what makes them recognisable at
 * 16px.
 */
export function ProviderMark({ provider }: { provider: ProviderName }) {
  switch (provider) {
    case "google":
      return <GoogleMark />;
    case "github":
      return <GitHubMark />;
    case "entra":
      return <MicrosoftMark />;
    default:
      // Whatever the operator runs has no mark this build could know, and a
      // guess would be somebody else's logo on their button.
      return <KeyRound className="size-4 shrink-0" aria-hidden="true" />;
  }
}

function GoogleMark() {
  return (
    <svg className="size-4 shrink-0" viewBox="0 0 18 18" aria-hidden="true">
      <path
        fill="#4285F4"
        d="M17.64 9.2c0-.64-.06-1.25-.16-1.84H9v3.48h4.84a4.14 4.14 0 0 1-1.8 2.72v2.26h2.92c1.7-1.57 2.68-3.88 2.68-6.62Z"
      />
      <path
        fill="#34A853"
        d="M9 18c2.43 0 4.47-.8 5.96-2.18l-2.92-2.26c-.8.54-1.84.86-3.04.86-2.34 0-4.32-1.58-5.03-3.7H.96v2.33A9 9 0 0 0 9 18Z"
      />
      <path
        fill="#FBBC05"
        d="M3.97 10.72a5.4 5.4 0 0 1 0-3.44V4.95H.96a9 9 0 0 0 0 8.1l3.01-2.33Z"
      />
      <path
        fill="#EA4335"
        d="M9 3.58c1.32 0 2.5.46 3.44 1.35l2.58-2.58C13.46.9 11.43 0 9 0A9 9 0 0 0 .96 4.95l3.01 2.33C4.68 5.16 6.66 3.58 9 3.58Z"
      />
    </svg>
  );
}

function GitHubMark() {
  return (
    <svg
      className="size-4 shrink-0 fill-foreground" viewBox="0 0 16 16"
      aria-hidden="true"
    >
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.4 7.4 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  );
}

function MicrosoftMark() {
  return (
    <svg className="size-4 shrink-0" viewBox="0 0 16 16" aria-hidden="true">
      <path fill="#F25022" d="M0 0h7.6v7.6H0z" />
      <path fill="#7FBA00" d="M8.4 0H16v7.6H8.4z" />
      <path fill="#00A4EF" d="M0 8.4h7.6V16H0z" />
      <path fill="#FFB900" d="M8.4 8.4H16V16H8.4z" />
    </svg>
  );
}
